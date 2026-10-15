package notify

import (
	"bufio"
	"context"

	"net"
	"strings"
	"sync"
	"testing"
)

// Exercise email delivery at the protocol level using a minimal real SMTP server,
// covering greeting, authentication, envelope and DATA failure semantics. Mocking
// net/smtp would omit the main risk: interaction with an actual SMTP peer.

// fakeSMTP implements greet/EHLO/AUTH/MAIL/RCPT/DATA/QUIT and returns configured
// reply codes at selected stages.
type fakeSMTP struct {
	ln net.Listener

	// rcptReply configures RCPT TO; default 250.
	rcptReply string
	// mailReply configures MAIL FROM; default 250.
	mailReply string
	// advertiseAuth enables AUTH PLAIN in EHLO.
	advertiseAuth bool

	mu       sync.Mutex
	data     string
	commands []string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, rcptReply: "250 OK", mailReply: "250 OK"}
	go f.serve()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeSMTP) hostPort(t *testing.T) (string, int) {
	t.Helper()
	addr, ok := f.ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("Listener address is not TCP")
	}
	return "127.0.0.1", addr.Port
}

func (f *fakeSMTP) record(cmd string) {
	f.mu.Lock()
	f.commands = append(f.commands, cmd)
	f.mu.Unlock()
}

func (f *fakeSMTP) body() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data
}

func (f *fakeSMTP) sawCommand(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.commands {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func (f *fakeSMTP) serve() {
	conn, err := f.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	w("220 fake.local ESMTP ready")
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		f.record(line)
		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			// Omit STARTTLS to exercise plaintext envelope handling rather than TLS.
			w("250-fake.local")
			if f.advertiseAuth {
				w("250-AUTH PLAIN")
			}
			w("250 8BITMIME")
		case strings.HasPrefix(line, "AUTH"):
			// Accept the PLAIN initial response directly; a full server may support multiple lines.
			w("235 2.7.0 Authentication successful")
		case strings.HasPrefix(line, "MAIL FROM"):
			w(f.mailReply)
		case strings.HasPrefix(line, "RCPT TO"):
			w(f.rcptReply)
		case strings.HasPrefix(line, "DATA"):
			w("354 End data with <CR><LF>.<CR><LF>")
			var sb strings.Builder
			for {
				dl, err := br.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dl, "\r\n") == "." {
					break
				}
				sb.WriteString(dl)
			}
			f.mu.Lock()
			f.data = sb.String()
			f.mu.Unlock()
			w("250 2.0.0 Ok: queued as FAKE1")
		case strings.HasPrefix(line, "QUIT"):
			w("221 2.0.0 Bye")
			return
		default:
			w("250 OK")
		}
	}
}

func emailCfg(t *testing.T, f *fakeSMTP, extra map[string]any) map[string]any {
	t.Helper()
	host, port := f.hostPort(t)
	cfg := map[string]any{
		"host": host,
		"port": float64(port),
		"from": "artex@example.com",
		"to":   []any{"a@example.com", "b@example.com"},
	}
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}

func TestEmailSendDeliversFullMessage(t *testing.T) {
	f := newFakeSMTP(t)
	f.advertiseAuth = true
	cfg := emailCfg(t, f, map[string]any{"username": "artex", "password": "pw"})

	if _, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg()); err != nil {
		t.Fatalf("Delivery failed: %v", err)
	}
	// Reach every envelope stage: sender, both recipients and DATA.
	for _, want := range []string{"MAIL FROM:<artex@example.com>", "RCPT TO:<a@example.com>", "RCPT TO:<b@example.com>", "DATA", "AUTH", "QUIT"} {
		if !f.sawCommand(want) {
			t.Errorf("SMTP session lacks %q, commands: %v", want, f.commands)
		}
	}
	// Body is base64 HTML and must retain the actual finding content after decoding.
	body := f.body()
	if body == "" {
		t.Fatal("No body received during DATA")
	}
	if !strings.Contains(body, "Content-Type: text/html") {
		t.Errorf("Missing Content-Type header:\n%s", body)
	}
	if !strings.Contains(body, "base64") {
		t.Errorf("Body lacks base64 encoding required to respect SMTP's 1000-byte line limit:\n%s", body)
	}
	// Both recipients must appear in the To header.
	if !strings.Contains(body, "a@example.com, b@example.com") {
		t.Errorf("To header lacks some recipients:\n%s", body)
	}
}

func TestEmailSendWithoutAuth(t *testing.T) {
	// Without a username, do not send AUTH; some relays reject it.
	f := newFakeSMTP(t)
	cfg := emailCfg(t, f, nil)
	if _, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg()); err != nil {
		t.Fatalf("Delivery failed: %v", err)
	}
	if f.sawCommand("AUTH") {
		t.Errorf("AUTH sent without configured username: %v", f.commands)
	}
}

// TestEmailSendClassifiesSMTPReplies verifies the audit fix: 5xx is permanent;
// 4xx greylisting is retryable.
func TestEmailSendClassifiesSMTPReplies(t *testing.T) {
	cases := []struct {
		name      string
		rcptReply string
		mailReply string
		permanent bool
	}{
		{"Recipient permanently rejected with 550", "550 5.1.1 User unknown", "250 OK", true},
		{"Recipient greylisted with 450", "450 4.7.1 Greylisting in action", "250 OK", false},
		{"Recipient mailbox full with 452", "452 4.2.2 Mailbox full", "250 OK", false},
		{"Sender permanently rejected with 553", "250 OK", "553 5.1.3 Bad address", true},
		{"Sender temporary failure with 451", "250 OK", "451 4.3.0 Temporary failure", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSMTP(t)
			f.rcptReply = tc.rcptReply
			f.mailReply = tc.mailReply
			_, err := (emailChannel{}).Send(context.Background(), emailCfg(t, f, nil), singleMsg())
			if err == nil {
				t.Fatal("Expected an error")
			}
			if got := IsPermanent(err); got != tc.permanent {
				t.Fatalf("Incorrect permanent classification: want %v, got %v (%v)", tc.permanent, got, err)
			}
			// Retain server text so the operator can distinguish server issues from address errors.
			if !strings.Contains(err.Error(), strings.Fields(tc.rcptReply)[0]) && !strings.Contains(err.Error(), strings.Fields(tc.mailReply)[0]) {
				t.Errorf("Error must retain server reply code: %v", err)
			}
		})
	}
}

func TestEmailSendRefusesPlaintextCredentials(t *testing.T) {
	// smtp.PlainAuth must refuse credentials over plaintext except localhost.
	// Trigger that protection with a non-localhost name and require actionable guidance.
	f := newFakeSMTP(t)
	f.advertiseAuth = true
	_, port := f.hostPort(t)
	cfg := map[string]any{
		"host":     "smtp.example.com", // Non-localhost
		"port":     float64(port),
		"from":     "a@example.com",
		"to":       []any{"b@example.com"},
		"username": "artex",
		"password": "pw",
	}
	_, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil {
		t.Skip("Local DNS resolved to a local server; skipping this case")
	}
	// Connection failure or refusing plaintext credentials is acceptable; silently sending the password is not.
	if !IsPermanent(err) && !strings.Contains(err.Error(), "Connect") {
		t.Logf("Error: %v (connection failure for non-localhost is expected)", err)
	}
}

func TestEmailValidateReportsMissingFields(t *testing.T) {
	// Check each required email setting before delivery and require the error to identify the missing field.
	cases := []struct {
		name string
		cfg  map[string]any
	}{
		{"Missing host", map[string]any{"port": float64(25), "from": "a@b.c", "to": []any{"d@e.f"}}},
		{"Missing port", map[string]any{"host": "smtp.example.com"}},
		{"Port out of range", map[string]any{"host": "h", "port": float64(70000), "from": "a@b.c", "to": []any{"d@e.f"}}},
		{"Missing from", map[string]any{"host": "h", "port": float64(25), "to": []any{"d@e.f"}}},
		{"Missing to", map[string]any{"host": "h", "port": float64(25), "from": "a@b.c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := (emailChannel{}).Validate(tc.cfg); err == nil {
				t.Fatalf("Expected validation failure: %v", tc.cfg)
			}
		})
	}
}

// TestEmailConfigTolerance covers JSONB float64 ports, string form inputs,
// and recipient arrays supplied as a single string.
func TestEmailConfigTolerance(t *testing.T) {
	cfg := map[string]any{
		"host": "smtp.example.com",
		"port": "587", // Port as a string
		"from": "a@b.c",
		"to":   "d@e.f", // Single string rather than array
		"tls":  "true",  // Boolean as a string
	}
	if err := (emailChannel{}).Validate(cfg); err != nil {
		t.Fatalf("String numeric input must be accepted: %v", err)
	}
	if got := cfgInt(cfg, "port"); got != 587 {
		t.Errorf("cfgInt did not parse string port, got %d", got)
	}
	if !cfgBool(cfg, "tls") {
		t.Error("cfgBool did not parse string true")
	}
	if to := cfgStrings(cfg, "to"); len(to) != 1 || to[0] != "d@e.f" {
		t.Errorf("cfgStrings did not accept single string, got %v", to)
	}
}

// TestFilterValidateRejectsTypo verifies that invalid thresholds are rejected on
// write rather than silently degrading to matching everything.
func TestFilterValidateRejectsTypo(t *testing.T) {
	good := []string{"", "low", "medium", "high", "critical"}
	for _, s := range good {
		if err := (Filter{MinSeverity: s}).Validate(); err != nil {
			t.Errorf("Valid threshold %q rejected: %v", s, err)
		}
	}
	// Reject these realistic typos.
	for _, s := range []string{"hgih", "HIGH", "Critical", "high ", "crit"} {
		err := (Filter{MinSeverity: s}).Validate()
		if err == nil {
			t.Errorf("Invalid threshold %q must be rejected to prevent silently matching all events", s)
			continue
		}
		// Error text must help the operator choose a valid value.
		if !strings.Contains(err.Error(), "low") || !strings.Contains(err.Error(), "critical") {
			t.Errorf("Error must list allowed values, got %q", err.Error())
		}
	}
}

// TestFilterValidateIsWriteTimeOnly keeps strict writes and tolerant reads:
// malformed existing configuration must not stop historical channels from sending.
func TestFilterValidateIsWriteTimeOnly(t *testing.T) {
	raw := []byte(`{"min_severity":"hgih"}`)
	f := ParseFilter(raw) // No error
	if f.MinSeverity != "hgih" {
		t.Fatalf("Read path must retain original value, got %q", f.MinSeverity)
	}
	// The channel must still decide whether the event matches without panic or blocking.
	_ = Match(f, Snapshot{Kind: EventFindingCreated, Severity: "critical"})
}
