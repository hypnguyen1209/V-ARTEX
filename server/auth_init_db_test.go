package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type authTestRig struct {
	t *testing.T
	s *Server
	h http.Handler
}

func newAuthTestRig(t *testing.T) *authTestRig {
	t.Helper()
	pg, _ := authSetupDatabase(t)
	t.Setenv(setupTokenEnv, "private-test-setup-token")
	t.Setenv(adminPasswordEnv, "")
	s := &Server{m: &Manager{pg: pg}, jwtKey: []byte(strings.Repeat("s", 32))}
	return authRigForServer(t, s)
}

func authRigForServer(t *testing.T, s *Server) *authTestRig {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/auth/status", s.authStatus)
	mux.HandleFunc("POST /api/auth/init", s.authInit)
	mux.HandleFunc("POST /api/auth/login", s.authLogin)
	mux.HandleFunc("POST /api/auth/change-password", s.authChangePassword)
	for _, path := range []string{"/api/ping", "/api/health"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	}
	return &authTestRig{t: t, s: s, h: s.requireAuth(mux)}
}

func (r *authTestRig) do(method, path, bearer string, body map[string]any) (int, map[string]any) {
	r.t.Helper()
	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			r.t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &payload)
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return r.request(req)
}

func (r *authTestRig) request(req *http.Request) (int, map[string]any) {
	r.t.Helper()
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	result := map[string]any{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			r.t.Fatal("authentication response was not JSON")
		}
	}
	return rec.Code, result
}

func (r *authTestRig) expect(wantCode, gotCode int, body map[string]any, wantError string) {
	r.t.Helper()
	// Do not include response bodies: successful responses contain session secrets.
	if gotCode != wantCode {
		r.t.Fatalf("status = %d, want %d", gotCode, wantCode)
	}
	if wantError != "" && body["error"] != wantError {
		r.t.Fatalf("unexpected authentication error; want %q", wantError)
	}
}

func (r *authTestRig) initialize() string {
	r.t.Helper()
	r.s.bootstrapAuth()
	code, body := r.do(http.MethodPost, "/api/auth/init", "", map[string]any{
		"password": "operator-password-123", "setup_token": r.s.currentSetupToken(),
	})
	r.expect(http.StatusOK, code, body, "")
	return r.responseToken(body)
}

func (r *authTestRig) responseToken(body map[string]any) string {
	r.t.Helper()
	bearer, _ := body["token"].(string)
	if bearer == "" {
		r.t.Fatal("authentication response lacks a session token")
	}
	return bearer
}

func (r *authTestRig) resetLimiter() {
	r.s.limiter().reset(clientKey(httptest.NewRequest(http.MethodPost, "/", nil)))
}

func TestAuthInitRequiresSetupTokenDB(t *testing.T) {
	rig := newAuthTestRig(t)
	code, body := rig.do(http.MethodGet, "/api/auth/status", "", nil)
	rig.expect(http.StatusOK, code, body, "")
	if len(body) != 2 || body["initialized"] != false || body["setup_token_required"] != true {
		t.Fatal("status must report only initialization and setup-token requirement")
	}
	if !rig.s.checkSetupToken("private-test-setup-token") {
		t.Fatal("status must ensure a setup token exists on a healthy empty database")
	}
	setupToken := rig.s.currentSetupToken()
	code, body = rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{"password": "operator-password-123"})
	rig.expect(http.StatusBadRequest, code, body, "Setup token must not be empty")
	code, body = rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{
		"password": "operator-password-123", "setup_token": "wrong",
	})
	rig.expect(http.StatusForbidden, code, body, "Setup token is incorrect")
	if hash, err := rig.s.reloadPassHash(); err != nil || hash != "" {
		t.Fatal("rejected initialization must not persist a password")
	}
	code, body = rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{
		"password": "operator-password-123", "setup_token": setupToken,
	})
	rig.expect(http.StatusOK, code, body, "")
	bearer := rig.responseToken(body)
	if rig.s.setupToken != "" {
		t.Fatal("successful initialization must clear setup token")
	}
	code, body = rig.do(http.MethodGet, "/api/ping", bearer, nil)
	rig.expect(http.StatusOK, code, body, "")
	code, body = rig.do(http.MethodGet, "/api/auth/status", "", nil)
	rig.expect(http.StatusOK, code, body, "")
	if len(body) != 2 || body["initialized"] != true || body["setup_token_required"] != false {
		t.Fatal("initialized status must contain only the two public booleans")
	}
	code, body = rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{
		"password": "replacement-password-123", "setup_token": setupToken,
	})
	rig.expect(http.StatusForbidden, code, body, "Password has already been set")
	limiter := rig.s.limiter()
	limiter.mu.Lock()
	attempts := limiter.clients[clientKey(httptest.NewRequest("POST", "/", nil))]
	limiter.mu.Unlock()
	if attempts != nil {
		t.Fatal("successful initialization must reset limiter")
	}
}

func TestAuthInitConcurrentRequestsHaveOneWinnerDB(t *testing.T) {
	for _, instances := range []int{1, 2} {
		t.Run(map[int]string{1: "one-instance", 2: "two-instances"}[instances], func(t *testing.T) {
			rig := newAuthTestRig(t)
			rigs := []*authTestRig{rig, rig}
			if instances == 2 {
				rigs[1] = authRigForServer(t, &Server{m: rig.s.m, jwtKey: rig.s.jwtKey})
			}
			for _, r := range rigs {
				r.s.bootstrapAuth()
			}
			type response struct {
				code  int
				body  map[string]any
				index int
			}
			responses := make(chan response, 2)
			var wg sync.WaitGroup
			for i, r := range rigs {
				wg.Add(1)
				go func(i int, r *authTestRig) {
					defer wg.Done()
					code, body := r.do(http.MethodPost, "/api/auth/init", "", map[string]any{
						"password": "operator-password-123", "setup_token": "private-test-setup-token",
					})
					responses <- response{code, body, i}
				}(i, r)
			}
			wg.Wait()
			close(responses)
			wins, rejects := 0, 0
			for result := range responses {
				switch result.code {
				case http.StatusOK:
					wins++
					if !rigs[result.index].s.verifyToken(rig.responseToken(result.body)) {
						t.Fatal("winning instance must issue a password-bound token")
					}
				case http.StatusForbidden:
					rejects++
					if hash, err := rigs[result.index].s.cachedPassHash(); err != nil || hash == "" {
						t.Fatal("losing instance must refresh its cached hash")
					}
				default:
					t.Fatalf("unexpected concurrent response: %d", result.code)
				}
			}
			if wins != 1 || rejects != 1 {
				t.Fatalf("wins=%d rejects=%d, want one each", wins, rejects)
			}
		})
	}
}

func TestAuthInitIsThrottledDB(t *testing.T) {
	rig := newAuthTestRig(t)
	rig.s.bootstrapAuth()
	for i := 0; i < 5; i++ {
		code, body := rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{
			"password": "operator-password-123", "setup_token": "wrong-token",
		})
		rig.expect(http.StatusForbidden, code, body, "Setup token is incorrect")
	}
	for _, path := range []string{"/api/auth/init", "/api/auth/login", "/api/auth/change-password"} {
		code, body := rig.do(http.MethodPost, path, "", nil)
		rig.expect(http.StatusTooManyRequests, code, body, "Too many failed attempts; try again later")
	}
	if hash, err := rig.s.reloadPassHash(); err != nil || hash != "" {
		t.Fatal("throttled initialization must not persist a password")
	}
}

func TestPasswordChangeReturnsFreshTokenAndRevokesOldTokensDB(t *testing.T) {
	rig := newAuthTestRig(t)
	first := rig.initialize()
	code, body := rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{"username": "ARTEX", "password": "operator-password-123"})
	rig.expect(http.StatusOK, code, body, "")
	second := rig.responseToken(body)
	code, body = rig.do(http.MethodPost, "/api/auth/change-password", first, map[string]any{
		"old_password": "operator-password-123", "new_password": "changed-password-123",
	})
	rig.expect(http.StatusOK, code, body, "")
	fresh := rig.responseToken(body)
	if body["ok"] != true {
		t.Fatal("password change must report success")
	}
	for _, old := range []string{first, second} {
		code, body = rig.do(http.MethodGet, "/api/ping", old, nil)
		rig.expect(http.StatusUnauthorized, code, body, "Token is invalid or expired")
	}
	code, body = rig.do(http.MethodGet, "/api/ping", fresh, nil)
	rig.expect(http.StatusOK, code, body, "")
	reset, err := bcrypt.GenerateFromPassword([]byte("reset-password-123"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := rig.s.m.pg.SetSetting(authPassKey, string(reset)); err != nil {
		t.Fatal(err)
	}
	rig.s.authMu.Lock()
	rig.s.passHashAt = time.Now().Add(-passHashCacheTTL)
	rig.s.authMu.Unlock()
	code, body = rig.do(http.MethodGet, "/api/ping", fresh, nil)
	rig.expect(http.StatusUnauthorized, code, body, "Token is invalid or expired")
	code, body = rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{"username": "ARTEX", "password": "reset-password-123"})
	rig.expect(http.StatusOK, code, body, "")
	code, body = rig.do(http.MethodGet, "/api/ping", rig.responseToken(body), nil)
	rig.expect(http.StatusOK, code, body, "")
}

func TestAuthLoginAndPasswordChangeShareLimiterDB(t *testing.T) {
	rig := newAuthTestRig(t)
	bearer := rig.initialize()
	for i := 0; i < 5; i++ {
		code, body := rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{
			"username": "unknown-user", "password": "operator-password-123",
		})
		rig.expect(http.StatusUnauthorized, code, body, "Username or password is incorrect")
	}
	code, body := rig.do(http.MethodPost, "/api/auth/change-password", bearer, nil)
	rig.expect(http.StatusTooManyRequests, code, body, "Too many failed attempts; try again later")
	rig.resetLimiter()
	for i := 0; i < 5; i++ {
		code, body = rig.do(http.MethodPost, "/api/auth/change-password", bearer, map[string]any{
			"old_password": "wrong-password-123", "new_password": "next-password-123",
		})
		rig.expect(http.StatusUnauthorized, code, body, "Current password is incorrect")
	}
	code, body = rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{"username": "ARTEX", "password": "operator-password-123"})
	rig.expect(http.StatusTooManyRequests, code, body, "Too many failed attempts; try again later")
	rig.resetLimiter()
	for i := 0; i < 5; i++ {
		code, body = rig.do(http.MethodPost, "/api/auth/change-password", "invalid", nil)
		rig.expect(http.StatusUnauthorized, code, body, "Unauthorized")
	}
	code, body = rig.do(http.MethodPost, "/api/auth/init", "", nil)
	rig.expect(http.StatusTooManyRequests, code, body, "Too many failed attempts; try again later")
}

func TestAuthBootstrapEnvironmentPasswordDB(t *testing.T) {
	rig := newAuthTestRig(t)
	logs := captureAuthLogs(t)
	const password = "environment-password-123"
	t.Setenv(adminPasswordEnv, password)
	rig.s.bootstrapAuth()
	code, body := rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{"username": "ARTEX", "password": password})
	rig.expect(http.StatusOK, code, body, "")
	t.Setenv(adminPasswordEnv, "replacement-password-123")
	rig.s.bootstrapAuth()
	code, body = rig.do(http.MethodPost, "/api/auth/login", "", map[string]any{"username": "ARTEX", "password": password})
	rig.expect(http.StatusOK, code, body, "")
	if strings.Contains(logs.String(), password) {
		t.Fatal("environment password leaked to logs")
	}
}

func TestInvalidBootstrapEnvironmentPasswordFallsBackToSetupTokenDB(t *testing.T) {
	for _, password := range []string{"short", strings.Repeat("x", maxPasswordBytes+1)} {
		t.Run(password[:5], func(t *testing.T) {
			rig := newAuthTestRig(t)
			t.Setenv(adminPasswordEnv, password)
			rig.s.bootstrapAuth()
			if hash, err := rig.s.reloadPassHash(); err != nil || hash != "" {
				t.Fatal("invalid environment password was persisted")
			}
			if !rig.s.checkSetupToken("private-test-setup-token") {
				t.Fatal("invalid environment password must fall back to setup token")
			}
		})
	}
}

func TestAuthMalformedRequestsAndPasswordValidationDB(t *testing.T) {
	rig := newAuthTestRig(t)
	rig.s.bootstrapAuth()
	for _, path := range []string{"/api/auth/init", "/api/auth/login"} {
		code, body := rig.request(httptest.NewRequest(http.MethodPost, path, strings.NewReader("{")))
		rig.expect(http.StatusBadRequest, code, body, "Invalid request format")
	}
	for _, password := range []string{"", "short", strings.Repeat("x", maxPasswordBytes+1)} {
		code, body := rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{"password": password, "setup_token": rig.s.currentSetupToken()})
		rig.expect(http.StatusBadRequest, code, body, "")
	}
	bearer := rig.initialize()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/change-password", strings.NewReader("{"))
	req.Header.Set("Authorization", "Bearer "+bearer)
	code, body := rig.request(req)
	rig.expect(http.StatusBadRequest, code, body, "Invalid request format")
	for _, password := range []string{"", "short", strings.Repeat("x", maxPasswordBytes+1)} {
		code, body = rig.do(http.MethodPost, "/api/auth/change-password", bearer, map[string]any{"old_password": "operator-password-123", "new_password": password})
		rig.expect(http.StatusBadRequest, code, body, "")
	}
}

func TestAuthDatabaseErrorsBlockAndAuthoritativeReadsRecoverDB(t *testing.T) {
	for _, path := range []string{"/api/auth/status", "/api/auth/init", "/api/auth/login", "/api/auth/change-password"} {
		t.Run(path, func(t *testing.T) {
			rig := newAuthTestRig(t)
			bearer := rig.initialize()
			pg := rig.s.m.pg
			if _, err := pg.Exec(`ALTER TABLE settings RENAME TO unavailable_settings`); err != nil {
				t.Fatal(err)
			}
			method := http.MethodPost
			if path == "/api/auth/status" {
				method = http.MethodGet
			}
			code, body := rig.do(method, path, bearer, map[string]any{
				"username": "unknown-user", "password": "operator-password-123", "setup_token": "private-test-setup-token",
				"old_password": "operator-password-123", "new_password": "next-password-123",
			})
			rig.expect(http.StatusServiceUnavailable, code, body, errDataSourceUnavailable)
			if rig.s.verifyToken(bearer) {
				t.Fatal("database read failure must block cached sessions")
			}
			if _, err := pg.Exec(`ALTER TABLE unavailable_settings RENAME TO settings`); err != nil {
				t.Fatal(err)
			}
			if rig.s.verifyToken(bearer) {
				t.Fatal("token verification must preserve sticky fail-closed state")
			}
			code, body = rig.do(http.MethodGet, "/api/auth/status", "", nil)
			rig.expect(http.StatusOK, code, body, "")
			if !rig.s.verifyToken(bearer) {
				t.Fatal("authoritative status read must recover authentication")
			}
		})
	}
}

func TestAuthAuthoritativeEmptyReadRecoversDB(t *testing.T) {
	for _, path := range []string{"/api/auth/init", "/api/auth/login", "/api/auth/change-password"} {
		t.Run(path, func(t *testing.T) {
			rig := newAuthTestRig(t)
			rig.s.bootstrapAuth()
			rig.s.blockAuth(errors.New("previous failure"))
			bearer := authTestToken(t, rig.s, "")
			code, body := rig.do(http.MethodPost, path, bearer, map[string]any{
				"password": "operator-password-123", "setup_token": "wrong-token", "new_password": "next-password-123",
			})
			if path == "/api/auth/init" {
				rig.expect(http.StatusForbidden, code, body, "Setup token is incorrect")
			} else {
				rig.expect(http.StatusForbidden, code, body, "Password is not initialized; set a password first")
			}
			if !rig.s.verifyToken(bearer) {
				t.Fatal("authoritative empty read must safely clear previous database failure")
			}
		})
	}
}

func TestAuthWriteErrorsDoNotInitializeOrExposeDatabaseDetailsDB(t *testing.T) {
	rig := newAuthTestRig(t)
	rig.s.bootstrapAuth()
	if _, err := rig.s.m.pg.Exec(`CREATE FUNCTION reject_auth_write() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'private-database-details'; END $$;
		CREATE TRIGGER reject_auth_write BEFORE INSERT OR UPDATE ON settings
		FOR EACH ROW EXECUTE FUNCTION reject_auth_write()`); err != nil {
		t.Fatal(err)
	}
	code, body := rig.do(http.MethodPost, "/api/auth/init", "", map[string]any{"password": "operator-password-123", "setup_token": rig.s.currentSetupToken()})
	rig.expect(http.StatusInternalServerError, code, body, "Failed to save password")
	if _, exists := body["token"]; exists {
		t.Fatal("failed write issued a token")
	}
	if rig.s.verifyToken(authTestToken(t, rig.s, "")) {
		t.Fatal("failed initialization write must block cached empty/raw auth")
	}
	if hash, ok, err := rig.s.m.pg.GetSetting(authPassKey); err != nil || ok || hash != "" {
		t.Fatal("failed write must not initialize")
	}
	if _, err := rig.s.m.pg.Exec(`DROP TRIGGER reject_auth_write ON settings`); err != nil {
		t.Fatal(err)
	}
	bearer := rig.initialize()
	if _, err := rig.s.m.pg.Exec(`CREATE TRIGGER reject_auth_write BEFORE INSERT OR UPDATE ON settings FOR EACH ROW EXECUTE FUNCTION reject_auth_write()`); err != nil {
		t.Fatal(err)
	}
	code, body = rig.do(http.MethodPost, "/api/auth/change-password", bearer, map[string]any{"old_password": "operator-password-123", "new_password": "next-password-123"})
	rig.expect(http.StatusInternalServerError, code, body, "Failed to save password")
	hash, _, err := rig.s.m.pg.GetSetting(authPassKey)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte("operator-password-123")) != nil {
		t.Fatal("failed change must preserve password")
	}
}

func TestAuthMiddlewareUsesPasswordBoundVerifier(t *testing.T) {
	s := &Server{jwtKey: []byte("root-signing-key")}
	s.setPassHash("password-hash")
	rig := authRigForServer(t, s)
	for _, tc := range []struct{ bearer, message string }{{"", "Unauthorized"}, {authTestToken(t, s, ""), "Token is invalid or expired"}} {
		code, body := rig.do(http.MethodGet, "/api/ping", tc.bearer, nil)
		rig.expect(http.StatusUnauthorized, code, body, tc.message)
	}
	code, body := rig.do(http.MethodGet, "/api/ping", authTestToken(t, s, "password-hash"), nil)
	rig.expect(http.StatusOK, code, body, "")
	code, body = rig.do(http.MethodGet, "/api/health", "", nil)
	rig.expect(http.StatusOK, code, body, "")
}

func TestAuthMissingDatabaseFailsClosed(t *testing.T) {
	for _, manager := range []*Manager{nil, {}} {
		s := &Server{m: manager, jwtKey: []byte("root-signing-key")}
		rig := authRigForServer(t, s)
		for _, path := range []string{"/api/auth/status", "/api/auth/init", "/api/auth/login", "/api/auth/change-password"} {
			method := http.MethodPost
			if path == "/api/auth/status" {
				method = http.MethodGet
			}
			code, body := rig.do(method, path, "", nil)
			rig.expect(http.StatusServiceUnavailable, code, body, errDataSourceUnavailable)
			if s.verifyToken(authTestToken(t, s, "")) {
				t.Fatal("missing database must block raw-root authentication")
			}
		}
	}
}

// Inspect control flow instead of asserting flaky bcrypt wall-clock timings.
func TestAuthLoginComparesPasswordBeforeUsername(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "auth.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "authLogin" {
			continue
		}
		compared, checked := false, false
		for _, stmt := range fn.Body.List {
			ast.Inspect(stmt, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "CompareHashAndPassword" {
						compared = true
					}
				}
				if expr, ok := n.(*ast.BinaryExpr); ok {
					if sel, ok := expr.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "Username" {
						checked = true
						if !compared {
							t.Error("username check must follow unconditional bcrypt comparison")
						}
					}
				}
				return true
			})
		}
		if !compared || !checked {
			t.Fatal("login must compare both password and username")
		}
		return
	}
	t.Fatal("authLogin was not found")
}
