package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// GetSetting returns an empty value both for missing keys and failed reads;
// only the error distinguishes them. Treating an error as an unset password
// opens authInit during database outages, allowing unauthenticated overwrites.
// authStatus would also direct the frontend to /setup.
//
// These tests close the connection pool and verify that both handlers fail closed.
func TestAuthStatusFailsClosedWhenDataSourceUnavailable(t *testing.T) {
	pg, _ := authSetupDatabase(t)
	m := &Manager{pg: pg}
	// Close the pool so GetSetting returns an error rather than sql.ErrNoRows.
	if err := m.pg.Close(); err != nil {
		t.Fatal(err)
	}

	s := &Server{m: m}
	w := httptest.NewRecorder()
	s.authStatus(w, httptest.NewRequest("GET", "/api/auth/status", nil))

	if w.Code != 503 {
		t.Fatalf("status=%d want 503 (treating read failure as uninitialized would send users to /setup to overwrite the password); body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		Initialized *bool `json:"initialized"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err == nil && payload.Initialized != nil {
		t.Fatalf("must not return initialized on read failure, got %v", *payload.Initialized)
	}
}

func TestAuthInitFailsClosedWhenDataSourceUnavailable(t *testing.T) {
	pg, _ := authSetupDatabase(t)
	m := &Manager{pg: pg}
	if err := m.pg.Close(); err != nil {
		t.Fatal(err)
	}

	s := &Server{m: m}
	w := httptest.NewRecorder()
	body := strings.NewReader(`{"password":"new-password","setup_token":"unavailable-db-token"}`)
	s.authInit(w, httptest.NewRequest("POST", "/api/auth/init", body))

	if w.Code != 503 {
		t.Fatalf("status=%d want 503 (allowing read failure would permit unauthenticated password overwrites); body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "token") {
		t.Fatalf("must not issue a token on read failure: %s", w.Body.String())
	}
}

func TestValidatePassword(t *testing.T) {
	for _, tc := range []struct {
		name, pw string
		wantErr  bool
	}{
		{"empty", "", true},
		{"seven characters", "1234567", true},
		{"eight characters", "12345678", false},
		{"eight multibyte characters are counted as characters", "éééééééé", false},
		{"three multibyte characters occupy nine bytes but count as three", "€€€", true},
		{"72 bytes", strings.Repeat("a", 72), false},
		{"73 bytes exceed the bcrypt limit", strings.Repeat("a", 73), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validatePassword(tc.pw); (got != "") != tc.wantErr {
				t.Fatalf("validatePassword(%q)=%q, wantErr=%v", tc.pw, got, tc.wantErr)
			}
		})
	}
}
