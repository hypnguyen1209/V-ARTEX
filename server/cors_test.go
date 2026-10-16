package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCORSPreservesSameOriginBrowserRequests(t *testing.T) {
	for _, tc := range []struct {
		name, target, origin string
		allowed              bool
	}{
		{"local-http", "http://127.0.0.1:8787/api/auth/login", "http://127.0.0.1:8787", true},
		{"local-hostname", "http://localhost:8787/api/auth/login", "http://LOCALHOST:8787", true},
		{"direct-tls", "https://artex.example.com/api/auth/login", "https://ARTEX.example.com", true},
		{"different-port", "http://127.0.0.1:8787/api/auth/login", "http://127.0.0.1:5173", false},
		{"different-scheme", "http://127.0.0.1:8787/api/auth/login", "https://127.0.0.1:8787", false},
		{"different-host", "http://127.0.0.1:8787/api/auth/login", "http://localhost:8787", false},
		{"host-suffix", "http://localhost:8787/api/auth/login", "http://localhost.evil.example:8787", false},
		{"untrusted-forwarded-host", "http://127.0.0.1:8787/api/auth/login", "https://evil.example", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, method := range []string{http.MethodPost, http.MethodOptions} {
				called := false
				h := corsWith(nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					called = true
					w.WriteHeader(http.StatusOK)
				}))
				req := httptest.NewRequest(method, tc.target, nil)
				req.Header.Set("Origin", tc.origin)
				req.Header.Set("Forwarded", "host=evil.example;proto=https")
				req.Header.Set("X-Forwarded-Host", "evil.example")
				req.Header.Set("X-Forwarded-Proto", "https")
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				want := http.StatusForbidden
				wantOrigin := ""
				if tc.allowed {
					wantOrigin = tc.origin
					want = http.StatusOK
					if method == http.MethodOptions {
						want = http.StatusNoContent
					}
				}
				if rec.Code != want || rec.Header().Get("Access-Control-Allow-Origin") != wantOrigin || called != (tc.allowed && method == http.MethodPost) {
					t.Fatalf("%s response = %d headers=%v downstream=%v", method, rec.Code, rec.Header(), called)
				}
			}
		})
	}
}

func TestCORSAllowedOriginsDefaultAndEnvironment(t *testing.T) {
	t.Setenv(corsOriginsEnv, "")
	defaults, err := corsAllowedOrigins()
	if err != nil {
		t.Fatal(err)
	}
	if len(defaults) != 2 {
		t.Fatalf("default origins = %v, want exactly two", defaults)
	}
	for _, origin := range []string{"http://localhost:5173", "http://127.0.0.1:5173"} {
		if _, ok := defaults[origin]; !ok {
			t.Fatalf("default origin %q missing", origin)
		}
	}
	t.Setenv(corsOriginsEnv, " HTTPS://UI.example.com , http://10.0.0.2:5173 ")
	configured, err := corsAllowedOrigins()
	if err != nil {
		t.Fatal(err)
	}
	if len(configured) != 2 {
		t.Fatalf("configured origins = %v", configured)
	}
	for _, origin := range []string{"https://ui.example.com", "http://10.0.0.2:5173"} {
		if _, ok := configured[origin]; !ok {
			t.Fatalf("normalized origin %q missing", origin)
		}
	}
	if _, ok := configured["http://localhost:5173"]; ok {
		t.Fatal("environment must replace defaults")
	}
}

func TestCORSInvalidConfigurationDeniesAllWithoutSecrets(t *testing.T) {
	for _, configured := range []string{
		"*", "https://*.example.com", "localhost:5173", "//localhost:5173",
		"https://operator:private-cors-password@example.com", "https://example.com/",
		"https://example.com/path", "https://example.com?query=private-cors-secret",
		"https://example.com?", "https://example.com#fragment", "https://example.com#",
		"http://localhost:5173,", ",http://localhost:5173", "http://localhost:5173, ,https://example.com",
	} {
		t.Run(configured, func(t *testing.T) {
			t.Setenv(corsOriginsEnv, configured)
			if _, err := corsAllowedOrigins(); err == nil {
				t.Fatal("malformed configuration accepted")
			}
			logs := captureAuthLogs(t)
			h := cors(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
			if !strings.Contains(logs.String(), corsOriginsEnv) || strings.Contains(logs.String(), "private-cors-") {
				t.Fatal("invalid configuration must log its name without its values")
			}
			for _, method := range []string{http.MethodGet, http.MethodOptions} {
				req := httptest.NewRequest(method, "/api/auth/status", nil)
				req.Header.Set("Origin", "http://localhost:5173")
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code != http.StatusForbidden || rec.Header().Get("Access-Control-Allow-Origin") != "" {
					t.Fatalf("invalid configuration response = %d headers=%v", rec.Code, rec.Header())
				}
			}
		})
	}
}

func TestCORSAllowsOnlyExactOrigins(t *testing.T) {
	called := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	})
	h := corsWith(map[string]struct{}{"http://localhost:5173": {}, "https://ui.example.com": {}}, next)
	for _, method := range []string{http.MethodGet, http.MethodOptions} {
		for _, origin := range []string{"http://LOCALHOST:5173", "HTTPS://UI.EXAMPLE.COM"} {
			req := httptest.NewRequest(method, "/api/auth/status", nil)
			req.Header.Set("Origin", origin)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			want := http.StatusOK
			if method == http.MethodOptions {
				want = http.StatusNoContent
			}
			if rec.Code != want || rec.Header().Get("Access-Control-Allow-Origin") != origin {
				t.Fatalf("allowed response = %d headers=%v", rec.Code, rec.Header())
			}
			if rec.Header().Get("Vary") != "Origin" || rec.Header().Get("Access-Control-Allow-Headers") != "Content-Type, Authorization" ||
				rec.Header().Get("Access-Control-Allow-Methods") != "GET, POST, PUT, DELETE, OPTIONS" {
				t.Fatalf("allowed headers = %v", rec.Header())
			}
		}
		for _, origin := range []string{
			"https://evil.example", "http://localhost:51730", "http://localhost", "https://localhost:5173",
			"http://localhost.evil.example:5173", "https://ui.example.com:443", "https://ui.example.com.evil.example",
			"null", "*", "http://user:secret@localhost:5173", "http://localhost:5173/",
			"http://localhost:5173?", "http://localhost:5173#", "", "http://localhost:5173 https://evil.example",
		} {
			req := httptest.NewRequest(method, "/api/auth/status", nil)
			req.Header.Set("Origin", origin)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden || rec.Header().Get("Access-Control-Allow-Origin") != "" || rec.Header().Get("Vary") != "Origin" {
				t.Fatalf("blocked %s origin %q response = %d headers=%v", method, origin, rec.Code, rec.Header())
			}
		}
	}
	if called != 2 {
		t.Fatalf("downstream calls = %d, want two allowed actual requests", called)
	}
}

func TestCORSNoOriginAndVary(t *testing.T) {
	t.Setenv(corsOriginsEnv, "")
	called := 0
	h := cors(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	}))
	for _, method := range []string{http.MethodGet, http.MethodOptions} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/api/auth/status", nil))
		want := http.StatusOK
		if method == http.MethodOptions {
			want = http.StatusNoContent
		}
		if rec.Code != want || rec.Header().Get("Access-Control-Allow-Origin") != "" || rec.Header().Get("Vary") != "" {
			t.Fatalf("no-Origin response = %d headers=%v", rec.Code, rec.Header())
		}
	}
	if called != 1 {
		t.Fatalf("no-Origin downstream calls = %d, want one", called)
	}
	for _, origin := range []string{"http://localhost:5173", "https://evil.example"} {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		rec.Header().Add("Vary", "Accept-Encoding")
		h.ServeHTTP(rec, req)
		vary := strings.Join(rec.Header().Values("Vary"), ",")
		if !strings.Contains(vary, "Accept-Encoding") || !strings.Contains(vary, "Origin") {
			t.Fatalf("existing Vary lost: %q", vary)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodOptions} {
		req := httptest.NewRequest(method, "/api/auth/status", nil)
		req.Header.Add("Origin", "http://localhost:5173")
		req.Header.Add("Origin", "https://evil.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("multiple Origin headers response = %d headers=%v", rec.Code, rec.Header())
		}
	}
}
