package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"fmt"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
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
