package main

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hypnguyen1209/V-ARTEX/agent"
	"github.com/hypnguyen1209/V-ARTEX/db"
	"github.com/jackc/pgx/v5"
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

type doctorStaticRows struct {
	columns []string
	values  []driver.Value
}

func (r *doctorStaticRows) Columns() []string { return r.columns }
func (*doctorStaticRows) Close() error        { return nil }
func (r *doctorStaticRows) Next(dest []driver.Value) error {
	if r.values == nil {
		return io.EOF
	}
	copy(dest, r.values)
	r.values = nil
	return nil
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

func TestDoctorWinsockOccupiedPortIsWarning(t *testing.T) {
	err := &net.OpError{Op: "listen", Net: "tcp", Err: &os.SyscallError{Syscall: "bind", Err: syscall.Errno(10048)}}
	result := checkAddress("network.http", "127.0.0.1:8080", func(string, string) (net.Listener, error) {
		return nil, err
	})
	if result.Status != doctorWarn {
		t.Fatalf("Winsock occupied address = %+v, want warning", result)
	}
}

func TestDoctorInvalidAddressIsFailure(t *testing.T) {
	if result := checkAddress("network.http", "127.0.0.1:99999", net.Listen); result.Status != doctorFail {
		t.Fatalf("invalid address = %+v, want failure", result)
	}
}

func TestDoctorUnavailableAddressIsFailureWithoutRawError(t *testing.T) {
	const rawError = "listen tcp: fixture unavailable error"
	result := checkAddress("network.http", "127.0.0.1:8080", func(string, string) (net.Listener, error) {
		return nil, errors.New(rawError)
	})
	if result.Status != doctorFail {
		t.Fatalf("unavailable address = %+v, want failure", result)
	}
	if strings.Contains(result.Detail, rawError) || strings.Contains(result.Remediation, rawError) {
		t.Fatalf("unavailable address exposes raw error: %+v", result)
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

func TestDoctorLiveLLMDoesNotExposeProviderErrors(t *testing.T) {
	const sentinel = "unknown-provider-secret-sentinel"
	result := checkLiveLLMProfiles(context.Background(), &doctorSession{
		sql: &sql.DB{},
		profiles: []*db.LLMProfile{{
			Name: "fixture", Format: "openai", Model: "fixture-model", APIKey: "profile-key",
		}},
	}, doctorRuntime{liveLLM: func(context.Context, agent.Config) (time.Duration, string, error) {
		return 0, "", errors.New("provider rejected " + sentinel)
	}})
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Detail+result.Remediation, sentinel) || bytes.Contains(encoded, []byte(sentinel)) {
		t.Fatalf("live probe exposed provider error in human or JSON output: %s", encoded)
	}
}

func TestDoctorLiveLLMContinuesAfterFailure(t *testing.T) {
	const sentinel = "first-provider-error-sentinel"
	var called []string
	result := checkLiveLLMProfiles(context.Background(), &doctorSession{
		sql: &sql.DB{},
		profiles: []*db.LLMProfile{
			{Name: "first", Format: "openai", Model: "first-model", APIKey: "first-key"},
			{Name: "second", Format: "openai", Model: "second-model", APIKey: "second-key"},
		},
	}, doctorRuntime{liveLLM: func(_ context.Context, cfg agent.Config) (time.Duration, string, error) {
		called = append(called, cfg.Model)
		if cfg.Model == "first-model" {
			return 0, "", errors.New(sentinel)
		}
		return time.Millisecond, "OK", nil
	}})
	if strings.Join(called, ",") != "first-model,second-model" {
		t.Fatalf("live probes called %q, want both profiles in order", called)
	}
	if result.Status != doctorFail || !strings.Contains(result.Detail, "2 attempted") || !strings.Contains(result.Detail, "1 succeeded") || !strings.Contains(result.Detail, "1 failed") {
		t.Fatalf("aggregate result = %+v", result)
	}
	if strings.Contains(result.Detail+result.Remediation, sentinel) {
		t.Fatalf("aggregate result exposed provider error: %+v", result)
	}
}

func TestDoctorLiveLLMSkipsWhitespaceOnlyAPIKeys(t *testing.T) {
	calls := 0
	result := checkLiveLLMProfiles(context.Background(), &doctorSession{
		sql: &sql.DB{},
		profiles: []*db.LLMProfile{{
			Name: "blank-key", Format: "openai", Model: "fixture-model", APIKey: " \t\n ",
		}},
	}, doctorRuntime{liveLLM: func(context.Context, agent.Config) (time.Duration, string, error) {
		calls++
		return 0, "OK", nil
	}})
	if calls != 0 || result.Status != doctorWarn || !strings.Contains(result.Detail, "No keyed profile") {
		t.Fatalf("calls = %d, result = %+v", calls, result)
	}
}

func TestDefaultDoctorSkipsLiveLLMWhenProfileValidationFails(t *testing.T) {
	database := sql.OpenDB(doctorQueryConnector{query: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
		switch {
		case strings.Contains(query, "SELECT 1"):
			return &doctorStaticRows{columns: []string{"one"}, values: []driver.Value{int64(1)}}, nil
		case strings.Contains(query, "to_regclass"):
			return &doctorStaticRows{columns: []string{"to_regclass"}, values: []driver.Value{"settings"}}, nil
		case strings.Contains(query, "SHOW server_version"):
			return &doctorStaticRows{columns: []string{"server_version"}, values: []driver.Value{"16"}}, nil
		case strings.Contains(query, "FROM llm_profiles"):
			return &doctorProfileRows{values: []driver.Value{
				int64(1), "invalid", "unsupported", "https://example.test", "", "fixture-model", "profile-key",
				"", "", true, int64(0), "", "",
			}}, nil
		default:
			return nil, errors.New("unexpected query: " + query)
		}
	}})
	t.Setenv("ARTEX_PG_DSN", "postgres://doctor:secret@localhost/artex?sslmode=disable")
	t.Setenv("ARTEX_SKILL_DIR", t.TempDir())
	calls := 0
	results := runDefaultDoctorChecks(context.Background(), doctorOptions{
		Addr: "", Proxy: "", DataDir: t.TempDir(), LiveLLM: true,
	}, doctorRuntime{
		openSQL:    func(string, string) (*sql.DB, error) { return database, nil },
		lookPath:   func(string) (string, error) { return "", os.ErrNotExist },
		runCommand: func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
		listen:     net.Listen,
		stat:       func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		readFile:   func(string) ([]byte, error) { return nil, os.ErrNotExist },
		liveLLM: func(context.Context, agent.Config) (time.Duration, string, error) {
			calls++
			return 0, "OK", nil
		},
	})
	checks := make(map[string]doctorResult)
	for _, result := range results {
		checks[result.Name] = result
	}
	if calls != 0 {
		t.Fatalf("invalid profiles triggered %d live calls", calls)
	}
	if result := checks["llm.profiles"]; result.Status != doctorFail {
		t.Fatalf("profile validation = %+v, want failure", result)
	}
	if result := checks["llm.live"]; result.Status != doctorWarn || !strings.Contains(strings.ToLower(result.Detail), "skipped") || !strings.Contains(strings.ToLower(result.Detail), "no live") {
		t.Fatalf("live check = %+v, want non-billable skipped warning", result)
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

func doctorDSNWithSearchPath(baseDSN, searchPath string) (string, error) {
	baseDSN = strings.TrimSpace(baseDSN)
	parsed, err := url.Parse(baseDSN)
	var isolatedDSN string
	if err == nil && (strings.EqualFold(parsed.Scheme, "postgres") || strings.EqualFold(parsed.Scheme, "postgresql")) {
		query := parsed.Query()
		query.Set("search_path", searchPath)
		parsed.RawQuery = query.Encode()
		isolatedDSN = parsed.String()
	} else {
		quoted := strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(searchPath)
		isolatedDSN = baseDSN + " search_path='" + quoted + "'"
	}
	config, err := pgx.ParseConfig(isolatedDSN)
	if err != nil {
		return "", fmt.Errorf("parse isolated PostgreSQL DSN: %w", err)
	}
	if config.RuntimeParams["search_path"] != searchPath {
		return "", fmt.Errorf("isolated PostgreSQL DSN did not retain search_path")
	}
	return isolatedDSN, nil
}

func TestDoctorDSNWithSearchPathSupportsURLAndKeyword(t *testing.T) {
	searchPath := pgx.Identifier{`Doctor "Schema`}.Sanitize()
	for _, baseDSN := range []string{
		"postgres://doctor:secret@localhost/artex?sslmode=disable",
		"host=localhost port=5432 user=doctor password=secret dbname=artex sslmode=disable",
	} {
		isolatedDSN, err := doctorDSNWithSearchPath(baseDSN, searchPath)
		if err != nil {
			t.Fatalf("build isolated DSN from %q: %v", baseDSN, err)
		}
		cfg, err := pgx.ParseConfig(isolatedDSN)
		if err != nil {
			t.Fatalf("parse isolated DSN from %q: %v", baseDSN, err)
		}
		if got := cfg.RuntimeParams["search_path"]; got != searchPath {
			t.Errorf("search_path from %q = %q, want %q", baseDSN, got, searchPath)
		}
	}
}

type doctorFilesystemEntry struct {
	Path          string `json:"path"`
	Type          string `json:"type"`
	Mode          uint32 `json:"mode"`
	SymlinkTarget string `json:"symlink_target,omitempty"`
	Content       []byte `json:"content,omitempty"`
}

func snapshotDoctorFilesystem(root string) ([]byte, error) {
	var entries []doctorFilesystemEntry
	err := filepath.WalkDir(root, func(path string, _ os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entry := doctorFilesystemEntry{
			Path: filepath.ToSlash(relative),
			Type: info.Mode().Type().String(),
			Mode: uint32(info.Mode()),
		}
		switch {
		case info.Mode().IsRegular():
			entry.Content, err = os.ReadFile(path)
		case info.Mode()&os.ModeSymlink != 0:
			entry.SymlinkTarget, err = os.Readlink(path)
		}
		if err != nil {
			return err
		}
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(entries)
}

func TestDoctorFilesystemSnapshotDetectsPersistentChanges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture requires no elevated Windows privileges")
	}
	root := t.TempDir()
	empty, err := snapshotDoctorFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(root, "state")
	if err := os.WriteFile(filePath, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	created, err := snapshotDoctorFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(empty, created) {
		t.Fatal("filesystem snapshot missed a created file")
	}
	if err := os.WriteFile(filePath, []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	contentChanged, err := snapshotDoctorFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(created, contentChanged) {
		t.Fatal("filesystem snapshot missed changed file content")
	}
	if err := os.Chmod(filePath, 0644); err != nil {
		t.Fatal(err)
	}
	modeChanged, err := snapshotDoctorFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(contentChanged, modeChanged) {
		t.Fatal("filesystem snapshot missed a changed file mode")
	}
	linkPath := filepath.Join(root, "link")
	if err := os.Symlink("first-target", linkPath); err != nil {
		t.Fatal(err)
	}
	firstTarget, err := snapshotDoctorFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(linkPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("second-target", linkPath); err != nil {
		t.Fatal(err)
	}
	secondTarget, err := snapshotDoctorFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(firstTarget, secondTarget) {
		t.Fatal("filesystem snapshot missed a changed symlink target")
	}
}

func openIsolatedDoctorDatabase(t *testing.T, baseDSN string) (string, *sql.DB) {
	t.Helper()
	admin, err := sql.Open("pgx", baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.PingContext(t.Context()); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	schema := "doctor_test_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.ExecContext(t.Context(), `CREATE SCHEMA `+quotedSchema); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+quotedSchema+` CASCADE`); err != nil {
			t.Errorf("drop isolated doctor schema: %v", err)
		}
		if err := admin.Close(); err != nil {
			t.Errorf("close isolated doctor schema connection: %v", err)
		}
	})

	isolatedDSN, err := doctorDSNWithSearchPath(baseDSN, quotedSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARTEX_PG_DSN", isolatedDSN)
	migrated, err := db.Open(isolatedDSN)
	if err != nil {
		t.Fatal(err)
	}
	return schema, migrated.DB
}

type doctorTableContents struct {
	Name string            `json:"name"`
	Rows []json.RawMessage `json:"rows"`
}

type doctorDatabaseSnapshot struct {
	Tables         []json.RawMessage     `json:"tables"`
	Columns        []json.RawMessage     `json:"columns"`
	Indexes        []json.RawMessage     `json:"indexes"`
	Constraints    []json.RawMessage     `json:"constraints"`
	Sequences      []json.RawMessage     `json:"sequences"`
	SequenceValues []json.RawMessage     `json:"sequence_values"`
	Functions      []json.RawMessage     `json:"functions"`
	Triggers       []json.RawMessage     `json:"triggers"`
	Contents       []doctorTableContents `json:"contents"`
}

func queryDoctorSnapshotNames(ctx context.Context, database *sql.DB, query string, args ...any) ([]string, error) {
	rows, err := database.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func queryDoctorSnapshotJSON(ctx context.Context, database *sql.DB, query string, args ...any) ([]json.RawMessage, error) {
	rows, err := database.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]json.RawMessage, 0)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if !json.Valid([]byte(raw)) {
			return nil, fmt.Errorf("snapshot query returned invalid JSON")
		}
		values = append(values, json.RawMessage(raw))
	}
	return values, rows.Err()
}

func snapshotDoctorDatabase(ctx context.Context, database *sql.DB, schema string) ([]byte, error) {
	tableNames, err := queryDoctorSnapshotNames(ctx, database, `
SELECT c.relname
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN ('r', 'p')
ORDER BY c.relname`, schema)
	if err != nil {
		return nil, fmt.Errorf("list base tables: %w", err)
	}
	sequenceNames, err := queryDoctorSnapshotNames(ctx, database, `
SELECT c.relname
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind = 'S'
ORDER BY c.relname`, schema)
	if err != nil {
		return nil, fmt.Errorf("list sequences: %w", err)
	}

	snapshot := doctorDatabaseSnapshot{}
	snapshot.Tables, err = queryDoctorSnapshotJSON(ctx, database, `
SELECT jsonb_build_object(
    'name', c.relname,
    'kind', c.relkind::text,
    'persistence', c.relpersistence::text,
    'row_security', c.relrowsecurity,
    'force_row_security', c.relforcerowsecurity
)::text
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN ('r', 'p')
ORDER BY c.relname`, schema)
	if err != nil {
		return nil, fmt.Errorf("snapshot tables: %w", err)
	}
	snapshot.Columns, err = queryDoctorSnapshotJSON(ctx, database, `
SELECT jsonb_build_object(
    'table', c.relname,
    'position', a.attnum,
    'name', a.attname,
    'type', pg_catalog.format_type(a.atttypid, a.atttypmod),
    'not_null', a.attnotnull,
    'default', pg_catalog.pg_get_expr(d.adbin, d.adrelid),
    'identity', a.attidentity::text,
    'generated', a.attgenerated::text,
    'collation', coll.collname
)::text
FROM pg_catalog.pg_attribute a
JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
LEFT JOIN pg_catalog.pg_collation coll ON coll.oid = a.attcollation
WHERE n.nspname = $1 AND c.relkind IN ('r', 'p') AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY c.relname, a.attnum`, schema)
	if err != nil {
		return nil, fmt.Errorf("snapshot columns: %w", err)
	}
	snapshot.Indexes, err = queryDoctorSnapshotJSON(ctx, database, `
SELECT jsonb_build_object(
    'table', table_name,
    'name', index_name,
    'definition', definition
)::text
FROM (
    SELECT tablename AS table_name, indexname AS index_name, indexdef AS definition
    FROM pg_catalog.pg_indexes
    WHERE schemaname = $1
) indexes
ORDER BY table_name, index_name`, schema)
	if err != nil {
		return nil, fmt.Errorf("snapshot indexes: %w", err)
	}
	snapshot.Constraints, err = queryDoctorSnapshotJSON(ctx, database, `
SELECT jsonb_build_object(
    'table', COALESCE(c.relname, ''),
    'name', constraint_row.conname,
    'type', constraint_row.contype::text,
    'definition', pg_catalog.pg_get_constraintdef(constraint_row.oid, true),
    'deferrable', constraint_row.condeferrable,
    'deferred', constraint_row.condeferred,
    'validated', constraint_row.convalidated
)::text
FROM pg_catalog.pg_constraint constraint_row
JOIN pg_catalog.pg_namespace n ON n.oid = constraint_row.connamespace
LEFT JOIN pg_catalog.pg_class c ON c.oid = constraint_row.conrelid
WHERE n.nspname = $1
ORDER BY COALESCE(c.relname, ''), constraint_row.conname`, schema)
	if err != nil {
		return nil, fmt.Errorf("snapshot constraints: %w", err)
	}
	snapshot.Sequences, err = queryDoctorSnapshotJSON(ctx, database, `
SELECT jsonb_build_object(
    'name', c.relname,
    'type', pg_catalog.format_type(sequence_row.seqtypid, NULL),
    'start', sequence_row.seqstart,
    'increment', sequence_row.seqincrement,
    'minimum', sequence_row.seqmin,
    'maximum', sequence_row.seqmax,
    'cache', sequence_row.seqcache,
    'cycle', sequence_row.seqcycle
)::text
FROM pg_catalog.pg_sequence sequence_row
JOIN pg_catalog.pg_class c ON c.oid = sequence_row.seqrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1
ORDER BY c.relname`, schema)
	if err != nil {
		return nil, fmt.Errorf("snapshot sequences: %w", err)
	}
	snapshot.Functions, err = queryDoctorSnapshotJSON(ctx, database, `
SELECT jsonb_build_object(
    'name', function_row.proname,
    'kind', function_row.prokind::text,
    'identity_arguments', pg_catalog.pg_get_function_identity_arguments(function_row.oid),
    'arguments', pg_catalog.pg_get_function_arguments(function_row.oid),
    'result', pg_catalog.pg_get_function_result(function_row.oid),
    'language', language_row.lanname,
    'volatility', function_row.provolatile::text,
    'parallel', function_row.proparallel::text,
    'security_definer', function_row.prosecdef,
    'strict', function_row.proisstrict,
    'leakproof', function_row.proleakproof,
    'returns_set', function_row.proretset,
    'definition', pg_catalog.pg_get_functiondef(function_row.oid)
)::text
FROM pg_catalog.pg_proc function_row
JOIN pg_catalog.pg_namespace n ON n.oid = function_row.pronamespace
JOIN pg_catalog.pg_language language_row ON language_row.oid = function_row.prolang
WHERE n.nspname = $1 AND function_row.prokind IN ('f', 'p')
ORDER BY function_row.proname,
         pg_catalog.pg_get_function_identity_arguments(function_row.oid),
         function_row.prokind`, schema)
	if err != nil {
		return nil, fmt.Errorf("snapshot functions: %w", err)
	}
	snapshot.Triggers, err = queryDoctorSnapshotJSON(ctx, database, `
SELECT jsonb_build_object(
    'relation', relation_row.relname,
    'relation_kind', relation_row.relkind::text,
    'name', trigger_row.tgname,
    'enabled', trigger_row.tgenabled::text,
    'definition', pg_catalog.pg_get_triggerdef(trigger_row.oid, true)
)::text
FROM pg_catalog.pg_trigger trigger_row
JOIN pg_catalog.pg_class relation_row ON relation_row.oid = trigger_row.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid = relation_row.relnamespace
WHERE n.nspname = $1 AND NOT trigger_row.tgisinternal
ORDER BY relation_row.relname, trigger_row.tgname`, schema)
	if err != nil {
		return nil, fmt.Errorf("snapshot triggers: %w", err)
	}

	snapshot.Contents = make([]doctorTableContents, 0, len(tableNames))
	for _, table := range tableNames {
		qualified := pgx.Identifier{schema, table}.Sanitize()
		rows, err := queryDoctorSnapshotJSON(ctx, database,
			`SELECT to_jsonb(snapshot_row)::text FROM `+qualified+` AS snapshot_row ORDER BY to_jsonb(snapshot_row)::text`)
		if err != nil {
			return nil, fmt.Errorf("snapshot table %s: %w", table, err)
		}
		snapshot.Contents = append(snapshot.Contents, doctorTableContents{Name: table, Rows: rows})
	}
	snapshot.SequenceValues = make([]json.RawMessage, 0, len(sequenceNames))
	for _, sequence := range sequenceNames {
		qualified := pgx.Identifier{schema, sequence}.Sanitize()
		values, err := queryDoctorSnapshotJSON(ctx, database,
			`SELECT jsonb_build_object('name', $1::text, 'last_value', last_value, 'is_called', is_called)::text FROM `+qualified,
			sequence)
		if err != nil {
			return nil, fmt.Errorf("snapshot sequence %s: %w", sequence, err)
		}
		snapshot.SequenceValues = append(snapshot.SequenceValues, values...)
	}
	return json.Marshal(snapshot)
}

func TestDoctorDoesNotPersistDatabaseOrFilesystemState(t *testing.T) {
	baseDSN, _, err := db.DSN()
	if err != nil {
		t.Skip("test PostgreSQL is not configured")
	}
	schema, sqlDB := openIsolatedDoctorDatabase(t, baseDSN)
	defer sqlDB.Close()

	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	skillDir := filepath.Join(root, "skills")
	jwtDir := filepath.Join(root, "jwt")
	dataDir := filepath.Join(root, "data")
	leaves := []string{configPath, skillDir, jwtDir, dataDir}
	for _, leaf := range leaves {
		if _, err := os.Lstat(leaf); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("isolated input must start absent: %s: %v", leaf, err)
		}
	}
	t.Setenv("ARTEX_CONFIG", configPath)
	t.Setenv("ARTEX_SKILL_DIR", skillDir)
	t.Setenv("ARTEX_JWT_KEY_DIR", jwtDir)

	beforeDatabase, err := snapshotDoctorDatabase(t.Context(), sqlDB, schema)
	if err != nil {
		t.Fatal(err)
	}
	beforeFilesystem, err := snapshotDoctorFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	code := runDoctor([]string{"--json", "--addr", "127.0.0.1:0", "--proxy", "", "--data", dataDir}, &output, io.Discard, defaultDoctorDeps())
	if code != 1 {
		t.Fatalf("doctor exit code = %d, want 1 for intentionally missing required paths: %s", code, output.String())
	}
	var report doctorReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("doctor emitted invalid JSON: %v\n%s", err, output.String())
	}
	afterDatabase, err := snapshotDoctorDatabase(t.Context(), sqlDB, schema)
	if err != nil {
		t.Fatal(err)
	}
	afterFilesystem, err := snapshotDoctorFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeDatabase, afterDatabase) {
		t.Fatal("doctor mutated persistent PostgreSQL state")
	}
	if !bytes.Equal(beforeFilesystem, afterFilesystem) {
		t.Fatal("doctor mutated an isolated filesystem input")
	}
	for _, leaf := range leaves {
		if _, err := os.Lstat(leaf); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("doctor created an absent filesystem input: %s: %v", leaf, err)
		}
	}

	probeTable := pgx.Identifier{schema, "doctor_snapshot_probe"}.Sanitize()
	if _, err := sqlDB.ExecContext(t.Context(), `CREATE TABLE `+probeTable+` (id BIGSERIAL PRIMARY KEY, value TEXT UNIQUE CHECK (value <> ''))`); err != nil {
		t.Fatal(err)
	}
	metadataChanged, err := snapshotDoctorDatabase(t.Context(), sqlDB, schema)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(afterDatabase, metadataChanged) {
		t.Fatal("database snapshot missed table, column, index, constraint, or sequence metadata changes")
	}
	probeSequence := pgx.Identifier{schema, "doctor_snapshot_probe_id_seq"}.Sanitize()
	if _, err := sqlDB.ExecContext(t.Context(), `SELECT nextval($1::regclass)`, probeSequence); err != nil {
		t.Fatal(err)
	}
	sequenceChanged, err := snapshotDoctorDatabase(t.Context(), sqlDB, schema)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(metadataChanged, sequenceChanged) {
		t.Fatal("database snapshot missed a changed sequence value")
	}
	if _, err := sqlDB.ExecContext(t.Context(), `INSERT INTO `+probeTable+` (id, value) VALUES (42, 'changed')`); err != nil {
		t.Fatal(err)
	}
	rowChanged, err := snapshotDoctorDatabase(t.Context(), sqlDB, schema)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(sequenceChanged, rowChanged) {
		t.Fatal("database snapshot missed changed table contents")
	}

	t.Run("snapshot detects executable schema changes", func(t *testing.T) {
		functionName := pgx.Identifier{schema, "doctor_snapshot_trigger"}.Sanitize()
		triggerName := pgx.Identifier{"doctor_snapshot_trigger"}.Sanitize()
		if _, err := sqlDB.ExecContext(t.Context(), `CREATE FUNCTION `+functionName+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`); err != nil {
			t.Fatal(err)
		}
		if _, err := sqlDB.ExecContext(t.Context(), `CREATE TRIGGER `+triggerName+` BEFORE INSERT ON `+probeTable+` FOR EACH ROW EXECUTE FUNCTION `+functionName+`() `); err != nil {
			t.Fatal(err)
		}
		beforeExecutableChange, err := snapshotDoctorDatabase(t.Context(), sqlDB, schema)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sqlDB.ExecContext(t.Context(), `CREATE OR REPLACE FUNCTION `+functionName+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.value := upper(NEW.value); RETURN NEW; END $$`); err != nil {
			t.Fatal(err)
		}
		afterFunctionChange, err := snapshotDoctorDatabase(t.Context(), sqlDB, schema)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(beforeExecutableChange, afterFunctionChange) {
			t.Fatal("database snapshot missed a changed function definition")
		}

		if _, err := sqlDB.ExecContext(t.Context(), `ALTER TABLE `+probeTable+` ENABLE REPLICA TRIGGER `+triggerName); err != nil {
			t.Fatal(err)
		}
		afterStatusChange, err := snapshotDoctorDatabase(t.Context(), sqlDB, schema)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(afterFunctionChange, afterStatusChange) {
			t.Fatal("database snapshot missed a changed trigger status")
		}

		if _, err := sqlDB.ExecContext(t.Context(), `DROP TRIGGER `+triggerName+` ON `+probeTable); err != nil {
			t.Fatal(err)
		}
		if _, err := sqlDB.ExecContext(t.Context(), `CREATE TRIGGER `+triggerName+` BEFORE INSERT OR UPDATE ON `+probeTable+` FOR EACH ROW EXECUTE FUNCTION `+functionName+`() `); err != nil {
			t.Fatal(err)
		}
		// Reapply the prior status so only the trigger definition differs.
		if _, err := sqlDB.ExecContext(t.Context(), `ALTER TABLE `+probeTable+` ENABLE REPLICA TRIGGER `+triggerName); err != nil {
			t.Fatal(err)
		}
		afterDefinitionChange, err := snapshotDoctorDatabase(t.Context(), sqlDB, schema)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(afterStatusChange, afterDefinitionChange) {
			t.Fatal("database snapshot missed a changed trigger definition")
		}
	})
}
