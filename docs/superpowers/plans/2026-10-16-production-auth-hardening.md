# V-ARTEX Production Authentication Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent first-run account takeover, revoke sessions after password changes, and make V-ARTEX local-only and exact-origin by default without changing its single-admin data model.

**Architecture:** Keep authentication in `server/auth.go`: derive JWT signing keys from the persisted root key and current bcrypt hash, gate first initialization with an in-memory or environment setup token, and share one bounded per-peer limiter across authentication operations. Rotate the legacy root key once into a rollback-safe versioned file, enforce CORS at the existing HTTP middleware, and update the existing setup/change-password UI rather than adding a new auth layer.

**Tech Stack:** Go 1.24 standard library, `golang-jwt/jwt/v5`, `golang.org/x/crypto/bcrypt`, PostgreSQL settings storage, Next.js 16/React 19/TypeScript, Node's built-in test runner, Docker Compose.

---

## Scope and file map

The implementation changes only the approved production-auth project. CI and release workflow hardening remain a separate project.

- Create `server/auth_key_test.go`: versioned JWT root-key creation, rotation, crash recovery, and rollback tests.
- Create `server/auth_setup_test.go`: password-bound signing, setup token, limiter, client identity, and loopback tests.
- Create `server/auth_init_db_test.go`: PostgreSQL-backed bootstrap, initialization, login, password-change, reset, throttling, and concurrency tests.
- Create `server/cors_test.go`: default and environment exact-origin behavior.
- Create `web/src/components/auth-workflows.test.mjs`: frontend API and token-storage regression tests.
- Modify `server/auth.go`: root-key format, setup/bootstrap state, password-hash cache, JWT derivation, limiter, and auth handlers.
- Modify `server/auth_test.go`: preserve fail-closed database tests under the required setup-token contract.
- Modify `server/server.go`: hold bounded auth state, bootstrap it at construction, and replace wildcard CORS.
- Modify `cmd/artex/main.go` and `cmd/artex/main_test.go`: local-only native default and exposure warning wiring.
- Modify `dev.sh`: local-only development backend binding.
- Modify `web/src/lib/api.ts`, `web/src/lib/auth.ts`, `web/src/app/(auth)/setup/page.tsx`, and `web/src/app/(main)/_components/sidebar/change-password-dialog.tsx`: new API contracts and browser behavior.
- Modify `docker-compose.yml`, `.env.example`, `install.sh`, and `README.md`: local-only publishing and bootstrap/CORS operator guidance.

Compatibility consequences are intentional: `POST /api/auth/init` no longer accepts password-only requests; the one-time root-key migration logs every existing browser out; password changes log out every other session. Existing bcrypt passwords, PostgreSQL rows, tasks, skills, and other application data are preserved.

### Task 1: Rotate and version the persisted JWT root key

**Files:**
- Create: `server/auth_key_test.go`
- Modify: `server/auth.go`

- [ ] **Step 1: Write root-key lifecycle tests**

Create `server/auth_key_test.go` with table-driven coverage for a new key, a legacy key in the current location, a legacy key in the former browsable data location, and an interrupted rotation:

```go
package server

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func readVersionedKeyFile(t *testing.T, path string) ([]byte, string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(text, jwtKeyVersionPrefix) {
		t.Fatalf("key file %q is not versioned", text)
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(text, jwtKeyVersionPrefix))
	if err != nil {
		t.Fatalf("decode key payload: %v", err)
	}
	return payload, text
}

func TestLoadOrCreateJWTKeyCreatesVersionedKey(t *testing.T) {
	keyDir, dataDir := t.TempDir(), t.TempDir()
	key, err := loadOrCreateJWTKey(keyDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != jwtKeyBytes {
		t.Fatalf("key length = %d, want %d", len(key), jwtKeyBytes)
	}
	onDisk, _ := readVersionedKeyFile(t, filepath.Join(keyDir, jwtKeyFilename))
	if string(onDisk) != string(key) {
		t.Fatal("returned key differs from versioned payload")
	}
	info, err := os.Stat(filepath.Join(keyDir, jwtKeyFilename))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions = %o, want 600", info.Mode().Perm())
	}
	again, err := loadOrCreateJWTKey(keyDir, dataDir)
	if err != nil || string(again) != string(key) {
		t.Fatalf("second load changed the key: key=%q err=%v", again, err)
	}
}

func TestLoadOrCreateJWTKeyRotatesLegacyKeyOnce(t *testing.T) {
	keyDir, dataDir := t.TempDir(), t.TempDir()
	path := filepath.Join(keyDir, jwtKeyFilename)
	legacy := []byte(strings.Repeat("L", 32))
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	legacyToken, err := signJWT(legacy)
	if err != nil {
		t.Fatal(err)
	}

	rotated, err := loadOrCreateJWTKey(keyDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if string(rotated) == string(legacy) || verifyJWT(legacyToken, rotated) {
		t.Fatal("legacy key or token survived rotation")
	}
	_, opaqueOldBinaryKey := readVersionedKeyFile(t, path)
	if verifyJWT(legacyToken, []byte(opaqueOldBinaryKey)) {
		t.Fatal("legacy token revived under rollback behavior")
	}
	hardenedToken, err := signJWT(rotated)
	if err != nil {
		t.Fatal(err)
	}
	if verifyJWT(hardenedToken, []byte(opaqueOldBinaryKey)) {
		t.Fatal("hardened token remained valid under rollback behavior")
	}
	again, err := loadOrCreateJWTKey(keyDir, dataDir)
	if err != nil || string(again) != string(rotated) {
		t.Fatalf("versioned key rotated twice: err=%v", err)
	}
}

func TestLoadOrCreateJWTKeyRotatesWorkspaceLegacyKey(t *testing.T) {
	keyDir, dataDir := t.TempDir(), t.TempDir()
	legacyPath := filepath.Join(dataDir, jwtKeyFilename)
	if err := os.WriteFile(legacyPath, []byte(strings.Repeat("W", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateJWTKey(keyDir, dataDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("workspace key still exists: %v", err)
	}
	readVersionedKeyFile(t, filepath.Join(keyDir, jwtKeyFilename))
}

func TestLoadOrCreateJWTKeyRecoversInterruptedRotation(t *testing.T) {
	keyDir, dataDir := t.TempDir(), t.TempDir()
	backup := filepath.Join(keyDir, jwtKeyFilename+jwtKeyBackupSuffix)
	if err := os.WriteFile(backup, []byte(strings.Repeat("B", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := loadOrCreateJWTKey(keyDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if string(key) == strings.Repeat("B", 32) {
		t.Fatal("interrupted legacy key was not rotated")
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("rotation backup remains: %v", err)
	}
	readVersionedKeyFile(t, filepath.Join(keyDir, jwtKeyFilename))
}
```

- [ ] **Step 2: Run the new tests and confirm the legacy implementation fails**

Run:

```bash
go test ./server -run 'TestLoadOrCreateJWTKey' -count=1
```

Expected: build failure for `jwtKeyVersionPrefix`, `jwtKeyBytes`, and `jwtKeyBackupSuffix`, proving the versioned lifecycle is not implemented yet.

- [ ] **Step 3: Replace the root-key loader with the versioned atomic rotation**

In `server/auth.go`, add `encoding/base64` to imports, remove `math/big`, and replace the old key constants and `loadOrCreateJWTKey` with the following implementation. Keep `signJWT` and `verifyJWT` immediately after it.

```go
const (
	jwtKeyFilename     = "jwt.key"
	jwtKeyVersionPrefix = "v2:"
	jwtKeyBackupSuffix = ".legacy"
	jwtKeyBytes        = 32
	authPassKey        = "auth.password_hash"
	jwtTTL             = 7 * 24 * time.Hour

	minPasswordRunes = 8
	maxPasswordBytes = 72
)

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

func decodeVersionedJWTKey(raw []byte) ([]byte, bool, error) {
	text := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(text, jwtKeyVersionPrefix) {
		return nil, false, nil
	}
	key, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(text, jwtKeyVersionPrefix))
	if err != nil || len(key) != jwtKeyBytes {
		return nil, true, fmt.Errorf("invalid versioned JWT key")
	}
	return key, true, nil
}

func writeVersionedJWTKey(path string, key []byte, rotateCurrent bool) (err error) {
	if len(key) != jwtKeyBytes {
		return fmt.Errorf("JWT key has %d bytes, want %d", len(key), jwtKeyBytes)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".jwt-key-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	content := jwtKeyVersionPrefix + base64.RawURLEncoding.EncodeToString(key) + "\n"
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.WriteString(content); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	backup := path + jwtKeyBackupSuffix
	if rotateCurrent {
		if _, err := os.Stat(backup); err == nil {
			return fmt.Errorf("JWT key rotation backup already exists: %s", backup)
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(path, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		if rotateCurrent {
			_ = os.Rename(backup, path)
		}
		return err
	}
	if rotateCurrent {
		if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func loadOrCreateJWTKey(keyDir, dataDir string) ([]byte, error) {
	path := filepath.Join(keyDir, jwtKeyFilename)
	backup := path + jwtKeyBackupSuffix

	if raw, err := os.ReadFile(path); err == nil {
		if key, versioned, parseErr := decodeVersionedJWTKey(raw); versioned {
			if parseErr != nil {
				return nil, parseErr
			}
			_ = os.Remove(backup)
			return key, nil
		}
		if len(strings.TrimSpace(string(raw))) < jwtKeyBytes {
			return nil, fmt.Errorf("invalid legacy JWT key")
		}
		key, err := randomBytes(jwtKeyBytes)
		if err != nil {
			return nil, fmt.Errorf("generate JWT key: %w", err)
		}
		if err := writeVersionedJWTKey(path, key, true); err != nil {
			return nil, fmt.Errorf("rotate JWT key: %w", err)
		}
		log.Printf("[auth] Rotated JWT key to versioned format; existing sessions were signed out")
		return key, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	if raw, err := os.ReadFile(backup); err == nil {
		if len(strings.TrimSpace(string(raw))) < jwtKeyBytes {
			return nil, fmt.Errorf("invalid JWT key rotation backup")
		}
		key, err := randomBytes(jwtKeyBytes)
		if err != nil {
			return nil, fmt.Errorf("generate JWT key: %w", err)
		}
		if err := writeVersionedJWTKey(path, key, false); err != nil {
			return nil, fmt.Errorf("recover JWT key rotation: %w", err)
		}
		if err := os.Remove(backup); err != nil {
			return nil, fmt.Errorf("remove JWT key rotation backup: %w", err)
		}
		return key, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	legacyPath := filepath.Join(dataDir, jwtKeyFilename)
	if legacyPath != path {
		if raw, err := os.ReadFile(legacyPath); err == nil {
			if len(strings.TrimSpace(string(raw))) < jwtKeyBytes {
				return nil, fmt.Errorf("invalid workspace JWT key")
			}
			key, err := randomBytes(jwtKeyBytes)
			if err != nil {
				return nil, fmt.Errorf("generate JWT key: %w", err)
			}
			if err := writeVersionedJWTKey(path, key, false); err != nil {
				return nil, fmt.Errorf("migrate JWT key: %w", err)
			}
			if err := os.Remove(legacyPath); err != nil {
				return nil, fmt.Errorf("remove workspace JWT key: %w", err)
			}
			log.Printf("[auth] Moved and rotated JWT key outside the browsable workspace")
			return key, nil
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}

	key, err := randomBytes(jwtKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("generate JWT key: %w", err)
	}
	if err := writeVersionedJWTKey(path, key, false); err != nil {
		return nil, fmt.Errorf("write JWT key: %w", err)
	}
	log.Printf("[auth] New versioned JWT key written to %s", path)
	return key, nil
}
```

This is deliberately one version only. The old binary reads the entire `v2:<payload>` text as an opaque HMAC key, while the hardened binary reads only the decoded payload, so neither pre-migration nor hardened tokens revive after a rollback.

- [ ] **Step 4: Format and run the key tests**

Run:

```bash
gofmt -w server/auth.go server/auth_key_test.go
go test ./server -run 'TestLoadOrCreateJWTKey' -count=1
```

Expected: all four root-key tests pass.

- [ ] **Step 5: Commit the root-key migration**

```bash
git add server/auth.go server/auth_key_test.go
git commit -m "fix(auth): rotate and version JWT root key"
```

### Task 2: Add bounded authentication state and startup bootstrap

**Files:**
- Create: `server/auth_setup_test.go`
- Modify: `server/auth.go`
- Modify: `server/server.go`

- [ ] **Step 1: Write unit tests for derivation, setup tokens, limiting, and peer identity**

Create `server/auth_setup_test.go` with these tests:

```go
package server

import (
	"bytes"
	"encoding/base64"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSigningKeyBoundToPasswordHash(t *testing.T) {
	s := &Server{jwtKey: []byte(strings.Repeat("k", 32))}
	if !bytes.Equal(s.signingKey(""), s.jwtKey) {
		t.Fatal("empty password hash must retain raw-key behavior for DB-less tests")
	}
	token, err := signJWT(s.signingKey("hash-A"))
	if err != nil {
		t.Fatal(err)
	}
	if !verifyJWT(token, s.signingKey("hash-A")) {
		t.Fatal("token did not verify with the same password hash")
	}
	if verifyJWT(token, s.signingKey("hash-B")) || verifyJWT(token, s.jwtKey) {
		t.Fatal("token survived a password-hash change")
	}
}

func TestVerifyTokenInvalidatedByPasswordChange(t *testing.T) {
	s := &Server{jwtKey: []byte(strings.Repeat("v", 32))}
	s.setPassHash("hash-A")
	token, err := signJWT(s.signingKey("hash-A"))
	if err != nil {
		t.Fatal(err)
	}
	if !s.verifyToken(token) {
		t.Fatal("current token was rejected")
	}
	s.setPassHash("hash-B")
	if s.verifyToken(token) || s.verifyToken("") {
		t.Fatal("old or empty token was accepted")
	}
}

func TestSetupTokenFromEnvironmentIsNotLogged(t *testing.T) {
	t.Setenv(setupTokenEnv, "fixed-setup-token")
	var logs bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldWriter) })

	s := &Server{}
	if got := s.currentSetupToken(); got != "fixed-setup-token" {
		t.Fatalf("setup token = %q", got)
	}
	if strings.Contains(logs.String(), "fixed-setup-token") {
		t.Fatal("configured setup token leaked to logs")
	}
	if !s.checkSetupToken(" fixed-setup-token\n") || s.checkSetupToken("wrong") {
		t.Fatal("setup token comparison returned the wrong result")
	}
}

func TestRandomSetupTokenContainsThirtyTwoRandomBytesAndLogsOnce(t *testing.T) {
	t.Setenv(setupTokenEnv, "")
	var logs bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldWriter) })

	s := &Server{}
	token := s.currentSetupToken()
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != setupTokenBytes {
		t.Fatalf("generated token is not %d random bytes: len=%d err=%v", setupTokenBytes, len(raw), err)
	}
	if s.currentSetupToken() != token || strings.Count(logs.String(), token) != 1 {
		t.Fatal("generated setup token was changed or logged more than once")
	}
	s.clearSetupToken()
	if s.currentSetupToken() == token {
		t.Fatal("cleared setup token was reused")
	}
}

func TestAuthLimiterCooldownExpiryResetAndBound(t *testing.T) {
	limiter := newAuthLimiter()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }
	const peer = "203.0.113.9"

	for i := 0; i < authFreeFailures; i++ {
		if !limiter.allow(peer) {
			t.Fatalf("attempt %d was blocked too early", i+1)
		}
		limiter.fail(peer)
	}
	if limiter.allow(peer) {
		t.Fatal("peer was not blocked after the free failures")
	}
	now = now.Add(authCooldownBase + time.Second)
	if !limiter.allow(peer) {
		t.Fatal("peer stayed blocked after the first cooldown")
	}
	limiter.fail(peer)
	now = now.Add(authCooldownBase + time.Second)
	if limiter.allow(peer) {
		t.Fatal("second cooldown did not double")
	}
	now = now.Add(authCooldownBase)
	if !limiter.allow(peer) {
		t.Fatal("peer stayed blocked after the doubled cooldown")
	}
	limiter.reset(peer)
	if !limiter.allow(peer) {
		t.Fatal("successful authentication did not reset the limiter")
	}
	for i := 0; i < 20; i++ {
		limiter.fail(peer)
	}
	if got := limiter.clients[peer].until.Sub(now); got > authCooldownMax {
		t.Fatalf("cooldown = %v, exceeds %v", got, authCooldownMax)
	}
	now = now.Add(authFailureWindow + time.Second)
	if !limiter.allow(peer) {
		t.Fatal("stale failure record did not expire")
	}
	for i := 0; i <= authLimiterMaxItems; i++ {
		limiter.fail("peer-" + strings.Repeat("x", i%31) + string(rune(i+1)))
	}
	if len(limiter.clients) > authLimiterMaxItems {
		t.Fatalf("limiter grew to %d entries", len(limiter.clients))
	}
	if !limiter.allow("peer-unknown") {
		t.Fatal("an unrelated peer was blocked")
	}
}

func TestClientKeyIgnoresForwardedFor(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	r.RemoteAddr = "198.51.100.7:40000"
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	if got := clientKey(r); got != "198.51.100.7" {
		t.Fatalf("limiter key = %q, want TCP peer", got)
	}
	if id := clientLogID(r); !strings.Contains(id, r.RemoteAddr) || !strings.Contains(id, "1.2.3.4") {
		t.Fatalf("audit identity omitted peer context: %q", id)
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	cases := map[string]bool{
		":8787": false, "0.0.0.0:8787": false, "[::]:8787": false,
		"10.0.0.5:8787": false, "127.0.0.1:8787": true,
		"localhost:8787": true, "[::1]:8787": true,
	}
	for addr, want := range cases {
		if got := isLoopbackAddr(addr); got != want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}
```

- [ ] **Step 2: Run the auth-state tests and confirm they fail**

Run:

```bash
go test ./server -run 'Test(SigningKey|VerifyToken|SetupToken|RandomSetup|AuthLimiter|ClientKey|IsLoopback)' -count=1
```

Expected: build failure for the new auth state, limiter, setup-token, and loopback symbols.

- [ ] **Step 3: Add the auth state to `Server` and bootstrap it during construction**

Add these fields after `jwtKey` in `server/server.go`:

```go
	authMu        sync.Mutex
	initMu        sync.Mutex
	passHash      string
	passHashAt    time.Time
	passHashLoaded bool
	setupToken    string
	loginLimiter  *authLimiter
```

Immediately after the `Server` literal in `New`, before `s.initSideQuestions()`, initialize authentication:

```go
	s.bootstrapAuth()
```

- [ ] **Step 4: Add password-bound signing and the two-second database cache**

Add `crypto/hmac`, `crypto/sha256`, `crypto/subtle`, `net`, and `sync` to `server/auth.go` imports. Add these constants beside the existing auth constants:

```go
const (
	setupTokenEnv    = "ARTEX_SETUP_TOKEN"
	adminPasswordEnv = "ARTEX_ADMIN_PASSWORD"
	setupTokenBytes  = 32
)

var passHashCacheTTL = 2 * time.Second
```

Add these methods after `verifyJWT`:

```go
func (s *Server) signingKey(hash string) []byte {
	if hash == "" {
		return s.jwtKey
	}
	m := hmac.New(sha256.New, s.jwtKey)
	_, _ = m.Write([]byte(hash))
	return m.Sum(nil)
}

func (s *Server) readPassHash() (string, error) {
	if s.m == nil || s.m.pg == nil {
		return "", nil
	}
	hash, _, err := s.m.pg.GetSetting(authPassKey)
	return hash, err
}

func (s *Server) setPassHash(hash string) {
	s.authMu.Lock()
	s.passHash = hash
	s.passHashAt = time.Now()
	s.passHashLoaded = true
	s.authMu.Unlock()
}

func (s *Server) reloadPassHash() (string, error) {
	hash, err := s.readPassHash()
	if err != nil {
		return "", err
	}
	s.setPassHash(hash)
	return hash, nil
}

func (s *Server) cachedPassHash() (string, error) {
	s.authMu.Lock()
	fresh := s.passHashLoaded && time.Since(s.passHashAt) < passHashCacheTTL
	hash := s.passHash
	s.authMu.Unlock()
	if fresh {
		return hash, nil
	}
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
	fresh, err := s.reloadPassHash()
	return err == nil && fresh != hash && verifyJWT(token, s.signingKey(fresh))
}
```

- [ ] **Step 5: Add setup-token creation and fail-closed startup bootstrap**

Add these functions after the password-hash cache. The valid headless path uses the existing first-write-only database method; invalid environment passwords fall back to a setup token, while hash or write failures return without issuing one.

```go
func (s *Server) bootstrapAuth() {
	if s.m == nil || s.m.pg == nil {
		return
	}
	hash, err := s.reloadPassHash()
	if err != nil {
		log.Printf("[auth] Cannot read password state; bootstrap is disabled: %v", err)
		return
	}
	if hash != "" {
		return
	}
	if password := os.Getenv(adminPasswordEnv); password != "" {
		if reason := validatePassword(password); reason != "" {
			log.Printf("[auth] %s is invalid: %s; falling back to setup-token bootstrap", adminPasswordEnv, reason)
		} else {
			encoded, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
			if err != nil {
				log.Printf("[auth] Cannot hash %s; bootstrap is disabled: %v", adminPasswordEnv, err)
				return
			}
			inserted, err := s.m.pg.InsertSettingIfAbsent(authPassKey, string(encoded))
			if err != nil {
				log.Printf("[auth] Cannot persist %s; bootstrap is disabled: %v", adminPasswordEnv, err)
				return
			}
			if !inserted {
				if _, err := s.reloadPassHash(); err != nil {
					log.Printf("[auth] Cannot reload concurrently initialized password: %v", err)
				}
				return
			}
			s.setPassHash(string(encoded))
			log.Printf("[auth] Administrator password initialized from %s for user ARTEX", adminPasswordEnv)
			return
		}
	}
	_ = s.currentSetupToken()
}

func (s *Server) currentSetupToken() string {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if s.setupToken != "" {
		return s.setupToken
	}
	if configured := strings.TrimSpace(os.Getenv(setupTokenEnv)); configured != "" {
		s.setupToken = configured
		log.Printf("[auth] Setup token loaded from %s and omitted from logs", setupTokenEnv)
		return s.setupToken
	}
	raw, err := randomBytes(setupTokenBytes)
	if err != nil {
		log.Printf("[auth] Cannot generate setup token: %v", err)
		return ""
	}
	s.setupToken = base64.RawURLEncoding.EncodeToString(raw)
	log.Printf("[auth] Setup token: %s", s.setupToken)
	log.Printf("[auth] Enter this value on /setup or send it as setup_token to POST /api/auth/init")
	return s.setupToken
}

func (s *Server) checkSetupToken(given string) bool {
	want := s.currentSetupToken()
	return want != "" && subtle.ConstantTimeCompare([]byte(want), []byte(strings.TrimSpace(given))) == 1
}

func (s *Server) clearSetupToken() {
	s.authMu.Lock()
	s.setupToken = ""
	s.authMu.Unlock()
}
```

- [ ] **Step 6: Add the bounded limiter, TCP-peer key, and loopback helpers**

Add this code before `extractToken` in `server/auth.go`:

```go
const (
	authFreeFailures    = 5
	authFailureWindow   = 10 * time.Minute
	authCooldownBase    = 30 * time.Second
	authCooldownMax     = 10 * time.Minute
	authLimiterMaxItems = 10_000
)

type authAttempts struct {
	fails int
	last  time.Time
	until time.Time
}

type authLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	clients map[string]*authAttempts
}

func newAuthLimiter() *authLimiter {
	return &authLimiter{now: time.Now, clients: map[string]*authAttempts{}}
}

func (l *authLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.clients[key]
	if a == nil {
		return true
	}
	now := l.now()
	if now.Sub(a.last) > authFailureWindow {
		delete(l.clients, key)
		return true
	}
	return !now.Before(a.until)
}

func (l *authLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	a := l.clients[key]
	if a == nil || now.Sub(a.last) > authFailureWindow {
		if len(l.clients) >= authLimiterMaxItems {
			oldestKey := ""
			var oldest time.Time
			for candidate, item := range l.clients {
				if oldestKey == "" || item.last.Before(oldest) {
					oldestKey, oldest = candidate, item.last
				}
			}
			delete(l.clients, oldestKey)
		}
		a = &authAttempts{}
		l.clients[key] = a
	}
	a.fails++
	a.last = now
	if extra := a.fails - authFreeFailures; extra >= 0 {
		cooldown := authCooldownBase << uint(extra)
		if cooldown <= 0 || cooldown > authCooldownMax {
			cooldown = authCooldownMax
		}
		a.until = now.Add(cooldown)
	}
}

func (l *authLimiter) reset(key string) {
	l.mu.Lock()
	delete(l.clients, key)
	l.mu.Unlock()
}

func (s *Server) limiter() *authLimiter {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if s.loginLimiter == nil {
		s.loginLimiter = newAuthLimiter()
	}
	return s.loginLimiter
}

func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func clientLogID(r *http.Request) string {
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		return r.RemoteAddr + " (untrusted X-Forwarded-For: " + forwarded + ")"
	}
	return r.RemoteAddr
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if host == "" {
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
```

- [ ] **Step 7: Format and run the focused tests**

Run:

```bash
gofmt -w server/auth.go server/auth_setup_test.go server/server.go
go test ./server -run 'Test(SigningKey|VerifyToken|SetupToken|RandomSetup|AuthLimiter|ClientKey|IsLoopback)' -count=1
```

Expected: all focused unit tests pass, and the existing server package still compiles.

- [ ] **Step 8: Commit auth state and bootstrap primitives**

```bash
git add server/auth.go server/auth_setup_test.go server/server.go
git commit -m "feat(auth): add secure bootstrap primitives"
```

### Task 3: Enforce the hardened authentication API contracts

**Files:**
- Create: `server/auth_init_db_test.go`
- Modify: `server/auth.go`
- Modify: `server/auth_test.go`

- [ ] **Step 1: Add a PostgreSQL-backed authentication test harness**

Create `server/auth_init_db_test.go` with the harness below. It follows the repository's existing convention: skip only when no PostgreSQL configuration is available, clean the one auth setting before and after each test, and route through `requireAuth`.

```go
package server

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hypnguyen1209/V-ARTEX/db"
	"golang.org/x/crypto/bcrypt"
)

type authTestRig struct {
	t *testing.T
	s *Server
	h http.Handler
}

func newAuthTestRig(t *testing.T) *authTestRig {
	t.Helper()
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skipf("no database config (%v)", err)
	}
	database, err := db.Open(dsn)
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	clear := func() { _, _ = database.Exec(`DELETE FROM settings WHERE key = $1`, authPassKey) }
	clear()
	t.Cleanup(clear)
	t.Setenv(setupTokenEnv, "")
	t.Setenv(adminPasswordEnv, "")

	s := &Server{m: &Manager{pg: database}, jwtKey: []byte(strings.Repeat("s", 32))}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/auth/status", s.authStatus)
	mux.HandleFunc("POST /api/auth/init", s.authInit)
	mux.HandleFunc("POST /api/auth/login", s.authLogin)
	mux.HandleFunc("POST /api/auth/change-password", s.authChangePassword)
	mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return &authTestRig{t: t, s: s, h: s.requireAuth(mux)}
}

func (r *authTestRig) do(method, path, token string, body map[string]any) (int, map[string]any) {
	r.t.Helper()
	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			r.t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &payload)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	result := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &result)
	return rec.Code, result
}

func (r *authTestRig) expect(wantCode, gotCode int, body map[string]any, wantError string) {
	r.t.Helper()
	if gotCode != wantCode {
		r.t.Fatalf("status = %d, want %d; body=%v", gotCode, wantCode, body)
	}
	if wantError != "" && body["error"] != wantError {
		r.t.Fatalf("error = %v, want %q", body["error"], wantError)
	}
}
```

- [ ] **Step 2: Add integration tests for setup-token initialization and concurrency**

Append these tests to `server/auth_init_db_test.go`:

```go
func TestAuthInitRequiresSetupTokenDB(t *testing.T) {
	rig := newAuthTestRig(t)
	rig.s.bootstrapAuth()
	setupToken := rig.s.currentSetupToken()
	if setupToken == "" {
		t.Fatal("startup did not issue a setup token")
	}

	code, body := rig.do(http.MethodGet, "/api/auth/status", "", nil)
	rig.expect(http.StatusOK, code, body, "")
	if body["initialized"] != false || body["setup_token_required"] != true {
		t.Fatalf("unexpected pre-init status: %v", body)
	}
	if _, exposed := body["setup_token"]; exposed {
		t.Fatalf("authentication status exposed the setup token: %v", body)
	}

	code, body = rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{"password": "attacker-password-123"})
	rig.expect(http.StatusBadRequest, code, body, authErrMissingSetupToken)
	code, body = rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{
		"password": "attacker-password-123", "setup_token": "wrong",
	})
	rig.expect(http.StatusForbidden, code, body, authErrSetupTokenInvalid)
	if hash, _ := rig.s.reloadPassHash(); hash != "" {
		t.Fatal("rejected initialization persisted a password")
	}

	code, body = rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{
		"password": "operator-password-123", "setup_token": setupToken,
	})
	rig.expect(http.StatusOK, code, body, "")
	token, _ := body["token"].(string)
	if token == "" {
		t.Fatal("initialization returned no JWT")
	}
	code, body = rig.do(http.MethodGet, "/api/ping", token, nil)
	rig.expect(http.StatusOK, code, body, "")
	code, body = rig.do(http.MethodGet, "/api/auth/status", "", nil)
	rig.expect(http.StatusOK, code, body, "")
	if body["initialized"] != true || body["setup_token_required"] != false {
		t.Fatalf("unexpected post-init status: %v", body)
	}
	code, body = rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{
		"password": "replacement-password-123", "setup_token": setupToken,
	})
	rig.expect(http.StatusForbidden, code, body, authErrPasswordAlreadySet)
}

func TestAuthInitConcurrentRequestsHaveOneWinnerDB(t *testing.T) {
	rig := newAuthTestRig(t)
	rig.s.bootstrapAuth()
	setupToken := rig.s.currentSetupToken()
	type response struct {
		code int
		body map[string]any
	}
	responses := make(chan response, 2)
	var wg sync.WaitGroup
	for _, password := range []string{"first-password-123", "second-password-123"} {
		password := password
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, body := rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{
				"password": password, "setup_token": setupToken,
			})
			responses <- response{code: code, body: body}
		}()
	}
	wg.Wait()
	close(responses)
	wins, rejects := 0, 0
	for result := range responses {
		switch result.code {
		case http.StatusOK:
			wins++
		case http.StatusForbidden:
			rejects++
		default:
			t.Fatalf("unexpected concurrent response: %d %v", result.code, result.body)
		}
	}
	if wins != 1 || rejects != 1 {
		t.Fatalf("wins=%d rejects=%d, want one each", wins, rejects)
	}
}

func TestAuthInitIsThrottledDB(t *testing.T) {
	rig := newAuthTestRig(t)
	rig.s.bootstrapAuth()
	for i := 0; i < authFreeFailures; i++ {
		code, body := rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{
			"password": "operator-password-123", "setup_token": "wrong-token",
		})
		rig.expect(http.StatusForbidden, code, body, authErrSetupTokenInvalid)
	}
	code, body := rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{
		"password": "operator-password-123", "setup_token": rig.s.currentSetupToken(),
	})
	rig.expect(http.StatusTooManyRequests, code, body, authErrTooManyAttempts)
	if hash, _ := rig.s.reloadPassHash(); hash != "" {
		t.Fatal("throttled initialization persisted a password")
	}
}
```

- [ ] **Step 3: Add integration tests for password-bound sessions, resets, bootstrap, and throttling**

Append the following tests to the same file:

```go
func TestPasswordChangeReturnsFreshTokenAndRevokesOldTokensDB(t *testing.T) {
	rig := newAuthTestRig(t)
	rig.s.bootstrapAuth()
	code, body := rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{
		"password": "operator-password-123", "setup_token": rig.s.currentSetupToken(),
	})
	rig.expect(http.StatusOK, code, body, "")
	firstToken := body["token"].(string)
	code, body = rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{
		"username": "ARTEX", "password": "operator-password-123",
	})
	rig.expect(http.StatusOK, code, body, "")
	secondToken := body["token"].(string)

	code, body = rig.do(http.MethodPost, "/api/auth/change-password", firstToken, map[string]any{
		"old_password": "operator-password-123", "new_password": "changed-password-123",
	})
	rig.expect(http.StatusOK, code, body, "")
	freshToken, _ := body["token"].(string)
	if body["ok"] != true || freshToken == "" {
		t.Fatalf("password change response lacks replacement token: %v", body)
	}
	for _, oldToken := range []string{firstToken, secondToken} {
		code, body = rig.do(http.MethodGet, "/api/ping", oldToken, nil)
		rig.expect(http.StatusUnauthorized, code, body, authErrTokenInvalid)
	}
	code, body = rig.do(http.MethodGet, "/api/ping", freshToken, nil)
	rig.expect(http.StatusOK, code, body, "")

	resetHash, err := bcrypt.GenerateFromPassword([]byte("reset-password-123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := rig.s.m.pg.SetSetting(authPassKey, string(resetHash)); err != nil {
		t.Fatal(err)
	}
	previousTTL := passHashCacheTTL
	passHashCacheTTL = 0
	t.Cleanup(func() { passHashCacheTTL = previousTTL })
	code, body = rig.do(http.MethodGet, "/api/ping", freshToken, nil)
	rig.expect(http.StatusUnauthorized, code, body, authErrTokenInvalid)
	code, body = rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{
		"username": "ARTEX", "password": "reset-password-123",
	})
	rig.expect(http.StatusOK, code, body, "")
}

func TestAuthLoginAndPasswordChangeShareLimiterDB(t *testing.T) {
	rig := newAuthTestRig(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("operator-password-123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := rig.s.m.pg.SetSetting(authPassKey, string(hash)); err != nil {
		t.Fatal(err)
	}
	rig.s.bootstrapAuth()
	for i := 0; i < authFreeFailures; i++ {
		code, body := rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{
			"username": "ARTEX", "password": "wrong-password-123",
		})
		rig.expect(http.StatusUnauthorized, code, body, authErrBadCredential)
	}
	code, body := rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{
		"username": "ARTEX", "password": "operator-password-123",
	})
	rig.expect(http.StatusTooManyRequests, code, body, authErrTooManyAttempts)
	peer := clientKey(httptest.NewRequest(http.MethodPost, "/api/auth/login", nil))
	rig.s.limiter().reset(peer)
	code, body = rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{
		"username": "ARTEX", "password": "operator-password-123",
	})
	rig.expect(http.StatusOK, code, body, "")
	token := body["token"].(string)
	for i := 0; i < authFreeFailures; i++ {
		code, body = rig.do(http.MethodPost, "/api/auth/change-password", token, map[string]any{
			"old_password": "wrong-password-123", "new_password": "next-password-123",
		})
		rig.expect(http.StatusUnauthorized, code, body, authErrCurrentPasswordWrong)
	}
	code, body = rig.do(http.MethodPost, "/api/auth/change-password", token, map[string]any{
		"old_password": "operator-password-123", "new_password": "next-password-123",
	})
	rig.expect(http.StatusTooManyRequests, code, body, authErrTooManyAttempts)
}

func TestAuthBootstrapEnvironmentPasswordDB(t *testing.T) {
	rig := newAuthTestRig(t)
	const environmentPassword = "environment-password-123"
	t.Setenv(adminPasswordEnv, environmentPassword)
	var logs bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldWriter) })
	rig.s.bootstrapAuth()
	if strings.Contains(logs.String(), environmentPassword) {
		t.Fatal("environment password leaked to logs")
	}
	code, body := rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{
		"username": "ARTEX", "password": environmentPassword,
	})
	rig.expect(http.StatusOK, code, body, "")

	t.Setenv(adminPasswordEnv, "different-password-123")
	rig.s.bootstrapAuth()
	code, body = rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{
		"username": "ARTEX", "password": environmentPassword,
	})
	rig.expect(http.StatusOK, code, body, "")
}

func TestInvalidBootstrapEnvironmentPasswordFallsBackToSetupTokenDB(t *testing.T) {
	for _, password := range []string{"short", strings.Repeat("x", maxPasswordBytes+1)} {
		t.Run(password[:5], func(t *testing.T) {
			rig := newAuthTestRig(t)
			t.Setenv(adminPasswordEnv, password)
			rig.s.bootstrapAuth()
			if hash, _ := rig.s.reloadPassHash(); hash != "" {
				t.Fatal("invalid environment password was persisted")
			}
			if rig.s.currentSetupToken() == "" {
				t.Fatal("invalid environment password did not fall back to setup-token bootstrap")
			}
		})
	}
}
```

- [ ] **Step 4: Run the database tests and confirm the API contract is still missing**

Run with a configured PostgreSQL instance:

```bash
go test ./server -run 'Test(AuthInit|PasswordChange|AuthLogin|AuthBootstrap|InvalidBootstrap)' -count=1 -v
```

Expected: setup-token, replacement-token, throttling, and password-bound assertions fail. If every test reports `SKIP`, configure PostgreSQL before continuing; a skipped integration suite is not acceptable evidence for this task.

- [ ] **Step 5: Define stable English authentication errors and failure recording**

Replace the single `errDataSourceUnavailable` constant in `server/auth.go` with:

```go
const (
	authErrUnauthorized          = "Unauthorized"
	authErrTokenInvalid          = "Token is invalid or expired"
	authErrPasswordAlreadySet    = "Password has already been set"
	authErrPasswordEmpty         = "Password must not be empty"
	authErrMissingSetupToken     = "Setup token must not be empty"
	authErrSetupTokenInvalid     = "Setup token is incorrect"
	authErrNewPasswordEmpty      = "New password must not be empty"
	authErrPasswordHash          = "Failed to hash password"
	authErrTokenGeneration       = "Failed to generate token"
	authErrInvalidRequest        = "Invalid request format"
	authErrPasswordNotInitialized = "Password is not initialized; set a password first"
	authErrCurrentPasswordWrong  = "Current password is incorrect"
	authErrBadCredential         = "Username or password is incorrect"
	authErrTooManyAttempts       = "Too many failed attempts; try again later"
	errDataSourceUnavailable     = "Data source temporarily unavailable; please try again later"
)

func (s *Server) recordAuthFailure(r *http.Request, operation string) {
	s.limiter().fail(clientKey(r))
	log.Printf("[auth] Failed %s from %s", operation, clientLogID(r))
}
```

- [ ] **Step 6: Route protected requests through the password-bound verifier**

Replace `requireAuth` with:

```go
func (s *Server) requireAuth(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasPrefix(path, "/api/auth/") || path == "/api/health" {
			h.ServeHTTP(w, r)
			return
		}
		token := extractToken(r)
		if token == "" {
			writeErr(w, http.StatusUnauthorized, authErrUnauthorized)
			return
		}
		if !s.verifyToken(token) {
			writeErr(w, http.StatusUnauthorized, authErrTokenInvalid)
			return
		}
		h.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 7: Replace status and initialization handlers**

Replace `authStatus` and `authInit` with:

```go
func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	hash, _, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, errDataSourceUnavailable)
		return
	}
	s.setPassHash(hash)
	if hash == "" {
		_ = s.currentSetupToken()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"initialized": hash != "", "setup_token_required": hash == "",
	})
}

func (s *Server) authInit(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	key := clientKey(r)
	if !s.limiter().allow(key) {
		writeErr(w, http.StatusTooManyRequests, authErrTooManyAttempts)
		return
	}
	var req struct {
		Password   string `json:"password"`
		SetupToken string `json:"setup_token"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, authErrInvalidRequest)
		return
	}
	if req.Password == "" {
		writeErr(w, http.StatusBadRequest, authErrPasswordEmpty)
		return
	}
	if strings.TrimSpace(req.SetupToken) == "" {
		writeErr(w, http.StatusBadRequest, authErrMissingSetupToken)
		return
	}

	s.initMu.Lock()
	defer s.initMu.Unlock()
	existing, _, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, errDataSourceUnavailable)
		return
	}
	if existing != "" {
		s.setPassHash(existing)
		writeErr(w, http.StatusForbidden, authErrPasswordAlreadySet)
		return
	}
	if !s.checkSetupToken(req.SetupToken) {
		s.recordAuthFailure(r, "password initialization")
		writeErr(w, http.StatusForbidden, authErrSetupTokenInvalid)
		return
	}
	if reason := validatePassword(req.Password); reason != "" {
		writeErr(w, http.StatusBadRequest, reason)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, authErrPasswordHash)
		return
	}
	inserted, err := pg.InsertSettingIfAbsent(authPassKey, string(hash))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Failed to save password")
		return
	}
	if !inserted {
		_, _ = s.reloadPassHash()
		writeErr(w, http.StatusForbidden, authErrPasswordAlreadySet)
		return
	}
	s.setPassHash(string(hash))
	s.clearSetupToken()
	s.limiter().reset(key)
	token, err := signJWT(s.signingKey(string(hash)))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, authErrTokenGeneration)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token})
}
```

- [ ] **Step 8: Replace login and password-change handlers**

Replace `authLogin` and `authChangePassword` with:

```go
func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	key := clientKey(r)
	if !s.limiter().allow(key) {
		writeErr(w, http.StatusTooManyRequests, authErrTooManyAttempts)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, authErrInvalidRequest)
		return
	}
	hash, ok, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, errDataSourceUnavailable)
		return
	}
	if !ok || hash == "" {
		writeErr(w, http.StatusForbidden, authErrPasswordNotInitialized)
		return
	}
	s.setPassHash(hash)
	passwordOK := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) == nil
	if req.Username != "ARTEX" || !passwordOK {
		s.recordAuthFailure(r, "login")
		writeErr(w, http.StatusUnauthorized, authErrBadCredential)
		return
	}
	s.limiter().reset(key)
	token, err := signJWT(s.signingKey(hash))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, authErrTokenGeneration)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token})
}

func (s *Server) authChangePassword(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	key := clientKey(r)
	if !s.limiter().allow(key) {
		writeErr(w, http.StatusTooManyRequests, authErrTooManyAttempts)
		return
	}
	if !s.verifyToken(extractToken(r)) {
		s.recordAuthFailure(r, "password change authorization")
		writeErr(w, http.StatusUnauthorized, authErrUnauthorized)
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, authErrInvalidRequest)
		return
	}
	if req.NewPassword == "" {
		writeErr(w, http.StatusBadRequest, authErrNewPasswordEmpty)
		return
	}
	if reason := validatePassword(req.NewPassword); reason != "" {
		writeErr(w, http.StatusBadRequest, reason)
		return
	}
	hash, ok, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, errDataSourceUnavailable)
		return
	}
	if !ok || hash == "" {
		writeErr(w, http.StatusForbidden, authErrPasswordNotInitialized)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.OldPassword)) != nil {
		s.recordAuthFailure(r, "password change")
		writeErr(w, http.StatusUnauthorized, authErrCurrentPasswordWrong)
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, authErrPasswordHash)
		return
	}
	if err := pg.SetSetting(authPassKey, string(newHash)); err != nil {
		writeErr(w, http.StatusInternalServerError, "Failed to save password")
		return
	}
	s.setPassHash(string(newHash))
	s.limiter().reset(key)
	token, err := signJWT(s.signingKey(string(newHash)))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, authErrTokenGeneration)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "token": token})
}
```

- [ ] **Step 9: Keep the fail-closed database regression aligned with the new request shape**

In `server/auth_test.go`, change the body in `TestAuthInitFailsClosedWhenDataSourceUnavailable` from password-only JSON to:

```go
	body := strings.NewReader(`{"password":"new-password","setup_token":"unavailable-db-token"}`)
```

Keep its expected status at 503. This proves a database outage wins over setup-token validation after syntactically valid input.

- [ ] **Step 10: Format and run unit plus PostgreSQL tests**

Run:

```bash
gofmt -w server/auth.go server/auth_test.go server/auth_init_db_test.go
go test ./server -run 'Test(AuthStatusFailsClosed|AuthInitFailsClosed|ValidatePassword|AuthInit|PasswordChange|AuthLogin|AuthBootstrap|InvalidBootstrap)' -count=1 -v
```

Expected: every unit test passes; every PostgreSQL test passes without `SKIP`; old tokens return 401 and the replacement token returns 200.

- [ ] **Step 11: Commit the authentication contracts**

```bash
git add server/auth.go server/auth_test.go server/auth_init_db_test.go
git commit -m "feat(auth): enforce secure initialization and session revocation"
```

### Task 4: Make browser and network boundaries local-only by default

**Files:**
- Create: `server/cors_test.go`
- Modify: `server/server.go`
- Modify: `server/auth.go`
- Modify: `server/auth_init_db_test.go`
- Modify: `cmd/artex/main.go`
- Modify: `cmd/artex/main_test.go`
- Modify: `dev.sh`

- [ ] **Step 1: Write exact-origin CORS tests**

Create `server/cors_test.go`:

```go
package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSAllowedOriginsDefaultAndEnvironment(t *testing.T) {
	t.Setenv(corsOriginsEnv, "")
	defaults, err := corsAllowedOrigins()
	if err != nil {
		t.Fatal(err)
	}
	for _, origin := range defaultCORSOrigins {
		if _, ok := defaults[origin]; !ok {
			t.Fatalf("default CORS origin %q is missing", origin)
		}
	}

	t.Setenv(corsOriginsEnv, " https://UI.example.com , http://10.0.0.2:5173 ")
	configured, err := corsAllowedOrigins()
	if err != nil {
		t.Fatal(err)
	}
	if len(configured) != 2 {
		t.Fatalf("configured origins = %v", configured)
	}
	if _, ok := configured["https://ui.example.com"]; !ok {
		t.Fatal("configured origin was not normalized")
	}
	if _, ok := configured["http://localhost:5173"]; ok {
		t.Fatal("environment origins did not replace defaults")
	}

	t.Setenv(corsOriginsEnv, "*")
	if _, err := corsAllowedOrigins(); err == nil {
		t.Fatal("wildcard CORS configuration was accepted")
	}
}

func TestCORSAllowsOnlyExactOrigins(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := corsWith(map[string]struct{}{"http://localhost:5173": {}}, next)
	request := func(method, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/auth/status", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := request(http.MethodGet, ""); rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("same-origin response = %d headers=%v", rec.Code, rec.Header())
	}
	allowed := request(http.MethodOptions, "http://LOCALHOST:5173")
	if allowed.Code != http.StatusNoContent {
		t.Fatalf("allowed preflight status = %d", allowed.Code)
	}
	if got := allowed.Header().Get("Access-Control-Allow-Origin"); got != "http://LOCALHOST:5173" {
		t.Fatalf("allowed origin echo = %q", got)
	}
	if got := allowed.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("Vary = %q", got)
	}
	if allowed.Header().Get("Access-Control-Allow-Headers") != "Content-Type, Authorization" {
		t.Fatalf("allow headers = %q", allowed.Header().Get("Access-Control-Allow-Headers"))
	}
	for _, method := range []string{http.MethodGet, http.MethodOptions} {
		blocked := request(method, "https://evil.example")
		if blocked.Code != http.StatusForbidden || blocked.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("blocked %s response = %d headers=%v", method, blocked.Code, blocked.Header())
		}
	}
}
```

- [ ] **Step 2: Run the CORS tests and confirm wildcard behavior fails**

Run:

```bash
go test ./server -run 'TestCORS' -count=1
```

Expected: build failure for `corsOriginsEnv`, `defaultCORSOrigins`, `corsAllowedOrigins`, and `corsWith`.

- [ ] **Step 3: Replace wildcard CORS with exact normalized origins**

Replace the current `cors` function at the bottom of `server/server.go` with:

```go
const corsOriginsEnv = "ARTEX_CORS_ORIGINS"

var defaultCORSOrigins = []string{
	"http://localhost:5173",
	"http://127.0.0.1:5173",
}

func normalizeOrigin(origin string) (string, error) {
	origin = strings.TrimSpace(origin)
	if origin == "" || origin == "*" {
		return "", fmt.Errorf("CORS origin must be an explicit URL")
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("invalid CORS origin %q", origin)
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host), nil
}

func corsAllowedOrigins() (map[string]struct{}, error) {
	configured := strings.TrimSpace(os.Getenv(corsOriginsEnv))
	values := defaultCORSOrigins
	if configured != "" {
		values = strings.Split(configured, ",")
	}
	allowed := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized, err := normalizeOrigin(value)
		if err != nil {
			return nil, err
		}
		allowed[normalized] = struct{}{}
	}
	return allowed, nil
}

func corsWith(allowed map[string]struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Vary", "Origin")
		normalized, err := normalizeOrigin(origin)
		_, ok := allowed[normalized]
		if err != nil || !ok {
			writeErr(w, http.StatusForbidden, "Origin is not allowed")
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func cors(next http.Handler) http.Handler {
	allowed, err := corsAllowedOrigins()
	if err != nil {
		log.Printf("[cors] Invalid %s; denying cross-origin requests: %v", corsOriginsEnv, err)
		allowed = map[string]struct{}{}
	}
	return corsWith(allowed, next)
}
```

`server/server.go` already imports `fmt`, `log`, `net/url`, `os`, and `strings`; do not add a dependency.

- [ ] **Step 4: Add exposure-warning behavior and its database test**

Add this method after `isLoopbackAddr` in `server/auth.go`:

```go
func (s *Server) WarnIfExposed(addr string) {
	if isLoopbackAddr(addr) {
		return
	}
	hash, err := s.cachedPassHash()
	if err != nil || hash != "" {
		return
	}
	log.Printf("[auth] WARNING: the administrator password is unset while ARTEX listens on %q; finish setup before exposing this port", addr)
}
```

Append this test to `server/auth_init_db_test.go`:

```go
func TestWarnIfExposedOnlyWhenUninitializedDB(t *testing.T) {
	rig := newAuthTestRig(t)
	rig.s.setPassHash("")
	var logs bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldWriter) })

	rig.s.WarnIfExposed("127.0.0.1:8787")
	if logs.Len() != 0 {
		t.Fatalf("loopback bind emitted warning: %s", logs.String())
	}
	rig.s.WarnIfExposed(":8787")
	if !strings.Contains(logs.String(), "WARNING") {
		t.Fatalf("public uninitialized bind emitted no warning: %s", logs.String())
	}
	logs.Reset()
	rig.s.setPassHash("already-initialized")
	rig.s.WarnIfExposed(":8787")
	if logs.Len() != 0 {
		t.Fatalf("initialized bind emitted warning: %s", logs.String())
	}
}
```

Reuse the test file's existing `log` import.

- [ ] **Step 5: Lock the native listen default with a command-package test**

Add this constant near the other package-level values in `cmd/artex/main.go`:

```go
const defaultHTTPAddr = "127.0.0.1:8787"
```

Change the flag declaration in `run` to:

```go
		addr = flag.String("addr", defaultHTTPAddr, "HTTP listen address")
```

Add this test to `cmd/artex/main_test.go`:

```go
func TestDefaultHTTPAddrIsLoopbackOnly(t *testing.T) {
	if defaultHTTPAddr != "127.0.0.1:8787" {
		t.Fatalf("default HTTP address = %q", defaultHTTPAddr)
	}
}
```

- [ ] **Step 6: Wire the warning and make development local-only**

In `cmd/artex/main.go`, add the warning immediately after `server.New`:

```go
	srv := server.New(ctx, mgr, skillDir, *dataDir, config.BaseDir())
	srv.WarnIfExposed(*addr)
```

In `dev.sh`, replace the backend command and status line with:

```bash
go run ./cmd/artex -addr 127.0.0.1:8787 -proxy 127.0.0.1:8788 &
```

```bash
echo "[dev] Backend 127.0.0.1:8787 / proxy 127.0.0.1:8788 / frontend http://localhost:5173  (Ctrl-C to exit)"
```

- [ ] **Step 7: Format and run boundary tests**

Run:

```bash
gofmt -w server/server.go server/auth.go server/cors_test.go server/auth_init_db_test.go cmd/artex/main.go cmd/artex/main_test.go
go test ./server -run 'Test(CORS|IsLoopback|WarnIfExposed)' -count=1 -v
go test ./cmd/artex -run 'Test(DefaultHTTPAddr|ShutdownContext)' -count=1
```

Expected: exact allowed origins receive their original `Origin` value and 204 preflight; wildcard and unlisted origins are rejected; the native default test passes. The warning test must pass without `SKIP`.

- [ ] **Step 8: Commit network-boundary hardening**

```bash
git add server/server.go server/auth.go server/cors_test.go server/auth_init_db_test.go cmd/artex/main.go cmd/artex/main_test.go dev.sh
git commit -m "fix(security): default HTTP and CORS to local access"
```

### Task 5: Update the frontend setup and password-change workflows

**Files:**
- Create: `web/src/components/auth-workflows.test.mjs`
- Modify: `web/src/lib/api.ts`
- Modify: `web/src/lib/auth.ts`
- Modify: `web/src/app/(auth)/setup/page.tsx`
- Modify: `web/src/app/(main)/_components/sidebar/change-password-dialog.tsx`

- [ ] **Step 1: Write frontend contract and storage tests**

Create `web/src/components/auth-workflows.test.mjs`:

```js
import ts from "typescript";

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

function apiMethod(name, post) {
  const path = new URL("../lib/api.ts", import.meta.url);
  const source = readFileSync(path, "utf8");
  const ast = ts.createSourceFile(path.pathname, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
  let initializer;
  for (const statement of ast.statements) {
    if (!ts.isVariableStatement(statement)) continue;
    for (const declaration of statement.declarationList.declarations) {
      if (declaration.name.getText(ast) !== "api" || !ts.isObjectLiteralExpression(declaration.initializer)) continue;
      for (const property of declaration.initializer.properties) {
        if (ts.isPropertyAssignment(property) && property.name.getText(ast) === name) initializer = property.initializer;
      }
    }
  }
  assert.ok(initializer, `api.${name} is missing`);
  const script = ts.transpileModule(`const method = ${initializer.getText(ast)}; method;`, {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
  }).outputText;
  return vm.runInNewContext(script, { post });
}

function loadAuth(storage) {
  const path = new URL("../lib/auth.ts", import.meta.url);
  const source = readFileSync(path, "utf8");
  const ast = ts.createSourceFile(path.pathname, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
  const declarations = ast.statements
    .filter((statement) =>
      ts.isVariableStatement(statement) &&
      statement.declarationList.declarations.some((item) => ["TOKEN_KEY", "COOKIE_MAX_AGE", "auth"].includes(item.name.getText(ast))),
    )
    .map((statement) => statement.getText(ast).replace(/^export\s+/, ""));
  const script = ts.transpileModule(`${declarations.join("\n")}\nauth;`, {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
  }).outputText;
  return vm.runInNewContext(script, {
    localStorage: storage,
    document: { cookie: "" },
    process: { env: {} },
    window: {},
  });
}

test("initialization sends the setup token and password", async () => {
  const calls = [];
  const initPassword = apiMethod("initPassword", async (...args) => calls.push(args));
  await initPassword("operator-password", "console-token");
  assert.deepEqual(calls, [["/auth/init", { password: "operator-password", setup_token: "console-token" }]]);
});

test("password change returns the replacement token contract", async () => {
  const replacement = { ok: true, token: "fresh-jwt" };
  const changePassword = apiMethod("changePassword", async (...args) => {
    assert.deepEqual(args, ["/auth/change-password", { old_password: "old", new_password: "new-password" }]);
    return replacement;
  });
  assert.deepEqual(await changePassword("old", "new-password"), replacement);
});

test("replacement token storage failure clears local authentication", () => {
  const successfulWrites = [];
  let removals = 0;
  let auth = loadAuth({
    setItem: (key, value) => successfulWrites.push([key, value]),
    removeItem: () => removals++,
    getItem: () => null,
  });
  assert.equal(auth.replaceToken("fresh-jwt"), true);
  assert.deepEqual(successfulWrites, [["artex_token", "fresh-jwt"]]);

  auth = loadAuth({
    setItem: () => {
      throw new Error("storage denied");
    },
    removeItem: () => removals++,
    getItem: () => null,
  });
  assert.equal(auth.replaceToken("fresh-jwt"), false);
  assert.equal(removals, 1);
});

test("setup remains fail-closed and asks for a setup token", () => {
  const source = readFileSync(new URL("../app/(auth)/setup/page.tsx", import.meta.url), "utf8");
  assert.match(source, /if \(checking\)[\s\S]*Checking setup status/);
  assert.match(source, /\.catch\(\(\) => \{[\s\S]*setUnavailable\(true\)/);
  assert.match(source, /\{unavailable \? \([\s\S]*\) : \(\s*<form/);
  assert.match(source, /id="setup-token"/);
  assert.match(source, /autoComplete="off"/);
  assert.match(source, /spellCheck=\{false\}/);
  assert.match(source, /initPassword\(password, setupToken\.trim\(\)\)/);
});

test("password dialog stores the replacement before closing or redirects to login", () => {
  const source = readFileSync(
    new URL("../app/(main)/_components/sidebar/change-password-dialog.tsx", import.meta.url),
    "utf8",
  );
  const replace = source.indexOf("auth.replaceToken(token)");
  const close = source.indexOf("onOpenChange(false)", replace);
  assert.ok(replace >= 0 && close > replace);
  assert.match(source, /window\.location\.assign\("\/login"\)/);
  assert.match(source, /other sessions are signed out/i);
});
```

- [ ] **Step 2: Run the frontend test and confirm the old contracts fail**

Run:

```bash
cd web && node --test src/components/auth-workflows.test.mjs
```

Expected: failures show that initialization omits `setup_token`, `auth.replaceToken` is absent, and the setup field/replacement-token workflow is absent.

- [ ] **Step 3: Update the TypeScript API contracts**

Replace the three auth entries in `web/src/lib/api.ts` with:

```ts
  authStatus: () => get<{ initialized: boolean; setup_token_required: boolean }>("/auth/status"),
  initPassword: (password: string, setupToken: string) =>
    post<{ token: string }>("/auth/init", { password, setup_token: setupToken }),
  changePassword: (oldPassword: string, newPassword: string) =>
    post<{ ok: boolean; token: string }>("/auth/change-password", {
      old_password: oldPassword,
      new_password: newPassword,
    }),
```

- [ ] **Step 4: Add one testable replacement-token operation to the existing auth store**

Add this method after `setToken` in `web/src/lib/auth.ts`:

```ts
  replaceToken(token: string): boolean {
    try {
      this.setToken(token);
      return true;
    } catch {
      try {
        this.clearToken();
      } catch {
        // Storage can be unavailable; the caller still redirects to login.
      }
      return false;
    }
  },
```

This is the only new frontend helper: it centralizes the required fail-closed storage behavior without adding a state-management dependency.

- [ ] **Step 5: Add the setup token to the setup page**

In `web/src/app/(auth)/setup/page.tsx`, add state beside the existing password state:

```tsx
  const [setupToken, setSetupToken] = React.useState("");
```

At the start of `handleSubmit`, before password validation, add:

```tsx
    if (!setupToken.trim()) {
      setError("Enter the setup token from the server console or deployment secret");
      return;
    }
```

Replace the API call with:

```tsx
      const result = await api.initPassword(password, setupToken.trim());
```

Insert this field as the first child of the available-state `<form>`, before the password field:

```tsx
              <div className="space-y-1.5">
                <Label htmlFor="setup-token">Setup token</Label>
                <Input
                  id="setup-token"
                  type="password"
                  value={setupToken}
                  onChange={(e) => setSetupToken(e.target.value)}
                  placeholder="Token from the server console"
                  autoFocus
                  autoComplete="off"
                  spellCheck={false}
                />
                <p className="text-xs text-muted-foreground">
                  Docker: run <code>docker compose logs artex</code>. Managed deployments use the configured setup token.
                </p>
              </div>
```

Remove `autoFocus` from the password input, and change the submit button's disabled expression to:

```tsx
disabled={loading || !setupToken.trim() || !password || !confirm}
```

Keep the existing `checking` early return and `unavailable ? ... : <form>` branch unchanged; they are the fail-closed status behavior covered by the new test.

- [ ] **Step 6: Store the password-change replacement token before closing**

Add this import to `web/src/app/(main)/_components/sidebar/change-password-dialog.tsx`:

```tsx
import { auth } from "@/lib/auth";
```

Replace the current `.then` callback with:

```tsx
      .then(({ token }) => {
        if (!auth.replaceToken(token)) {
          toast.error("Password changed, but the new session could not be stored. Sign in again.");
          window.location.assign("/login");
          return;
        }
        toast.success("Password changed; other sessions are signed out");
        onOpenChange(false);
      })
```

Replace the final sentence of `DialogDescription` with:

```tsx
              Changing the password signs out every other session. This browser stores a fresh sign-in token.
```

- [ ] **Step 7: Run frontend tests and the static build**

Run:

```bash
cd web
node --test src/components/auth-workflows.test.mjs
find src -name '*.test.mjs' -print0 | xargs -0 node --test
npm run build:static
```

Expected: the new file passes, all existing Node tests pass, and Next.js completes the static export without TypeScript or rendering errors.

- [ ] **Step 8: Commit the frontend workflow**

```bash
git add web/src/components/auth-workflows.test.mjs web/src/lib/api.ts web/src/lib/auth.ts 'web/src/app/(auth)/setup/page.tsx' 'web/src/app/(main)/_components/sidebar/change-password-dialog.tsx'
git commit -m "feat(web): require setup token and rotate browser session"
```

### Task 6: Harden Docker defaults and document operator workflows

**Files:**
- Modify: `docker-compose.yml`
- Modify: `.env.example`
- Modify: `install.sh`
- Modify: `README.md`

- [ ] **Step 1: Prove the current Compose mapping is public**

Run:

```bash
docker compose --env-file .env.example config --format json | node -e '
let text=""; process.stdin.on("data", c => text += c); process.stdin.on("end", () => {
  const cfg = JSON.parse(text);
  const port = cfg.services.artex.ports.find(p => Number(p.target) === 8787);
  if (port?.host_ip === "127.0.0.1") process.exit(0);
  console.error(port);
  process.exit(1);
});'
```

Expected: exit 1 and print the current 8787 mapping because it has no loopback `host_ip`.

- [ ] **Step 2: Pass bootstrap/CORS variables and bind the host port locally**

In the `artex.environment` map in `docker-compose.yml`, add:

```yaml
      ARTEX_SETUP_TOKEN: ${ARTEX_SETUP_TOKEN:-}
      ARTEX_ADMIN_PASSWORD: ${ARTEX_ADMIN_PASSWORD:-}
      ARTEX_CORS_ORIGINS: ${ARTEX_CORS_ORIGINS:-}
```

Replace the first published port with:

```yaml
      - "${ARTEX_BIND:-127.0.0.1}:8787:8787"
```

Leave the container's internal command unchanged: the image must continue listening on `:8787`, while Compose controls host exposure.

- [ ] **Step 3: Document secure defaults in the environment template**

Insert this block in `.env.example` after the PostgreSQL variables and before the LLM section:

```dotenv
# Web exposure: loopback-only by default. Set 0.0.0.0 only behind an HTTPS reverse proxy.
ARTEX_BIND=127.0.0.1

# First-run authentication. Leave both blank for a random token printed once in `docker compose logs artex`.
# ARTEX_SETUP_TOKEN is never logged. ARTEX_ADMIN_PASSWORD initializes only an empty installation;
# remove the plaintext password after initialization or inject it with your deployment secret manager.
ARTEX_SETUP_TOKEN=
ARTEX_ADMIN_PASSWORD=

# Exact browser origins, comma-separated. Blank keeps the local development defaults.
# Wildcard origins are rejected.
ARTEX_CORS_ORIGINS=
```

- [ ] **Step 4: Make the installer point to the generated setup token**

In `install.sh`, replace the two lines after `docker compose up -d` with:

```bash
  ok "Started → http://localhost:8787"
  info "First run: find the setup token with: docker compose logs artex"
  info "Follow logs: docker compose logs -f artex"
```

Do not write a setup token into `.env` automatically; interactive installs should use the server-generated 32-byte token, and headless operators can opt into the environment variables themselves.

- [ ] **Step 5: Replace first-run installation guidance in the README**

In `README.md`, replace the sentence beginning `After installation, open` with:

```markdown
After installation, get the one-time setup token from the server console (`docker compose logs artex` for Docker), then open **http://localhost:8787**. The first visit opens `/setup`, which requires that token before it accepts the administrator password. The HTTP service is loopback-only by default.
```

After the manual Docker Compose command block, add:

```markdown
On first start, run `docker compose logs artex` and copy the `[auth] Setup token` value into `/setup`. For unattended bootstrap, set either `ARTEX_SETUP_TOKEN` or `ARTEX_ADMIN_PASSWORD` in `.env` before the first start. `ARTEX_ADMIN_PASSWORD` is ignored after initialization; remove it from plaintext configuration afterward.

Docker publishes port 8787 on `127.0.0.1` by default. Set `ARTEX_BIND=0.0.0.0` only when an HTTPS reverse proxy or an equivalent trusted network boundary is ready.
```

- [ ] **Step 6: Document native binding, CORS, bootstrap, and the one-time logout**

In the Configuration section of `README.md`, replace the current Common arguments paragraph with:

```markdown
**Authentication bootstrap**: An empty installation prints a random setup token once with an `[auth]` prefix. Set `ARTEX_SETUP_TOKEN` to supply that token without logging it, or set `ARTEX_ADMIN_PASSWORD` to initialize the password headlessly. Neither variable overwrites an existing password.

**Browser origins**: Same-origin UI requests require no CORS configuration. `ARTEX_CORS_ORIGINS` replaces the local development allowlist with comma-separated exact origins; `*` is rejected.

**Common arguments**: `./start.sh -addr 127.0.0.1:8787 -proxy 127.0.0.1:8788` (`-addr` serves the frontend and API; `-proxy` is the traffic-recording proxy). The startup script passes arguments unchanged. Passing `-addr :8787` explicitly exposes the HTTP service on every interface.
```

In the opening paragraph under Reverse Proxy Deployment, replace `default :8787` with `default 127.0.0.1:8787`, and add this warning immediately before the Nginx example:

```markdown
> ARTEX does not terminate TLS. Complete administrator setup before changing the bind address, expose only the reverse proxy, and keep the application port private.
```

In the first paragraph under Updates and Upgrades, append:

```markdown

The first upgrade to the hardened authentication format rotates `jwt.key` once and signs out every existing browser. Passwords and application data are preserved. A later binary rollback also requires a fresh login; this prevents pre-upgrade tokens from becoming valid again.
```

- [ ] **Step 7: Validate Compose interpolation and documentation references**

Run:

```bash
docker compose --env-file .env.example config --format json | node -e '
let text=""; process.stdin.on("data", c => text += c); process.stdin.on("end", () => {
  const cfg = JSON.parse(text);
  const service = cfg.services.artex;
  const port = service.ports.find(p => Number(p.target) === 8787);
  if (port?.host_ip !== "127.0.0.1") throw new Error(JSON.stringify(port));
  for (const key of ["ARTEX_SETUP_TOKEN", "ARTEX_ADMIN_PASSWORD", "ARTEX_CORS_ORIGINS"])
    if (!(key in service.environment)) throw new Error(`${key} missing`);
});'
rg -n 'ARTEX_(BIND|SETUP_TOKEN|ADMIN_PASSWORD|CORS_ORIGINS)|docker compose logs artex|one-time setup token|signs out every existing browser' .env.example docker-compose.yml install.sh README.md
```

Expected: the Node assertion exits 0; the search finds every operator-facing variable and both token/logout notices.

- [ ] **Step 8: Commit deployment defaults and documentation**

```bash
git add docker-compose.yml .env.example install.sh README.md
git commit -m "docs(security): document local-only bootstrap defaults"
```

### Task 7: Run the complete acceptance suite

**Files:**
- Verify: all files changed in Tasks 1–6

- [ ] **Step 1: Verify formatting without rewriting files**

Run:

```bash
test -z "$(gofmt -l server/auth.go server/auth_key_test.go server/auth_setup_test.go server/auth_init_db_test.go server/auth_test.go server/cors_test.go server/server.go cmd/artex/main.go cmd/artex/main_test.go)"
```

Expected: exit 0 with no output.

- [ ] **Step 2: Run the full Go regression suite**

Run:

```bash
go test ./... -count=1
```

Expected: every Go package passes. Record any PostgreSQL-backed `SKIP`; Step 3 must run those tests against a real database even when the general suite skips them.

- [ ] **Step 3: Run authentication integration tests against a disposable PostgreSQL database**

Run this exact isolated test database sequence:

```bash
docker run --rm -d --name v-artex-auth-test-20261016 \
  -e POSTGRES_USER=artex -e POSTGRES_PASSWORD=artex_test_password -e POSTGRES_DB=artex_test \
  -p 127.0.0.1:55432:5432 postgres:16-alpine
for attempt in $(seq 1 30); do
  docker exec v-artex-auth-test-20261016 pg_isready -U artex -d artex_test && break
  sleep 1
done
ARTEX_PG_DSN='postgres://artex:artex_test_password@127.0.0.1:55432/artex_test?sslmode=disable' \
  go test ./server -run 'Test(AuthInit|PasswordChange|AuthLogin|AuthBootstrap|InvalidBootstrap|WarnIfExposed)' -count=1 -v
docker stop v-artex-auth-test-20261016
```

Expected: PostgreSQL reports ready, all named tests show `PASS` with no `SKIP`, and the test container stops and removes itself. If the test command fails, stop the named container before investigating so the fixed port is released.

- [ ] **Step 4: Run every frontend regression and static export**

Run:

```bash
cd web
find src -name '*.test.mjs' -print0 | xargs -0 node --test
npm run build:static
cd ..
```

Expected: all Node subtests pass and Next.js completes the static export.

- [ ] **Step 5: Recheck deployment output and English-only changed files**

Run:

```bash
docker compose --env-file .env.example config --format json | node -e '
let text=""; process.stdin.on("data", c => text += c); process.stdin.on("end", () => {
  const cfg = JSON.parse(text);
  const port = cfg.services.artex.ports.find(p => Number(p.target) === 8787);
  if (port?.host_ip !== "127.0.0.1") throw new Error(JSON.stringify(port));
});'
if rg -n -P '[\x{3400}-\x{9FFF}\x{AC00}-\x{D7AF}]' \
  server/auth.go server/auth_key_test.go server/auth_setup_test.go server/auth_init_db_test.go server/cors_test.go \
  cmd/artex/main.go cmd/artex/main_test.go web/src/components/auth-workflows.test.mjs \
  web/src/lib/api.ts web/src/lib/auth.ts 'web/src/app/(auth)/setup/page.tsx' \
  'web/src/app/(main)/_components/sidebar/change-password-dialog.tsx' docker-compose.yml .env.example install.sh README.md; then
  exit 1
fi
```

Expected: Compose remains loopback-only and the CJK/Hangul scan prints nothing for the changed authentication/deployment surface.

- [ ] **Step 6: Inspect the final diff for accidental scope growth or secrets**

Run:

```bash
git status --short
git diff --stat HEAD~6..HEAD
git diff --check HEAD~6..HEAD
git log --oneline -6
```

Expected: only the mapped files are changed, `git diff --check` prints nothing, no real password/setup token appears in the diff, and the six task commits are present. There is no CI/release workflow change in this implementation.

## Acceptance checklist

- [ ] Password-only or incorrect-token initialization cannot create an administrator password.
- [ ] The correct setup token initializes exactly once, including under concurrent requests.
- [ ] Existing passwords and application data survive startup and upgrade.
- [ ] The versioned root-key migration signs out pre-upgrade sessions and prevents rollback token revival.
- [ ] Password changes return one fresh token and revoke earlier tokens; out-of-process resets revoke within two seconds.
- [ ] Login, initialization, and password-change credential failures share the bounded TCP-peer limiter.
- [ ] Native, development, and Docker defaults bind port 8787 to loopback only.
- [ ] Only exact configured browser origins receive CORS access; wildcard configuration and disallowed preflight are rejected.
- [ ] Setup status failures remain fail-closed in both API and UI.
- [ ] Environment secrets are not logged or returned by an API.
- [ ] Go, PostgreSQL integration, Node, static export, Compose, formatting, and English-only checks all pass.
