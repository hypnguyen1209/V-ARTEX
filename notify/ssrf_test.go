package notify

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// Test SSRF protection and redaction of validation errors. Ordinary tests use
// loopback httptest receivers, so TestMain opts into AllowLocalTargetsEnv. Each
// SSRF test explicitly clears it to verify rejection under the default configuration.

func TestMain(m *testing.M) {
	// Allow ordinary tests to reach local receivers; SSRF cases temporarily clear this.
	_ = os.Setenv(AllowLocalTargetsEnv, "1")
	os.Exit(m.Run())
}

// TestDialGuardRejectsLoopbackByDefault requires connection-layer rejection
// of loopback destinations under the default configuration.
func TestDialGuardRejectsLoopbackByDefault(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = io.WriteString(w, `{"errcode":0}`)
	}))
	defer srv.Close()

	t.Setenv(AllowLocalTargetsEnv, "") // Disable opt-in to restore default behavior
	_, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"}, Message{Items: []Item{{Severity: "high"}}})
	if err == nil {
		t.Fatal("Default configuration must reject loopback delivery")
	}
	if hit {
		t.Fatal("Request reached local service; guard failed")
	}
	// Explain the explicit opt-in for legitimate local SMTP relays.
	if !strings.Contains(err.Error(), AllowLocalTargetsEnv) {
		t.Errorf("Rejection must explain explicit opt-in: %v", err)
	}
}

// TestDialGuardAllowsLoopbackWhenOptedIn preserves explicitly enabled local
// postfix and relay deployments.
func TestDialGuardAllowsLoopbackWhenOptedIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer srv.Close()

	t.Setenv(AllowLocalTargetsEnv, "1")
	if _, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"}, Message{Items: []Item{{Severity: "high"}}}); err != nil {
		t.Fatalf("Explicit opt-in must allow delivery: %v", err)
	}
}

func TestIsBlockedDialIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.1.2.3", "::1",
		"169.254.169.254", // Cloud metadata endpoint; a primary reason for this guard
		"169.254.1.1", "fe80::1",
		"0.0.0.0", "::",
		"224.0.0.1", "ff02::1",
		"::ffff:127.0.0.1", // Normalize IPv4-mapped addresses before checking to prevent bypass
		"",
	}
	for _, s := range blocked {
		if !isBlockedDialIP(net.ParseIP(s)) {
			t.Errorf("%s must be rejected", s)
		}
	}
	// RFC1918 private destinations intentionally remain allowed for self-hosted
	// Mattermost and SMTP relays. Lock this tradeoff so broadening rejection requires
	// a conscious decision rather than silently breaking deployments.
	allowed := []string{"10.0.0.5", "172.16.3.4", "192.168.1.10", "8.8.8.8", "2606:4700::1111"}
	for _, s := range allowed {
		if isBlockedDialIP(net.ParseIP(s)) {
			t.Errorf("%s must be allowed for legitimate private-network delivery", s)
		}
	}
}

// TestValidateHTTPURLRejectsLiteralPrivateTargets provides save-time feedback
// for blocked literal IPs instead of waiting for a delivery failure.
func TestValidateHTTPURLRejectsLiteralPrivateTargets(t *testing.T) {
	t.Setenv(AllowLocalTargetsEnv, "")
	for _, raw := range []string{
		"http://127.0.0.1:8080/hook",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]:8080/hook",
	} {
		if err := validateHTTPURL(raw); err == nil {
			t.Errorf("%s must be rejected during configuration validation", raw)
		}
	}
	// Public and RFC1918 private addresses remain valid; the dial guard also allows private addresses.
	for _, raw := range []string{"https://oapi.dingtalk.com/robot/send", "http://10.0.0.9/hook"} {
		if err := validateHTTPURL(raw); err != nil {
			t.Errorf("%s must pass validation: %v", raw, err)
		}
	}
}

// TestValidateHTTPURLErrorNeverLeaksCredentials covers url.Parse failure, whose
// *url.Error includes the complete original URL. Unsupported file/gopher/ftp schemes
// parse successfully and cannot prove that parse-error redaction works.
func TestValidateHTTPURLErrorNeverLeaksCredentials(t *testing.T) {
	cases := []string{
		"http://127.0.0.1/%zz?access_token=" + leakProbeToken,         // Invalid percent escape
		"https://a.example.com:port/x?access_token=" + leakProbeToken, // Non-numeric port
		"http://[::1?access_token=" + leakProbeToken,                  // Unmatched bracket
	}
	for _, raw := range cases {
		// First confirm url.Parse actually fails so the test cannot silently exercise a different branch.
		if _, err := url.Parse(raw); err == nil {
			t.Errorf("%q must fail parsing to exercise the intended branch", raw)
			continue
		}
		err := validateHTTPURL(raw)
		if err == nil {
			t.Errorf("%q must fail validation", raw)
			continue
		}
		assertNoSecret(t, err.Error(), leakProbeToken)
	}
	// Also check that channel-level error wrapping does not leak the URL.
	t.Setenv(AllowLocalTargetsEnv, "")
	err := (dingTalkChannel{}).Validate(map[string]any{"webhook": cases[0]})
	if err == nil {
		t.Fatal("Invalid address must fail validation")
	}
	assertNoSecret(t, err.Error(), leakProbeToken)
}

// TestEmailDialGuardRejectsLoopbackByDefault covers the SMTP guard. An unguarded
// net.Dialer would allow metadata/local targets; handshake errors could expose peer
// responses through last_error and delivery history, while timing reveals ports.
// Clear TestMain's global local-target opt-in or the test cannot distinguish
// whether the guard exists.
func TestEmailDialGuardRejectsLoopbackByDefault(t *testing.T) {
	f := newFakeSMTP(t)
	cfg := emailCfg(t, f, nil)

	t.Setenv(AllowLocalTargetsEnv, "") // Disable opt-in to restore default behavior
	_, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil {
		t.Fatal("Default configuration must reject loopback email delivery")
	}
	// The Control hook must block before connection setup, so EHLO is never sent.
	if f.sawCommand("EHLO") || f.sawCommand("HELO") {
		t.Fatal("SMTP session was established; guard failed")
	}
	// Explain the explicit opt-in for legitimate local postfix relays.
	if !strings.Contains(err.Error(), AllowLocalTargetsEnv) {
		t.Errorf("Rejection must explain explicit opt-in: %v", err)
	}
}

// TestEmailDialGuardAllowsLoopbackWhenOptedIn preserves valid local/self-hosted
// SMTP delivery when explicitly enabled.
func TestEmailDialGuardAllowsLoopbackWhenOptedIn(t *testing.T) {
	f := newFakeSMTP(t)
	cfg := emailCfg(t, f, nil)

	t.Setenv(AllowLocalTargetsEnv, "1")
	if _, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg()); err != nil {
		t.Fatalf("Local SMTP must work after explicit opt-in: %v", err)
	}
	if !f.sawCommand("EHLO") {
		t.Fatal("No EHLO observed; session was not established")
	}
}
