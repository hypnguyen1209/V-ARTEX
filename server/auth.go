package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const (
	jwtKeyFilename      = "jwt.key"
	jwtKeyVersionPrefix = "v2:"
	jwtKeyBackupSuffix  = ".legacy"
	jwtKeyBytes         = 32
	authPassKey         = "auth.password_hash"
	jwtTTL              = 7 * 24 * time.Hour
	setupTokenEnv       = "ARTEX_SETUP_TOKEN"
	adminPasswordEnv    = "ARTEX_ADMIN_PASSWORD"
	setupTokenBytes     = 32
	authFailureWindow   = 10 * time.Minute
	authInitialCooldown = 30 * time.Second
	authMaxCooldown     = 10 * time.Minute
	authMaxClients      = 10000

	// The minimum matches the setup page validation; frontend-only validation
	// can be bypassed through the API. bcrypt imposes a 72-byte maximum:
	// GenerateFromPassword returns ErrPasswordTooLong beyond that limit.
	minPasswordRunes = 8
	maxPasswordBytes = 72
)

// errDataSourceUnavailable is the common response when password reads fail.
// Never interpret a read failure as an unset password: authInit once allowed
// an unauthenticated request to overwrite the administrator password this way.
const errDataSourceUnavailable = "Data source temporarily unavailable; please try again later"

var passHashCacheTTL = 2 * time.Second

func (s *Server) signingKey(hash string) []byte {
	if hash == "" {
		return append([]byte(nil), s.jwtKey...)
	}
	mac := hmac.New(sha256.New, s.jwtKey)
	mac.Write([]byte(hash))
	return mac.Sum(nil)
}

func (s *Server) readPassHash() (string, error) {
	if s.m == nil || s.m.pg == nil {
		s.authMu.Lock()
		defer s.authMu.Unlock()
		return s.passHash, nil
	}
	hash, _, err := s.m.pg.GetSetting(authPassKey)
	return hash, err
}

func (s *Server) setPassHash(hash string) {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	s.passHash = hash
	s.passHashAt = time.Now()
	s.passHashLoaded = true
}

func (s *Server) reloadPassHash() (string, error) {
	if s.m == nil || s.m.pg == nil {
		return s.readPassHash()
	}
	// Serialize reads with password changes so an older read cannot replace a new hash.
	s.authMu.Lock()
	defer s.authMu.Unlock()
	hash, err := s.readPassHash()
	if err != nil {
		return "", err
	}
	s.passHash = hash
	s.passHashAt = time.Now()
	s.passHashLoaded = true
	return hash, nil
}

func (s *Server) cachedPassHash() (string, error) {
	s.authMu.Lock()
	if s.passHashLoaded && (s.m == nil || s.m.pg == nil || time.Since(s.passHashAt) < passHashCacheTTL) {
		hash := s.passHash
		s.authMu.Unlock()
		return hash, nil
	}
	s.authMu.Unlock()
	return s.reloadPassHash()
}

func (s *Server) verifyToken(token string) bool {
	if token == "" {
		return false
	}
	hash, err := s.cachedPassHash()
	if err != nil {
		return false
	}
	if verifyJWT(token, s.signingKey(hash)) {
		return true
	}
	if s.m == nil || s.m.pg == nil {
		return false
	}
	current, err := s.reloadPassHash()
	return err == nil && current != hash && verifyJWT(token, s.signingKey(current))
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

func (s *Server) currentSetupToken() string {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if s.setupToken != "" {
		return s.setupToken
	}
	if token := strings.TrimSpace(os.Getenv(setupTokenEnv)); token != "" {
		s.setupToken = token
		log.Printf("[auth] Setup token loaded from %s", setupTokenEnv)
		return token
	}
	b, err := randomBytes(setupTokenBytes)
	if err != nil {
		log.Printf("[auth] Generate setup token: %v", err)
		return ""
	}
	s.setupToken = base64.RawURLEncoding.EncodeToString(b)
	log.Printf("[auth] Setup token: %s", s.setupToken)
	log.Printf("[auth] Enter this setup token on the setup page to initialize user ARTEX")
	return s.setupToken
}

func (s *Server) checkSetupToken(token string) bool {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	return s.setupToken != "" && subtle.ConstantTimeCompare([]byte(s.setupToken), []byte(strings.TrimSpace(token))) == 1
}

func (s *Server) clearSetupToken() {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	s.setupToken = ""
}

func (s *Server) bootstrapAuth() {
	if s.m == nil || s.m.pg == nil {
		return
	}
	hash, err := s.reloadPassHash()
	if err != nil {
		log.Printf("[auth] Read password hash: %v", err)
		return
	}
	if hash != "" {
		return
	}
	if password := os.Getenv(adminPasswordEnv); password != "" {
		if reason := validatePassword(password); reason != "" {
			log.Printf("[auth] Invalid %s: %s", adminPasswordEnv, reason)
		} else {
			hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
			if err != nil {
				log.Printf("[auth] Hash password from %s: %v", adminPasswordEnv, err)
				return
			}
			inserted, err := s.m.pg.InsertSettingIfAbsent(authPassKey, string(hash))
			if err != nil {
				log.Printf("[auth] Initialize password from %s: %v", adminPasswordEnv, err)
				return
			}
			if !inserted {
				if _, err := s.reloadPassHash(); err != nil {
					log.Printf("[auth] Reload initialized password hash: %v", err)
				}
				return
			}
			s.setPassHash(string(hash))
			log.Printf("[auth] Initialized user ARTEX from %s", adminPasswordEnv)
			return
		}
	}
	s.currentSetupToken()
}

type authAttempts struct {
	failures     int
	windowStart  time.Time
	lastSeen     time.Time
	blockedUntil time.Time
	cooldown     time.Duration
}

type authLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	clients map[string]*authAttempts
}

func (s *Server) limiter() *authLimiter {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if s.loginLimiter == nil {
		s.loginLimiter = &authLimiter{now: time.Now, clients: make(map[string]*authAttempts)}
	}
	return s.loginLimiter
}

// attempts is called with l.mu held.
func (l *authLimiter) attempts(peer string, now time.Time) *authAttempts {
	a := l.clients[peer]
	if a != nil && now.Sub(a.lastSeen) > authFailureWindow {
		delete(l.clients, peer)
		return nil
	}
	return a
}

func (l *authLimiter) allow(peer string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if a := l.attempts(peer, now); a != nil {
		a.lastSeen = now
		if wait := a.blockedUntil.Sub(now); wait > 0 {
			return false, wait
		}
	}
	return true, 0
}

func (l *authLimiter) failure(peer string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	a := l.attempts(peer, now)
	if a == nil {
		if l.clients == nil {
			l.clients = make(map[string]*authAttempts)
		}
		if len(l.clients) >= authMaxClients {
			var oldest string
			var oldestRecord *authAttempts
			for key, record := range l.clients {
				if oldestRecord == nil || record.lastSeen.Before(oldestRecord.lastSeen) {
					oldest, oldestRecord = key, record
				}
			}
			delete(l.clients, oldest)
		}
		a = &authAttempts{windowStart: now}
		l.clients[peer] = a
	}
	a.lastSeen = now
	if now.Before(a.blockedUntil) {
		return
	}
	if a.cooldown == 0 && now.Sub(a.windowStart) > authFailureWindow {
		a.failures = 0
		a.windowStart = now
	}
	if a.failures < 5 {
		a.failures++
	}
	if a.failures >= 5 {
		if a.cooldown == 0 {
			a.cooldown = authInitialCooldown
		} else if a.cooldown < authMaxCooldown {
			a.cooldown *= 2
			if a.cooldown > authMaxCooldown {
				a.cooldown = authMaxCooldown
			}
		}
		a.blockedUntil = now.Add(a.cooldown)
	}
}

func (l *authLimiter) reset(peer string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.clients, peer)
}

func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func clientLogID(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		return fmt.Sprintf("%s (untrusted X-Forwarded-For: %q)", r.RemoteAddr, forwarded)
	}
	return r.RemoteAddr
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = strings.Trim(addr, "[]")
	}
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

// validatePassword returns an empty string on success, otherwise a displayable reason.
func validatePassword(pw string) string {
	if utf8.RuneCountInString(pw) < minPasswordRunes {
		return fmt.Sprintf("Password must contain at least %d characters", minPasswordRunes)
	}
	if len(pw) > maxPasswordBytes {
		return fmt.Sprintf("Password must not exceed %d bytes", maxPasswordBytes)
	}
	return ""
}

type jwtKeyFileOps struct {
	rename func(string, string) error
	remove func(string) error
}

// loadOrCreateJWTKey loads a versioned signing key outside the browsable workspace.
// Legacy keys at either location are replaced once, invalidating existing sessions.
func loadOrCreateJWTKey(keyDir, dataDir string) ([]byte, error) {
	return loadOrCreateJWTKeyWithOps(keyDir, dataDir, jwtKeyFileOps{rename: os.Rename, remove: os.Remove})
}

func loadOrCreateJWTKeyWithOps(keyDir, dataDir string, ops jwtKeyFileOps) ([]byte, error) {
	path := filepath.Join(keyDir, jwtKeyFilename)
	backup := path + jwtKeyBackupSuffix
	key, currentExists, err := readJWTKey(path)
	if err != nil {
		return nil, err
	}
	backupKey, backupExists, err := readJWTKey(backup)
	if err != nil {
		return nil, err
	}
	if backupKey != nil {
		return nil, fmt.Errorf("jwt key backup must contain an unversioned key")
	}
	if currentExists && key == nil && backupExists {
		return nil, fmt.Errorf("both current and backup jwt keys are unversioned")
	}

	workspace := filepath.Join(dataDir, jwtKeyFilename)
	workspaceExists := false
	if workspace != path {
		keyDirInfo, err := os.Stat(keyDir)
		if err != nil {
			return nil, fmt.Errorf("stat jwt key directory: %w", err)
		}
		dataDirInfo, err := os.Stat(dataDir)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat workspace directory: %w", err)
		}
		if err != nil || !os.SameFile(keyDirInfo, dataDirInfo) {
			_, workspaceExists, err = readJWTKey(workspace)
			if err != nil {
				return nil, err
			}
		}
	}
	if key == nil {
		key, err = writeVersionedJWTKey(path, currentExists, ops)
		if err != nil {
			return nil, err
		}
	}
	if backupExists {
		if err := ops.remove(backup); err != nil {
			return nil, fmt.Errorf("remove jwt key backup: %w", err)
		}
	}
	if workspaceExists {
		if err := ops.remove(workspace); err != nil {
			return nil, fmt.Errorf("remove stale workspace jwt key: %w", err)
		}
	}
	return key, nil
}

func readJWTKey(path string) ([]byte, bool, error) {
	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("stat jwt key %s: %w", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("read jwt key %s: %w", path, err)
	}
	key, err := decodeVersionedJWTKey(data)
	if err != nil {
		return nil, true, fmt.Errorf("invalid jwt key %s: %w", path, err)
	}
	return key, true, nil
}

// decodeVersionedJWTKey returns nil for a valid legacy key, which must be rotated.
func decodeVersionedJWTKey(data []byte) ([]byte, error) {
	text := string(data)
	if !strings.HasPrefix(strings.TrimSpace(text), jwtKeyVersionPrefix) {
		if len(strings.TrimSpace(text)) < jwtKeyBytes {
			return nil, fmt.Errorf("legacy key must contain at least %d bytes", jwtKeyBytes)
		}
		return nil, nil
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(text, jwtKeyVersionPrefix), "\n")
	key, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(key) != jwtKeyBytes || jwtKeyVersionPrefix+base64.RawURLEncoding.EncodeToString(key)+"\n" != text {
		return nil, fmt.Errorf("versioned key must encode exactly %d bytes as raw base64url followed by one newline", jwtKeyBytes)
	}
	return key, nil
}

func writeVersionedJWTKey(path string, rotate bool, ops jwtKeyFileOps) ([]byte, error) {
	key := make([]byte, jwtKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate jwt key: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".jwt.key-*")
	if err != nil {
		return nil, fmt.Errorf("create jwt key temp file: %w", err)
	}
	defer ops.remove(temp.Name())
	_, writeErr := temp.WriteString(jwtKeyVersionPrefix + base64.RawURLEncoding.EncodeToString(key) + "\n")
	if writeErr == nil {
		writeErr = temp.Sync()
	}
	closeErr := temp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return nil, fmt.Errorf("write jwt key: %w", err)
	}
	backup := path + jwtKeyBackupSuffix
	if rotate {
		if err := ops.rename(path, backup); err != nil {
			return nil, fmt.Errorf("back up legacy jwt key: %w", err)
		}
	}
	if err := ops.rename(temp.Name(), path); err != nil {
		if rotate {
			if restoreErr := ops.rename(backup, path); restoreErr != nil {
				err = errors.Join(err, fmt.Errorf("restore legacy jwt key: %w", restoreErr))
			}
		}
		return nil, fmt.Errorf("install versioned jwt key: %w", err)
	}
	if rotate {
		if err := ops.remove(backup); err != nil {
			return nil, fmt.Errorf("remove jwt key backup: %w", err)
		}
	}
	log.Printf("[auth] New JWT key written to %s", path)
	return key, nil
}

// signJWT issues a 7-day HS256 token for user ARTEX.
func signJWT(key []byte) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   "ARTEX",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(jwtTTL)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	}).SignedString(key)
}

// verifyJWT returns true when tokenStr is a valid, non-expired HS256 token.
func verifyJWT(tokenStr string, key []byte) bool {
	t, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return key, nil
	})
	return err == nil && t.Valid
}

// extractToken reads the JWT from Authorization: Bearer header,
// artex_token cookie, or ?token= query param (for SSE connections).
func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if c, err := r.Cookie("artex_token"); err == nil && c.Value != "" {
		return c.Value
	}
	return r.URL.Query().Get("token")
}

// requireAuth wraps h with JWT validation.
// /api/auth/* and /api/health are exempt.
func (s *Server) requireAuth(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/api/auth/") || p == "/api/health" {
			h.ServeHTTP(w, r)
			return
		}
		tok := extractToken(r)
		if tok == "" {
			writeErr(w, 401, "Unauthorized")
			return
		}
		if !verifyJWT(tok, s.jwtKey) {
			writeErr(w, 401, "Token is invalid or expired")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// GET /api/auth/status — reports whether the admin password has been initialised.
// Read failures must return 503 rather than initialized:false, which sends
// the frontend to /setup (login/page.tsx). Returning 200 for a database failure
// would steer users toward overwriting an existing password.
func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	hash, _, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	writeJSON(w, 200, map[string]any{"initialized": hash != ""})
}

// POST /api/auth/init — sets the password for the first time; rejected if already set.
func (s *Server) authInit(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	existing, _, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if existing != "" {
		writeErr(w, 403, "Password has already been set")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil || req.Password == "" {
		writeErr(w, 400, "Password must not be empty")
		return
	}
	if msg := validatePassword(req.Password); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, "Failed to hash password")
		return
	}
	// Use INSERT ... ON CONFLICT DO NOTHING rather than upsert. GetSetting above
	// is only a fast rejection; the primary key enforces first initialization.
	// bcrypt takes tens of milliseconds; concurrent requests may initialize first, and reads can fail.
	inserted, err := pg.InsertSettingIfAbsent(authPassKey, string(hash))
	if err != nil {
		writeErr(w, 500, "Failed to save: "+err.Error())
		return
	}
	if !inserted {
		writeErr(w, 403, "Password has already been set")
		return
	}
	tok, err := signJWT(s.jwtKey)
	if err != nil {
		writeErr(w, 500, "Failed to generate token")
		return
	}
	writeJSON(w, 200, map[string]any{"token": tok})
}

// POST /api/auth/change-password — changes the admin password. Requires a valid
// token (this route is under /api/auth/* which requireAuth exempts, so the token
// is validated here) AND the current password.
func (s *Server) authChangePassword(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	if !verifyJWT(extractToken(r), s.jwtKey) {
		writeErr(w, 401, "Unauthorized")
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "Invalid request format")
		return
	}
	if req.NewPassword == "" {
		writeErr(w, 400, "New password must not be empty")
		return
	}
	if msg := validatePassword(req.NewPassword); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	hash, ok, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if !ok || hash == "" {
		writeErr(w, 403, "Password is not initialized; set a password first")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.OldPassword)); err != nil {
		writeErr(w, 401, "Current password is incorrect")
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, "Failed to hash password")
		return
	}
	if err := pg.SetSetting(authPassKey, string(newHash)); err != nil {
		writeErr(w, 500, "Failed to save: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// POST /api/auth/login — validates username/password and returns a JWT.
func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "Invalid request format")
		return
	}
	if req.Username != "ARTEX" {
		writeErr(w, 401, "Username or password is incorrect")
		return
	}
	hash, ok, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if !ok || hash == "" {
		writeErr(w, 403, "Password is not initialized; set a password first")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		writeErr(w, 401, "Username or password is incorrect")
		return
	}
	tok, err := signJWT(s.jwtKey)
	if err != nil {
		writeErr(w, 500, "Failed to generate token")
		return
	}
	writeJSON(w, 200, map[string]any{"token": tok})
}
