package server

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
