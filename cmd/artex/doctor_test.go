package main

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hypnguyen1209/V-ARTEX/agent"
	"github.com/hypnguyen1209/V-ARTEX/db"
)

type doctorQueryConnector struct {
	query func(context.Context, string, []driver.NamedValue) (driver.Rows, error)
}

func (c doctorQueryConnector) Connect(context.Context) (driver.Conn, error) {
	return doctorQueryConn{c}, nil
}
func (c doctorQueryConnector) Driver() driver.Driver { return doctorQueryDriver{c} }

type doctorQueryDriver struct{ doctorQueryConnector }

func (d doctorQueryDriver) Open(string) (driver.Conn, error) {
	return doctorQueryConn{d.doctorQueryConnector}, nil
}

type doctorQueryConn struct{ doctorQueryConnector }

func (doctorQueryConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (doctorQueryConn) Close() error { return nil }
func (doctorQueryConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}
func (c doctorQueryConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.query(ctx, query, args)
}

func TestDoctorProfilesQueryHasDeadline(t *testing.T) {
	var queried, bounded bool
	database := sql.OpenDB(doctorQueryConnector{query: func(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
		queried = true
		_, bounded = ctx.Deadline()
		return nil, errors.New("fixture query failure")
	}})
	defer database.Close()
	start := time.Now()
	result := checkDoctorProfiles(context.Background(), &doctorSession{sql: database})
	if !queried || !bounded {
		t.Errorf("profile query called = %t, has deadline = %t; want both true", queried, bounded)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("profile query returned after %s, want under one second", elapsed)
	}
	if result.Status != doctorFail {
		t.Errorf("profile query failure = %+v, want failure", result)
	}
}

type doctorProfileRows struct {
	values []driver.Value
	closed bool
}

func (r *doctorProfileRows) Columns() []string {
	return []string{"id", "name", "format", "base_url", "proxy", "model", "api_key", "thinking_type", "reasoning_effort", "streaming", "max_tokens", "max_tokens_field", "session_header_key"}
}
func (r *doctorProfileRows) Close() error { r.closed = true; return nil }
func (r *doctorProfileRows) Next(dest []driver.Value) error {
	if r.values == nil {
		return io.EOF
	}
	copy(dest, r.values)
	r.values = nil
	return nil
}

func TestDoctorProfilesCollectsLiveProbeConfiguration(t *testing.T) {
	rows := &doctorProfileRows{values: []driver.Value{
		int64(7), "fixture", "openai", "https://example.test/v1", "http://proxy.test", "fixture-model", "profile-secret",
		"disabled", "high", true, int64(123), "max_completion_tokens", "x-session",
	}}
	database := sql.OpenDB(doctorQueryConnector{query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
		return rows, nil
	}})
	defer database.Close()
	session := &doctorSession{sql: database}
	result := checkDoctorProfiles(context.Background(), session)
	if result.Status != doctorOK || len(session.profiles) != 1 {
		t.Fatalf("profile result = %+v, collected = %d", result, len(session.profiles))
	}
	p := session.profiles[0]
	if p.ID != 7 || p.Name != "fixture" || p.Format != "openai" || p.BaseURL != "https://example.test/v1" || p.Proxy != "http://proxy.test" || p.Model != "fixture-model" || p.APIKey != "profile-secret" || p.ThinkingType != "disabled" || p.ReasoningEffort != "high" || !p.Streaming || p.MaxTokens != 123 || p.MaxTokensField != "max_completion_tokens" || p.SessionHeaderKey != "x-session" {
		t.Fatal("live probe configuration was not preserved")
	}
	if !rows.closed {
		t.Error("profile rows were not closed")
	}
	if len(session.secrets) != 1 || session.secrets[0] != p.APIKey {
		t.Error("profile API key was not collected for redaction")
	}
	if strings.Contains(result.Detail+result.Remediation, p.APIKey) {
		t.Error("profile API key leaked in result")
	}
}

func TestDoctorExecutableBoundsInheritedPipes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "python3"), []byte("#!/bin/sh\n/bin/sleep 4 &\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("ARTEX_CONFIG", filepath.Join(dir, "missing.json"))
	t.Setenv("ARTEX_PG_DSN", "postgres://%")
	t.Setenv("ARTEX_SKILL_DIR", dir)
	start := time.Now()
	results := defaultDoctorDeps().runChecks(context.Background(), doctorOptions{DataDir: dir})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("executable with inherited pipes took %s, want under two seconds", elapsed)
	}
	for _, result := range results {
		if result.Name == "runtime.python" {
			if result.Status != doctorWarn {
				t.Errorf("inherited-pipe probe = %+v, want warning", result)
			}
			return
		}
	}
	t.Fatal("missing Python executable check")
}

func TestDoctorOpenFailureDoesNotEchoKeywordDSN(t *testing.T) {
	const secret = "keyword-secret-sentinel"
	const dsn = "host=localhost user=doctor password=" + secret + " dbname=artex"
	t.Setenv("ARTEX_PG_DSN", dsn)
	_, result := openDoctorDatabase(context.Background(), doctorRuntime{
		openSQL: func(string, string) (*sql.DB, error) { return nil, errors.New("invalid DSN: " + dsn) },
	})
	if text := result.Detail + result.Remediation; strings.Contains(text, secret) || strings.Contains(text, dsn) {
		t.Fatal("PostgreSQL open failure exposes keyword DSN credentials")
	}
	if result.Status != doctorFail {
		t.Errorf("open failure = %+v, want failure", result)
	}
}

func TestDoctorSkipsDependentDatabaseChecksAndRedactsDSN(t *testing.T) {
	t.Setenv("ARTEX_PG_DSN", "postgres://user:secret-pass@127.0.0.1:1/artex?sslmode=disable")
	results := runDefaultDoctorChecks(context.Background(), doctorOptions{
		Addr: "127.0.0.1:0", DataDir: t.TempDir(),
	}, doctorRuntime{
		openSQL:  func(string, string) (*sql.DB, error) { return nil, errors.New("dial failed") },
		lookPath: exec.LookPath, listen: net.Listen, stat: os.Stat, readFile: os.ReadFile,
		runCommand: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("fixture version"), nil
		},
	})
	checks := make(map[string]doctorResult)
	for _, result := range results {
		if strings.Contains(result.Detail+result.Remediation, "secret-pass") {
			t.Fatalf("secret leaked in %s", result.Name)
		}
		checks[result.Name] = result
	}
	if result := checks["postgres.connectivity"]; result.Status != doctorFail {
		t.Errorf("connectivity = %+v, want failure", result)
	}
	if result := checks["postgres.schema"]; result.Status != doctorWarn || !strings.Contains(strings.ToLower(result.Detail), "skipped") {
		t.Errorf("schema = %+v, want skipped warning", result)
	}
}

func TestDoctorOccupiedPortIsWarning(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if result := checkAddress("network.http", listener.Addr().String(), net.Listen); result.Status != doctorWarn {
		t.Fatalf("occupied address = %+v, want warning", result)
	}
}

func TestDoctorConfigParseFailureIsFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARTEX_CONFIG", path)
	if result := checkConfigFile(os.ReadFile); result.Status != doctorFail {
		t.Fatalf("invalid config = %+v, want failure", result)
	}
}

func TestRunDoctorJSONHasStableShapeAndNoANSI(t *testing.T) {
	deps := doctorDeps{runChecks: func(context.Context, doctorOptions) []doctorResult {
		return []doctorResult{{Name: "fixture", Status: doctorOK, Detail: "ready", DurationMS: 3}}
	}}
	var out, errOut bytes.Buffer
	if code := runDoctor([]string{"--json"}, &out, &errOut, deps); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, errOut.String())
	}
	var report doctorReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Version != version || report.Status != doctorOK || report.LiveLLM || len(report.Checks) != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	var public map[string]any
	if err := json.Unmarshal(out.Bytes(), &public); err != nil {
		t.Fatal(err)
	}
	if len(public) != 4 || public["version"] != version || public["status"] != "ok" || public["live_llm"] != false {
		t.Fatalf("unexpected public report fields: %#v", public)
	}
	checks, ok := public["checks"].([]any)
	if !ok || len(checks) != 1 {
		t.Fatalf("unexpected public checks: %#v", public["checks"])
	}
	check, ok := checks[0].(map[string]any)
	if !ok || len(check) != 5 || check["name"] != "fixture" || check["status"] != "ok" || check["detail"] != "ready" || check["remediation"] != "" || check["duration_ms"] != float64(3) {
		t.Fatalf("unexpected public check fields: %#v", checks[0])
	}
	if bytes.Contains(out.Bytes(), []byte("\x1b[")) {
		t.Fatalf("JSON output contains ANSI escapes: %q", out.String())
	}
}

func TestRunDoctorExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		status doctorStatus
		want   int
	}{
		{name: "warning", status: doctorWarn, want: 0},
		{name: "failure", status: doctorFail, want: 1},
		{name: "unknown flag", args: []string{"--unknown"}, status: doctorOK, want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := doctorDeps{runChecks: func(context.Context, doctorOptions) []doctorResult {
				return []doctorResult{{Name: "fixture", Status: tc.status}}
			}}
			if code := runDoctor(tc.args, io.Discard, io.Discard, deps); code != tc.want {
				t.Fatalf("exit code = %d, want %d", code, tc.want)
			}
		})
	}
}

func TestRunDoctorDefaultDisablesLiveLLM(t *testing.T) {
	var captured doctorOptions
	deps := doctorDeps{runChecks: func(_ context.Context, opts doctorOptions) []doctorResult {
		captured = opts
		return nil
	}}
	if code := runDoctor(nil, io.Discard, io.Discard, deps); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if captured.LiveLLM {
		t.Fatal("live LLM checks enabled by default")
	}
}

func TestRunDoctorHumanStatusColumnsAreAligned(t *testing.T) {
	deps := doctorDeps{runChecks: func(context.Context, doctorOptions) []doctorResult {
		return []doctorResult{
			{Name: "fixture", Status: doctorOK},
			{Name: "fixture", Status: doctorWarn},
			{Name: "fixture", Status: doctorFail},
		}
	}}
	var out bytes.Buffer
	if code := runDoctor(nil, &out, io.Discard, deps); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	lines := bytes.Split(out.Bytes(), []byte("\n"))
	if len(lines) != 5 {
		t.Fatalf("unexpected human output: %q", out.String())
	}
	for i, prefix := range []string{"OK    fixture", "WARN  fixture", "FAIL  fixture"} {
		if !bytes.HasPrefix(lines[i+1], []byte(prefix)) {
			t.Errorf("line = %q, want prefix %q", lines[i+1], prefix)
		}
	}
}

var errDoctorWrite = errors.New("fixture write failure")

type failingDoctorWriter struct{}

func (failingDoctorWriter) Write([]byte) (int, error) {
	return 0, errDoctorWrite
}

func TestRunDoctorOutputFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "human"},
		{name: "json", args: []string{"--json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := doctorDeps{runChecks: func(context.Context, doctorOptions) []doctorResult {
				return []doctorResult{{Name: "fixture", Status: doctorOK}}
			}}
			var errOut bytes.Buffer
			if code := runDoctor(tc.args, failingDoctorWriter{}, &errOut, deps); code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if want := "write doctor report: " + errDoctorWrite.Error() + "\n"; errOut.String() != want {
				t.Errorf("stderr = %q, want %q", errOut.String(), want)
			}
		})
	}
}

func TestDoctorLiveLLMChecksKeyedProfiles(t *testing.T) {
	keyed := &db.LLMProfile{
		Name: "fixture", Format: "openai", BaseURL: "https://example.test/v1/chat/completions",
		Proxy: "http://proxy.test", Model: "fixture-model", APIKey: "profile-secret",
		ThinkingType: "disabled", ReasoningEffort: "high", Streaming: false,
		MaxTokens: 123, MaxTokensField: "max_completion_tokens", SessionHeaderKey: "x-session",
	}
	calls := 0
	result := checkLiveLLMProfiles(context.Background(), &doctorSession{
		sql:      &sql.DB{},
		profiles: []*db.LLMProfile{{Name: "keyless"}, keyed},
	}, doctorRuntime{liveLLM: func(_ context.Context, cfg agent.Config) (time.Duration, string, error) {
		calls++
		if cfg.BaseURL != "https://example.test/v1" || cfg.Proxy != keyed.Proxy || cfg.Model != keyed.Model || cfg.APIKey != keyed.APIKey || cfg.ThinkingType != keyed.ThinkingType || cfg.ReasoningEffort != keyed.ReasoningEffort || cfg.Stream != keyed.Streaming || cfg.MaxTokens != keyed.MaxTokens || cfg.MaxTokensField != keyed.MaxTokensField || cfg.SessionHeaderKey != keyed.SessionHeaderKey {
			t.Fatalf("live probe configuration = %+v", cfg)
		}
		return time.Millisecond, "OK", nil
	}})
	if calls != 1 || result.Status != doctorOK || !strings.Contains(result.Detail, "1 keyed profile") {
		t.Fatalf("calls = %d, result = %+v", calls, result)
	}
}

func TestDefaultDoctorNeverCallsLLM(t *testing.T) {
	t.Setenv("ARTEX_PG_DSN", "postgres://user:secret@127.0.0.1:1/artex?sslmode=disable")
	calls := 0
	runtime := doctorRuntime{
		openSQL:    func(string, string) (*sql.DB, error) { return nil, errors.New("fixture connection failure") },
		lookPath:   func(name string) (string, error) { return name, nil },
		runCommand: func(context.Context, string, ...string) ([]byte, error) { return []byte("fixture version"), nil },
		listen:     net.Listen, stat: os.Stat, readFile: os.ReadFile,
		liveLLM: func(context.Context, agent.Config) (time.Duration, string, error) {
			calls++
			return 0, "", nil
		},
	}
	_ = runDefaultDoctorChecks(t.Context(), doctorOptions{
		Addr: "127.0.0.1:0", Proxy: "", DataDir: t.TempDir(), LiveLLM: false,
	}, runtime)
	if calls != 0 {
		t.Fatalf("default doctor made %d live LLM calls", calls)
	}
}
