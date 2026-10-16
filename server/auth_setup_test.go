package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/bcrypt"
)

func captureAuthLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	writer := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(writer) })
	return &logs
}

func TestSigningKeyBindsPasswordHash(t *testing.T) {
	s := &Server{jwtKey: []byte("root-signing-key")}
	if !bytes.Equal(s.signingKey(""), s.jwtKey) {
		t.Fatal("empty password hash must preserve raw key behavior")
	}
	key := s.signingKey("")
	key[0] ^= 1
	if bytes.Equal(key, s.jwtKey) {
		t.Fatal("signingKey must not expose the root key slice")
	}
	mac := hmac.New(sha256.New, s.jwtKey)
	mac.Write([]byte("bcrypt-password-hash"))
	if !bytes.Equal(s.signingKey("bcrypt-password-hash"), mac.Sum(nil)) {
		t.Fatal("password key must be HMAC-SHA256(root key, persisted hash)")
	}
	token, err := signJWT(s.signingKey("bcrypt-password-hash"))
	if err != nil {
		t.Fatal(err)
	}
	if !verifyJWT(token, s.signingKey("bcrypt-password-hash")) {
		t.Fatal("same password hash must verify")
	}
	if verifyJWT(token, s.signingKey("different-password-hash")) || verifyJWT(token, s.jwtKey) {
		t.Fatal("different hash and raw root key must reject derived token")
	}
}

func TestVerifyTokenUsesCurrentHashWithoutRawFallback(t *testing.T) {
	s := &Server{jwtKey: []byte("root-signing-key")}
	s.setPassHash("first-hash")
	token, err := signJWT(s.signingKey("first-hash"))
	if err != nil {
		t.Fatal(err)
	}
	if !s.verifyToken(token) {
		t.Fatal("current derived token must verify")
	}
	s.setPassHash("second-hash")
	if s.verifyToken(token) || s.verifyToken("") {
		t.Fatal("old and empty tokens must fail")
	}
	rawToken, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	if s.verifyToken(rawToken) {
		t.Fatal("DB-less server with in-memory hash must not fall back to root key")
	}
	if !(&Server{jwtKey: s.jwtKey}).verifyToken(rawToken) {
		t.Fatal("empty DB-less server must preserve raw token compatibility")
	}
}

func TestVerifyTokenDatabaseReadFailsClosed(t *testing.T) {
	pool, err := sql.Open("pgx", "postgres://invalid.invalid/artex")
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{pg: &db.DB{DB: pool}}, jwtKey: []byte("root-signing-key")}
	rawToken, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := s.cachedPassHash(); err == nil {
			t.Fatal("failed DB reads must not be cached as empty")
		}
	}
	if s.passHashLoaded || s.verifyToken(rawToken) {
		t.Fatal("read error must never authorize raw-key tokens")
	}
	s.setPassHash("stored-hash")
	s.authMu.Lock()
	s.passHashAt = time.Now().Add(-passHashCacheTTL - time.Second)
	s.authMu.Unlock()
	derived, err := signJWT(s.signingKey("stored-hash"))
	if err != nil {
		t.Fatal(err)
	}
	if s.verifyToken(derived) {
		t.Fatal("expired cache and unavailable DB must fail closed")
	}
	logs := captureAuthLogs(t)
	t.Setenv(adminPasswordEnv, "secret-admin-password")
	t.Setenv(setupTokenEnv, "secret-setup-token")
	s.bootstrapAuth()
	if s.setupToken != "" || strings.Contains(logs.String(), "secret-") {
		t.Fatal("bootstrap read failure must issue no setup token and log no secrets")
	}
}

func TestSetupTokenEnvironmentIsTrimmedAndNeverLogged(t *testing.T) {
	logs := captureAuthLogs(t)
	t.Setenv(setupTokenEnv, " \tprivate-setup-token\n")
	s := &Server{}
	if got := s.currentSetupToken(); got != "private-setup-token" {
		t.Fatalf("token = %q, want trimmed environment token", got)
	}
	t.Setenv(setupTokenEnv, "changed-environment-token")
	if s.currentSetupToken() != "private-setup-token" || !s.checkSetupToken(" \tprivate-setup-token\n") {
		t.Fatal("environment token must be retained and client input trimmed")
	}
	for _, wrong := range []string{"", "private-setup-tokem", "short", "changed-environment-token"} {
		if s.checkSetupToken(wrong) {
			t.Fatalf("wrong setup token %q accepted", wrong)
		}
	}
	if strings.Contains(logs.String(), "private-setup-token") || strings.Contains(logs.String(), "changed-environment-token") {
		t.Fatal("environment setup token must never be logged")
	}
	if strings.Count(logs.String(), setupTokenEnv) != 1 {
		t.Fatal("environment source must be logged exactly once")
	}
}

func TestRandomSetupTokenLengthLoggingAndClear(t *testing.T) {
	logs := captureAuthLogs(t)
	t.Setenv(setupTokenEnv, " \t ")
	s := &Server{}
	token := s.currentSetupToken()
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("setup token must encode exactly 32 random bytes: length=%d err=%v", len(decoded), err)
	}
	for i := 0; i < 3; i++ {
		if s.currentSetupToken() != token || !s.checkSetupToken(token) {
			t.Fatal("setup token must remain stable until cleared")
		}
	}
	if s.checkSetupToken(token[:len(token)-1]) || s.checkSetupToken(strings.Repeat("!", len(token))) || s.checkSetupToken("") {
		t.Fatal("wrong and short setup tokens must fail")
	}
	if strings.Count(logs.String(), token) != 1 || strings.Count(strings.TrimSpace(logs.String()), "\n") != 1 {
		t.Fatal("generated token must be logged once with one instruction line")
	}
	s.clearSetupToken()
	if s.setupToken != "" {
		t.Fatal("clear must erase in-memory token")
	}
	next := s.currentSetupToken()
	if next == "" || next == token || s.checkSetupToken(token) {
		t.Fatal("cleared token must be replaced and stop matching")
	}
}

func TestSetupTokenBootstrapWithoutDatabaseDoesNothing(t *testing.T) {
	t.Setenv(adminPasswordEnv, "valid-admin-password")
	t.Setenv(setupTokenEnv, "environment-setup-token")
	for _, s := range []*Server{{}, {m: &Manager{}}} {
		s.bootstrapAuth()
		if s.setupToken != "" || s.passHashLoaded {
			t.Fatal("bootstrap without PostgreSQL must not change auth state")
		}
	}
}

func TestAuthLimiterCooldownAndReset(t *testing.T) {
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	l := &authLimiter{now: func() time.Time { return now }}
	for i := 0; i < 5; i++ {
		if ok, wait := l.allow("peer"); !ok || wait != 0 {
			t.Fatalf("failure %d must be allowed, wait=%s", i+1, wait)
		}
		l.failure("peer")
	}
	if ok, wait := l.allow("peer"); ok || wait != 30*time.Second {
		t.Fatalf("fifth failure must start 30s cooldown: allowed=%v wait=%s", ok, wait)
	}
	now = now.Add(30 * time.Second)
	if ok, wait := l.allow("peer"); !ok || wait != 0 {
		t.Fatal("cooldown must expire at 30s")
	}
	l.failure("peer")
	if ok, wait := l.allow("peer"); ok || wait != time.Minute {
		t.Fatalf("next failure must double cooldown: allowed=%v wait=%s", ok, wait)
	}
	l.reset("peer")
	if ok, wait := l.allow("peer"); !ok || wait != 0 || len(l.clients) != 0 {
		t.Fatal("success must remove peer record")
	}
	s := &Server{}
	if s.limiter() == nil || s.limiter() != s.limiter() {
		t.Fatal("server must lazily retain one shared limiter")
	}
}

func TestAuthLimiterWindowStalenessAndCooldownCap(t *testing.T) {
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	l := &authLimiter{now: func() time.Time { return now }}
	for i := 0; i < 4; i++ {
		l.failure("peer")
	}
	now = now.Add(10*time.Minute + time.Nanosecond)
	if ok, _ := l.allow("peer"); !ok || len(l.clients) != 0 {
		t.Fatal("record stale for more than 10m must be removed on access")
	}
	for i := 0; i < 5; i++ {
		l.failure("peer")
	}
	for i := 0; i < 80; i++ {
		ok, wait := l.allow("peer")
		if ok || wait <= 0 || wait > 10*time.Minute {
			t.Fatalf("cooldown must remain positive and bounded, iteration %d: %s", i, wait)
		}
		if i >= 5 && wait != 10*time.Minute {
			t.Fatalf("cooldown must cap at 10m: %s", wait)
		}
		now = now.Add(wait)
		l.failure("peer")
	}
}

func TestAuthLimiterIgnoresFailuresDuringCooldown(t *testing.T) {
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	l := &authLimiter{now: func() time.Time { return now }}
	for i := 0; i < 5; i++ {
		l.failure("peer")
	}
	now = now.Add(time.Second)
	l.failure("peer")
	if ok, wait := l.allow("peer"); ok || wait != 29*time.Second {
		t.Fatalf("concurrent failure during cooldown must not escalate or extend it: allowed=%v wait=%s", ok, wait)
	}
}

func TestAuthLimiterFailureWindowExpiresDespiteAccess(t *testing.T) {
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	l := &authLimiter{now: func() time.Time { return now }}
	for i := 0; i < 4; i++ {
		l.failure("peer")
	}
	now = now.Add(5 * time.Minute)
	l.allow("peer")
	now = now.Add(5*time.Minute + time.Nanosecond)
	l.failure("peer")
	if ok, wait := l.allow("peer"); !ok || wait != 0 {
		t.Fatal("failures outside the 10m window must not trigger cooldown")
	}
}

func TestAuthLimiterBoundedOldestEviction(t *testing.T) {
	now := time.Time{}
	l := &authLimiter{now: func() time.Time { return now }}
	l.failure("oldest")
	for i := 1; i < 10000; i++ {
		now = now.Add(time.Millisecond)
		l.failure(fmt.Sprintf("peer-%d", i))
	}
	if len(l.clients) != 10000 {
		t.Fatalf("record count = %d, want 10000", len(l.clients))
	}
	now = now.Add(time.Millisecond)
	l.failure("new-peer")
	if len(l.clients) != 10000 || l.clients["oldest"] != nil || l.clients["peer-9999"] == nil || l.clients["peer-1"] == nil || l.clients["new-peer"] == nil {
		t.Fatal("at capacity, evict only the oldest record and preserve newer peers")
	}
}

func TestClientKeyIgnoresForwardedPeerClaims(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/auth/login", nil)
	r.RemoteAddr = "192.0.2.25:12345"
	r.Header.Set("X-Forwarded-For", "127.0.0.1")
	if key := clientKey(r); key != "192.0.2.25" {
		t.Fatalf("client key = %q, want TCP peer", key)
	}
	if id := clientLogID(r); !strings.Contains(id, r.RemoteAddr) || !strings.Contains(id, "127.0.0.1") || !strings.Contains(id, "untrusted") {
		t.Fatalf("audit identity must label forwarded claim untrusted: %q", id)
	}
	r.RemoteAddr = "[2001:db8::1]:8787"
	if key := clientKey(r); key != "2001:db8::1" {
		t.Fatalf("IPv6 peer key = %q", key)
	}
	r.RemoteAddr = "malformed-peer"
	r.Header.Del("X-Forwarded-For")
	if clientKey(r) != r.RemoteAddr || clientLogID(r) != r.RemoteAddr {
		t.Fatal("malformed peer must retain original RemoteAddr")
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"127.0.0.1", true}, {"127.0.0.1:8787", true}, {"127.12.34.56:80", true},
		{"::1", true}, {"[::1]:8787", true}, {"[::1]", true},
		{"localhost", true}, {"LOCALHOST:8787", true},
		{":8787", false}, {"0.0.0.0", false}, {"0.0.0.0:8787", false},
		{"::", false}, {"[::]:8787", false}, {"192.0.2.25:8787", false},
		{"2001:db8::1", false}, {"example.com:8787", false}, {"", false},
	} {
		t.Run(tc.addr, func(t *testing.T) {
			if got := isLoopbackAddr(tc.addr); got != tc.want {
				t.Fatalf("isLoopbackAddr(%q) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
}

// Each test owns its schema; never alter the configured database's real auth setting.
func authSetupDatabase(t *testing.T) (*db.DB, *sql.DB) {
	t.Helper()
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skipf("PostgreSQL DSN absent: %v", err)
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { admin.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		t.Fatalf("configured PostgreSQL must be available: %v", err)
	}
	random, err := randomBytes(8)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("auth_test_%x", random)
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Errorf("remove test schema: %v", err)
		}
	})
	cfg.RuntimeParams["search_path"] = schema
	pool, other := stdlib.OpenDB(*cfg), stdlib.OpenDB(*cfg)
	t.Cleanup(func() { pool.Close(); other.Close() })
	if _, err := pool.Exec(`CREATE TABLE settings (
		key text PRIMARY KEY, value text NOT NULL, updated_at timestamptz DEFAULT now()
	)`); err != nil {
		t.Fatal(err)
	}
	return &db.DB{DB: pool}, other
}

func authTestToken(t *testing.T, s *Server, hash string) string {
	t.Helper()
	token, err := signJWT(s.signingKey(hash))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestVerifyTokenDatabaseCacheAndExternalReset(t *testing.T) {
	pg, other := authSetupDatabase(t)
	if passHashCacheTTL != 2*time.Second {
		t.Fatal("password cache TTL must default to two seconds")
	}
	if err := pg.SetSetting(authPassKey, "first-hash"); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{pg: pg}, jwtKey: []byte("root-signing-key")}
	first := authTestToken(t, s, "first-hash")
	if !s.verifyToken(first) {
		t.Fatal("database-backed current token must verify")
	}
	if _, err := other.Exec(`UPDATE settings SET value=$1 WHERE key=$2`, "second-hash", authPassKey); err != nil {
		t.Fatal(err)
	}
	if hash, err := s.cachedPassHash(); err != nil || hash != "first-hash" || !s.verifyToken(first) {
		t.Fatalf("cache must reuse its successful read before expiry: hash=%q err=%v", hash, err)
	}
	s.authMu.Lock()
	s.passHashAt = time.Now().Add(-passHashCacheTTL)
	s.authMu.Unlock()
	if s.verifyToken(first) {
		t.Fatal("external password reset must revoke old token at cache expiry")
	}
	if !s.verifyToken(authTestToken(t, s, "second-hash")) {
		t.Fatal("refreshed hash must authorize its current token")
	}
	if _, err := other.Exec(`UPDATE settings SET value=$1 WHERE key=$2`, "third-hash", authPassKey); err != nil {
		t.Fatal(err)
	}
	if !s.verifyToken(authTestToken(t, s, "third-hash")) {
		t.Fatal("failed cached verification must reload once and retry a changed hash")
	}
}

func TestVerifyTokenAuthoritativeEmptyAndReloadFailure(t *testing.T) {
	pg, other := authSetupDatabase(t)
	s := &Server{m: &Manager{pg: pg}, jwtKey: []byte("root-signing-key")}
	raw := authTestToken(t, s, "")
	if !s.verifyToken(raw) {
		t.Fatal("successful authoritative empty read must preserve raw-key behavior")
	}
	if _, err := other.Exec(`INSERT INTO settings(key,value) VALUES ($1,$2)`, authPassKey, "externally-initialized-hash"); err != nil {
		t.Fatal(err)
	}
	if err := pg.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.reloadPassHash(); err == nil {
		t.Fatal("closed pool must make reload fail")
	}
	if s.verifyToken(raw) || s.passHashLoaded {
		t.Fatal("failed reload after another process initializes must invalidate cached empty/raw auth")
	}
	s.m.pg = &db.DB{DB: other}
	if s.verifyToken(raw) {
		t.Fatal("verification must not silently clear an authentication failure")
	}
	if _, err := s.reloadPassHash(); err != nil {
		t.Fatal(err)
	}
	if !s.verifyToken(authTestToken(t, s, "externally-initialized-hash")) {
		t.Fatal("explicit successful reload must restore authentication")
	}
}

func TestSetupTokenHeadlessBootstrap(t *testing.T) {
	pg, _ := authSetupDatabase(t)
	logs := captureAuthLogs(t)
	password := "headless-private-password"
	t.Setenv(adminPasswordEnv, password)
	t.Setenv(setupTokenEnv, "private-setup-token")
	s := &Server{m: &Manager{pg: pg}, jwtKey: []byte("root-signing-key")}
	s.bootstrapAuth()
	hash, ok, err := pg.GetSetting(authPassKey)
	if err != nil || !ok || bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		t.Fatalf("headless bootstrap must persist the bcrypt password: ok=%v err=%v", ok, err)
	}
	if s.setupToken != "" || !s.verifyToken(authTestToken(t, s, hash)) {
		t.Fatal("headless success must cache hash and issue no setup token")
	}
	t.Setenv(adminPasswordEnv, "replacement-private-password")
	s.bootstrapAuth()
	stored, _, err := pg.GetSetting(authPassKey)
	if err != nil || stored != hash || s.setupToken != "" {
		t.Fatal("existing password must ignore bootstrap environment and never be overwritten")
	}
	if strings.Contains(logs.String(), password) || strings.Contains(logs.String(), "private-setup-token") || !strings.Contains(logs.String(), adminPasswordEnv) || !strings.Contains(logs.String(), "ARTEX") {
		t.Fatal("headless log must identify env/user without secrets")
	}
}

func TestSetupTokenInvalidHeadlessPasswordFallsBack(t *testing.T) {
	pg, _ := authSetupDatabase(t)
	logs := captureAuthLogs(t)
	t.Setenv(adminPasswordEnv, "short")
	t.Setenv(setupTokenEnv, "private-interactive-token")
	s := &Server{m: &Manager{pg: pg}}
	s.bootstrapAuth()
	if !s.checkSetupToken("private-interactive-token") {
		t.Fatal("invalid headless password must fall back to interactive setup")
	}
	if _, ok, err := pg.GetSetting(authPassKey); err != nil || ok {
		t.Fatal("invalid password must not be persisted")
	}
	if !strings.Contains(logs.String(), adminPasswordEnv) || !strings.Contains(logs.String(), validatePassword("short")) || strings.Contains(logs.String(), "short") || strings.Contains(logs.String(), "private-interactive-token") {
		t.Fatal("invalid-password log must include reason and env name without secrets")
	}
}

func TestSetupTokenBootstrapHashFailureBlocksAuthentication(t *testing.T) {
	pg, _ := authSetupDatabase(t)
	logs := captureAuthLogs(t)
	t.Setenv(adminPasswordEnv, "private-headless-password")
	t.Setenv(setupTokenEnv, "private-interactive-token")
	s := &Server{m: &Manager{pg: pg}, jwtKey: []byte("root-signing-key")}
	reader := rand.Reader
	rand.Reader = iotest.ErrReader(errors.New("forced bcrypt entropy failure"))
	t.Cleanup(func() { rand.Reader = reader })
	s.bootstrapAuth()
	rand.Reader = reader
	if s.setupToken != "" || s.passHashLoaded || s.verifyToken(authTestToken(t, s, "")) {
		t.Fatal("bcrypt failure must invalidate/block auth and issue no setup token")
	}
	if !strings.Contains(logs.String(), "forced bcrypt entropy failure") || strings.Contains(logs.String(), "private-") {
		t.Fatal("hash failure must be logged without secrets")
	}
}

func TestSetupTokenBootstrapWriteFailureBlocksAuthentication(t *testing.T) {
	pg, _ := authSetupDatabase(t)
	logs := captureAuthLogs(t)
	if _, err := pg.Exec(`CREATE FUNCTION reject_auth_insert() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'forced auth write failure'; END $$;
		CREATE TRIGGER reject_auth_insert BEFORE INSERT ON settings
		FOR EACH ROW EXECUTE FUNCTION reject_auth_insert()`); err != nil {
		t.Fatal(err)
	}
	t.Setenv(adminPasswordEnv, "private-headless-password")
	t.Setenv(setupTokenEnv, "private-interactive-token")
	s := &Server{m: &Manager{pg: pg}, jwtKey: []byte("root-signing-key")}
	s.bootstrapAuth()
	if s.setupToken != "" || s.passHashLoaded || s.verifyToken(authTestToken(t, s, "")) {
		t.Fatal("write failure must invalidate/block cached empty auth and issue no setup token")
	}
	if !strings.Contains(logs.String(), "forced auth write failure") || strings.Contains(logs.String(), "private-") {
		t.Fatal("write failure must be logged without secrets")
	}
}

func TestSetupTokenBootstrapReadFailureInvalidatesEmptyCache(t *testing.T) {
	pg, _ := authSetupDatabase(t)
	captureAuthLogs(t)
	s := &Server{m: &Manager{pg: pg}, jwtKey: []byte("root-signing-key")}
	s.setPassHash("")
	if err := pg.Close(); err != nil {
		t.Fatal(err)
	}
	s.bootstrapAuth()
	if s.setupToken != "" || s.passHashLoaded || s.verifyToken(authTestToken(t, s, "")) {
		t.Fatal("bootstrap read failure must invalidate pre-existing empty cache and block raw-root auth")
	}
}

func TestSetupTokenBootstrapCompetingInsertReloadFailure(t *testing.T) {
	pg, other := authSetupDatabase(t)
	logs := captureAuthLogs(t)
	t.Setenv(adminPasswordEnv, "private-headless-password")
	t.Setenv(setupTokenEnv, "private-interactive-token")
	if err := pg.SetSetting(authPassKey, ""); err != nil {
		t.Fatal(err)
	}
	pg.SetMaxOpenConns(1)
	var backend int
	if err := pg.QueryRow(`SELECT pg_backend_pid()`).Scan(&backend); err != nil {
		t.Fatal(err)
	}
	lockKey := time.Now().UnixNano()
	if _, err := pg.Exec(fmt.Sprintf(`CREATE FUNCTION wait_auth_insert() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_advisory_xact_lock(%d); RETURN NEW; END $$;
		CREATE TRIGGER wait_auth_insert BEFORE INSERT ON settings
		FOR EACH ROW EXECUTE FUNCTION wait_auth_insert()`, lockKey)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	blocker, err := other.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		blocker.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, lockKey)
		blocker.Close()
	})
	if _, err := blocker.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{pg: pg}, jwtKey: []byte("root-signing-key")}
	done := make(chan struct{})
	go func() { s.bootstrapAuth(); close(done) }()
	for {
		var waiting bool
		if err := blocker.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND NOT granted
		)`, backend).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := blocker.ExecContext(ctx, `UPDATE settings SET value=$1 WHERE key=$2`, "competing-process-hash", authPassKey); err != nil {
		t.Fatal(err)
	}
	if err := pg.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, lockKey); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("bootstrap did not finish after releasing competing insert")
	}
	if !strings.Contains(logs.String(), "Reload initialized password hash") {
		t.Fatalf("must exercise failed reload after losing insert: %s", logs.String())
	}
	if s.setupToken != "" || s.passHashLoaded || s.verifyToken(authTestToken(t, s, "")) {
		t.Fatal("failed competing-insert reload must block cached empty/raw auth without issuing setup token")
	}
	s.m.pg = &db.DB{DB: other}
	if s.verifyToken(authTestToken(t, s, "competing-process-hash")) {
		t.Fatal("verification must not clear bootstrap failure itself")
	}
	if hash, err := s.reloadPassHash(); err != nil || hash != "competing-process-hash" {
		t.Fatalf("explicit reload must recover competing hash without overwriting it: hash=%q err=%v", hash, err)
	}
	if !s.verifyToken(authTestToken(t, s, "competing-process-hash")) {
		t.Fatal("successful authoritative reload must restore verification")
	}
}

func TestAuthLimiterTiedTimestampEviction(t *testing.T) {
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	l := &authLimiter{now: func() time.Time { return now }}
	for i := 0; i < 10000; i++ {
		l.failure(fmt.Sprintf("peer-%05d", i))
	}
	l.failure("new-peer")
	if len(l.clients) != 10000 || l.clients["peer-00000"] != nil || l.clients["peer-00001"] == nil || l.clients["peer-09999"] == nil {
		t.Fatal("equal timestamps must deterministically evict the lexicographically first client")
	}
}

func TestAuthLimiterConcurrentFailuresAndProbes(t *testing.T) {
	now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	s := &Server{}
	l := s.limiter()
	l.now = func() time.Time { return now }
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.limiter() != l {
				t.Error("concurrent callers must share one limiter")
			}
			l.failure("peer")
		}()
	}
	wg.Wait()
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l.failure("peer")
			if ok, wait := l.allow("peer"); ok || wait != 30*time.Second {
				t.Errorf("active concurrent cooldown must remain 30s: allowed=%v wait=%s", ok, wait)
			}
			key := fmt.Sprintf("independent-%d", i)
			l.failure(key)
			l.allow(key)
			l.reset(key)
		}(i)
	}
	wg.Wait()
	if len(l.clients) != 1 || l.clients["peer"] == nil {
		t.Fatal("concurrent reset must preserve unrelated peer records")
	}
}
