# Read-Only Doctor Command Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a deterministic `artex doctor` command that is secret-safe, makes no persistent changes, and never contacts an LLM unless explicitly requested.

**Architecture:** Route the subcommand before server startup. A dedicated runner executes ordered checks through injectable dependencies, uses one direct `database/sql` connection for read-only queries, and renders either human or stable JSON output.

**Tech Stack:** Go, `flag.FlagSet`, `database/sql` with pgx, `net.Listener`, `os/exec`, JSON, existing provider/profile types.

**Design:** `docs/superpowers/specs/2026-10-21-runtime-safety-delivery-foundation-design.md`

**Sequence:** Land before the CI plan so CI can exercise `doctor --json`.

---

## File Map

- Create `cmd/artex/doctor.go`: parsing, ordered checks, redaction, output, and exit codes.
- Create `cmd/artex/doctor_test.go`: output, dependency, no-live-call, and read-only tests.
- Modify `cmd/artex/main.go`: route the subcommand before normal startup.
- Modify `agent/provider.go` and `agent/provider_capture_test.go`: quiet live connectivity probe.
- Modify `README.md`: operator usage and billing warning.

### Task 1: Add the command boundary and stable output

**Files:**
- Create: `cmd/artex/doctor.go`
- Create: `cmd/artex/doctor_test.go`
- Modify: `cmd/artex/main.go:45`

- [ ] **Step 1: Write failing command tests**

Create `cmd/artex/doctor_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
)

func TestRunDoctorJSONHasStableShapeAndNoANSI(t *testing.T) {
	var out, errOut bytes.Buffer
	deps := doctorDeps{runChecks: func(context.Context, doctorOptions) []doctorResult {
		return []doctorResult{{Name: "fixture", Status: doctorOK, Detail: "ready", DurationMS: 3}}
	}}
	if code := runDoctor([]string{"--json"}, &out, &errOut, deps); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
	var report doctorReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out.String())
	}
	if report.Version != version || report.Status != doctorOK || report.LiveLLM || len(report.Checks) != 1 {
		t.Fatalf("report=%+v", report)
	}
	if bytes.Contains(out.Bytes(), []byte("\x1b[")) {
		t.Fatal("JSON output contains ANSI escapes")
	}
}

func TestRunDoctorExitCodes(t *testing.T) {
	tests := []struct {
		args []string
		checks []doctorResult
		want int
	}{
		{checks: []doctorResult{{Name: "warning", Status: doctorWarn}}, want: 0},
		{checks: []doctorResult{{Name: "failure", Status: doctorFail}}, want: 1},
		{args: []string{"--unknown"}, want: 2},
	}
	for _, test := range tests {
		deps := doctorDeps{runChecks: func(context.Context, doctorOptions) []doctorResult { return test.checks }}
		if got := runDoctor(test.args, io.Discard, io.Discard, deps); got != test.want {
			t.Fatalf("exit=%d, want %d", got, test.want)
		}
	}
}

func TestRunDoctorDefaultDisablesLiveLLM(t *testing.T) {
	var captured doctorOptions
	deps := doctorDeps{runChecks: func(_ context.Context, options doctorOptions) []doctorResult {
		captured = options
		return nil
	}}
	_ = runDoctor(nil, io.Discard, io.Discard, deps)
	if captured.LiveLLM {
		t.Fatal("default doctor enabled billable LLM checks")
	}
}
```

- [ ] **Step 2: Run tests and verify RED**

```bash
go test ./cmd/artex -run TestRunDoctor -count=1
```

Expected: build failure because the doctor types do not exist.

- [ ] **Step 3: Implement command types and rendering**

Create `cmd/artex/doctor.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/hypnguyen1209/V-ARTEX/config"
)

type doctorStatus string

const (
	doctorOK doctorStatus = "ok"
	doctorWarn doctorStatus = "warn"
	doctorFail doctorStatus = "fail"
)

type doctorResult struct {
	Name string `json:"name"`
	Status doctorStatus `json:"status"`
	Detail string `json:"detail"`
	Remediation string `json:"remediation"`
	DurationMS int64 `json:"duration_ms"`
}

type doctorReport struct {
	Version string `json:"version"`
	Status doctorStatus `json:"status"`
	LiveLLM bool `json:"live_llm"`
	Checks []doctorResult `json:"checks"`
}

type doctorOptions struct {
	Addr, DataDir, Proxy string
	JSON, LiveLLM bool
}

type doctorDeps struct {
	runChecks func(context.Context, doctorOptions) []doctorResult
}

func runDoctor(args []string, stdout, stderr io.Writer, deps doctorDeps) int {
	fs := flag.NewFlagSet("artex doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	options := doctorOptions{}
	fs.StringVar(&options.Addr, "addr", defaultHTTPAddr, "HTTP listen address")
	fs.StringVar(&options.DataDir, "data", filepath.Join(config.BaseDir(), "data"), "data directory")
	fs.StringVar(&options.Proxy, "proxy", "127.0.0.1:8788", "traffic recording proxy address")
	fs.BoolVar(&options.JSON, "json", false, "emit machine-readable JSON")
	fs.BoolVar(&options.LiveLLM, "live-llm", false, "run networked, potentially billable LLM checks")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected arguments: %s\n", strings.Join(fs.Args(), " "))
		return 2
	}
	if deps.runChecks == nil {
		deps = defaultDoctorDeps()
	}
	report := doctorReport{Version: version, Status: doctorOK, LiveLLM: options.LiveLLM, Checks: deps.runChecks(context.Background(), options)}
	for _, check := range report.Checks {
		if check.Status == doctorFail {
			report.Status = doctorFail
			break
		}
		if check.Status == doctorWarn {
			report.Status = doctorWarn
		}
	}
	if options.JSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(report)
	} else {
		fmt.Fprintf(stdout, "V-ARTEX %s doctor (%s)\n", report.Version, report.Status)
		for _, check := range report.Checks {
			fmt.Fprintf(stdout, "%-5s %-28s %s", strings.ToUpper(string(check.Status)), check.Name, check.Detail)
			if check.Remediation != "" {
				fmt.Fprintf(stdout, " — %s", check.Remediation)
			}
			fmt.Fprintln(stdout)
		}
	}
	if report.Status == doctorFail {
		return 1
	}
	return 0
}
```

- [ ] **Step 4: Route doctor before startup**

Rename the current `run` declaration to `runServer` without changing its body. Insert this new wrapper immediately before it:

```go
func run() int {
	if len(os.Args) > 1 && os.Args[1] == "doctor" {
		return runDoctor(os.Args[2:], os.Stdout, os.Stderr, defaultDoctorDeps())
	}
	return runServer()
}
```

The moved body begins with the current flag declarations. This places doctor before banner/log setup, self-update bootstrap, manager creation, migrations, JWT creation, engines, and HTTP listeners.

- [ ] **Step 5: Test and commit**

```bash
go test ./cmd/artex -run 'TestRunDoctor|TestDefaultHTTPAddr|TestJWTKeyDir' -count=1
git add cmd/artex/main.go cmd/artex/doctor.go cmd/artex/doctor_test.go
GIT_AUTHOR_NAME='hypnguyen1209' GIT_AUTHOR_EMAIL='hypnguyen1209@gmail.com' GIT_COMMITTER_NAME='hypnguyen1209' GIT_COMMITTER_EMAIL='hypnguyen1209@gmail.com' git commit -m 'feat(cli): add doctor command boundary'
```

Expected: tests PASS and the commit contains no runtime check implementation yet.

### Task 2: Implement local and read-only PostgreSQL checks

**Files:**
- Modify: `cmd/artex/doctor.go`
- Modify: `cmd/artex/doctor_test.go`

- [ ] **Step 1: Add failing dependency, port, and config tests**

Extend the test imports with `database/sql`, `errors`, `net`, `os`, `os/exec`, `path/filepath`, and `strings`. Append:

```go
func TestDoctorSkipsDependentDatabaseChecksAndRedactsDSN(t *testing.T) {
	t.Setenv("ARTEX_PG_DSN", "postgres://user:secret-pass@127.0.0.1:1/artex?sslmode=disable")
	results := runDefaultDoctorChecks(t.Context(), doctorOptions{
		Addr: "127.0.0.1:0", Proxy: "", DataDir: t.TempDir(),
	}, doctorRuntime{
		openSQL: func(string, string) (*sql.DB, error) { return nil, errors.New("dial failed") },
		lookPath: exec.LookPath, listen: net.Listen, stat: os.Stat, readFile: os.ReadFile,
		runCommand: func(context.Context, string, ...string) ([]byte, error) { return []byte("fixture version"), nil },
	})
	var connectFailed, schemaSkipped bool
	for _, result := range results {
		if strings.Contains(result.Detail+result.Remediation, "secret-pass") {
			t.Fatal("doctor leaked a DSN password")
		}
		connectFailed = connectFailed || result.Name == "postgres.connectivity" && result.Status == doctorFail
		schemaSkipped = schemaSkipped || result.Name == "postgres.schema" && result.Status == doctorWarn
	}
	if !connectFailed || !schemaSkipped {
		t.Fatalf("results=%+v", results)
	}
}

func TestDoctorOccupiedPortIsWarning(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if result := checkAddress("network.http", listener.Addr().String(), net.Listen); result.Status != doctorWarn {
		t.Fatalf("result=%+v", result)
	}
}

func TestDoctorConfigParseFailureIsFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARTEX_CONFIG", path)
	if result := checkConfigFile(os.ReadFile); result.Status != doctorFail {
		t.Fatalf("result=%+v", result)
	}
}
```

- [ ] **Step 2: Run tests and verify RED**

```bash
go test ./cmd/artex -run 'TestDoctorSkips|TestDoctorOccupied|TestDoctorConfig' -count=1
```

Expected: build failure for missing check functions.

- [ ] **Step 3: Add injected runtime and local checks**

Add imports for `database/sql`, `net`, `net/url`, `os`, `os/exec`, `time`, `agent`, `db`, and pgx registration. Add to `doctor.go`:

```go
type doctorRuntime struct {
	openSQL func(string, string) (*sql.DB, error)
	lookPath func(string) (string, error)
	runCommand func(context.Context, string, ...string) ([]byte, error)
	listen func(string, string) (net.Listener, error)
	stat func(string) (os.FileInfo, error)
	readFile func(string) ([]byte, error)
	liveLLM func(context.Context, agent.Config) (time.Duration, string, error)
}

func defaultDoctorDeps() doctorDeps {
	runtime := doctorRuntime{
		openSQL: sql.Open, lookPath: exec.LookPath, listen: net.Listen,
		stat: os.Stat, readFile: os.ReadFile, liveLLM: agent.TestConnectionQuiet,
		runCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
	}
	return doctorDeps{runChecks: func(ctx context.Context, options doctorOptions) []doctorResult {
		return runDefaultDoctorChecks(ctx, options, runtime)
	}}
}

func timedDoctorResult(name string, run func() (doctorStatus, string, string)) doctorResult {
	start := time.Now()
	status, detail, remediation := run()
	return doctorResult{Name: name, Status: status, Detail: detail, Remediation: remediation, DurationMS: time.Since(start).Milliseconds()}
}

func checkConfigFile(readFile func(string) ([]byte, error)) doctorResult {
	return timedDoctorResult("config.file", func() (doctorStatus, string, string) {
		path := config.Path()
		raw, err := readFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return doctorWarn, path + " is absent; environment-only configuration will be used", "Create config.json only when environment configuration is not intentional"
		}
		if err != nil {
			return doctorFail, "Cannot read " + path, err.Error()
		}
		var parsed config.Config
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return doctorFail, "Configuration JSON is invalid", err.Error()
		}
		return doctorOK, path + " is readable and valid JSON", ""
	})
}

func checkAddress(name, address string, listen func(string, string) (net.Listener, error)) doctorResult {
	return timedDoctorResult(name, func() (doctorStatus, string, string) {
		if strings.TrimSpace(address) == "" {
			return doctorOK, "disabled", ""
		}
		listener, err := listen("tcp", address)
		if err != nil {
			return doctorWarn, address + " is already in use or unavailable", "Identify the listener; it can be the running V-ARTEX instance"
		}
		_ = listener.Close()
		return doctorOK, address + " can be bound", ""
	})
}
```

Add these path/executable helpers. They only read metadata and version output; each executable probe has its own two-second deadline:

```go
func checkPath(name, path string, required bool, stat func(string) (os.FileInfo, error)) doctorResult {
	return timedDoctorResult(name, func() (doctorStatus, string, string) {
		info, err := stat(path)
		if err != nil {
			status := doctorWarn
			if required {
				status = doctorFail
			}
			return status, path + " is unavailable", err.Error()
		}
		return doctorOK, fmt.Sprintf("%s is readable metadata: mode=%s size=%d", path, info.Mode(), info.Size()), ""
	})
}

func checkExecutable(ctx context.Context, name string, candidates []string, runtime doctorRuntime) doctorResult {
	return timedDoctorResult(name, func() (doctorStatus, string, string) {
		for _, candidate := range candidates {
			path, err := runtime.lookPath(candidate)
			if err != nil {
				continue
			}
			probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			output, runErr := runtime.runCommand(probeCtx, path, "--version")
			cancel()
			if runErr != nil {
				return doctorWarn, path + " was found but its version probe failed", runErr.Error()
			}
			return doctorOK, path + ": " + strings.TrimSpace(string(output)), ""
		}
		return doctorWarn, strings.Join(candidates, " or ") + " was not found", "Install it only if the associated feature is required"
	})
}

func doctorSkillDir() string {
	if path := strings.TrimSpace(os.Getenv("ARTEX_SKILL_DIR")); path != "" {
		return path
	}
	if path := strings.TrimSpace(config.Load().SkillDir); path != "" {
		return path
	}
	return filepath.Join(config.BaseDir(), "skills")
}
```

Resolve the skill path from `ARTEX_SKILL_DIR`, then `config.Load().SkillDir`, then `BaseDir()/skills` without calling `config.SkillDir`, which creates the directory.

- [ ] **Step 4: Add the direct SQL session and schema/profile queries**

Add:

```go
type doctorSession struct {
	sql *sql.DB
	profiles []*db.LLMProfile
	secrets []string
}

func doctorSecrets(dsn string) []string {
	secrets := []string{dsn}
	if parsed, err := url.Parse(dsn); err == nil && parsed.User != nil {
		if password, ok := parsed.User.Password(); ok && password != "" {
			secrets = append(secrets, password)
		}
	}
	return secrets
}

func openDoctorDatabase(ctx context.Context, runtime doctorRuntime) (*doctorSession, doctorResult) {
	session := &doctorSession{}
	result := timedDoctorResult("postgres.connectivity", func() (doctorStatus, string, string) {
		dsn, source, err := db.DSN()
		if err != nil {
			return doctorFail, "PostgreSQL DSN is not configured", err.Error()
		}
		session.secrets = doctorSecrets(dsn)
		sqlDB, err := runtime.openSQL("pgx", dsn)
		if err != nil {
			return doctorFail, "Cannot create PostgreSQL client for " + config.Redact(dsn), "Check the DSN syntax"
		}
		session.sql = sqlDB
		sqlDB.SetMaxOpenConns(2)
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if err := sqlDB.PingContext(probeCtx); err != nil {
			_ = sqlDB.Close()
			session.sql = nil
			return doctorFail, "PostgreSQL connection failed via " + source, redactDoctorText(err.Error(), []string{dsn})
		}
		var one int
		if err := sqlDB.QueryRowContext(probeCtx, "SELECT 1").Scan(&one); err != nil || one != 1 {
			_ = sqlDB.Close()
			session.sql = nil
			return doctorFail, "PostgreSQL read-only probe failed", redactDoctorText(fmt.Sprint(err), []string{dsn})
		}
		return doctorOK, "PostgreSQL accepted SELECT 1 via " + source, ""
	})
	return session, result
}

func checkDoctorSchema(ctx context.Context, session *doctorSession) doctorResult {
	return timedDoctorResult("postgres.schema", func() (doctorStatus, string, string) {
		if session == nil || session.sql == nil {
			return doctorWarn, "Skipped because postgres.connectivity failed", "Restore PostgreSQL connectivity and rerun doctor"
		}
		for _, table := range []string{"settings", "tasks", "explorations", "exploration_nodes", "llm_profiles", "agents"} {
			var exists bool
			if err := session.sql.QueryRowContext(ctx, "SELECT to_regclass($1) IS NOT NULL", "public."+table).Scan(&exists); err != nil || !exists {
				return doctorFail, "Required table " + table + " is missing or unreadable", "Start the normal server once to apply migrations"
			}
		}
		var pgVersion string
		_ = session.sql.QueryRowContext(ctx, "SHOW server_version").Scan(&pgVersion)
		return doctorOK, "Required schema is readable on PostgreSQL " + pgVersion, ""
	})
}
```

Add the profile and redaction functions:

```go
func checkDoctorProfiles(ctx context.Context, session *doctorSession) doctorResult {
	return timedDoctorResult("llm.profiles", func() (doctorStatus, string, string) {
		if session == nil || session.sql == nil {
			return doctorWarn, "Skipped because postgres.connectivity failed", "Restore PostgreSQL connectivity and rerun doctor"
		}
		profiles, err := (&db.DB{DB: session.sql}).ListProfiles()
		if err != nil {
			return doctorFail, "Stored LLM profiles are unreadable", err.Error()
		}
		if len(profiles) == 0 {
			return doctorWarn, "No LLM profiles are configured", "Create a provider profile before starting agent work"
		}
		allowed := map[string]bool{"anthropic": true, "openai": true, "openai-responses": true}
		keyed := 0
		for _, listed := range profiles {
			profile, err := (&db.DB{DB: session.sql}).ProfileByID(listed.ID)
			if err != nil || profile == nil {
				return doctorFail, fmt.Sprintf("Profile %d is unreadable", listed.ID), fmt.Sprint(err)
			}
			if strings.TrimSpace(profile.Name) == "" || strings.TrimSpace(profile.Model) == "" || !allowed[profile.Format] {
				return doctorFail, fmt.Sprintf("Profile %d has invalid name, model, or format", profile.ID), "Use anthropic, openai, or openai-responses with a non-empty name and model"
			}
			if profile.APIKey != "" {
				keyed++
				session.secrets = append(session.secrets, profile.APIKey)
			}
			session.profiles = append(session.profiles, profile)
		}
		if keyed == 0 {
			return doctorWarn, fmt.Sprintf("%d valid profile(s), none with a stored API key", len(profiles)), "Store a key before starting agent work"
		}
		return doctorOK, fmt.Sprintf("%d valid profile(s), %d with stored API keys", len(profiles), keyed), ""
	})
}

func redactDoctorText(text string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	return text
}
```

Pass all result details and remediation text through `redactDoctorText` after the profile check has collected every API key. The raw DSN and parsed password are inserted into `session.secrets` before the connection attempt, so connection errors are safe too.

- [ ] **Step 5: Assemble the fixed check order**

Implement `runDefaultDoctorChecks` in this order:

```go
results := []doctorResult{
	checkConfigFile(runtime.readFile),
}
session, connection := openDoctorDatabase(ctx, runtime)
results = append(results, connection, checkDoctorSchema(ctx, session))
results = append(results,
	checkPath("path.data", options.DataDir, true, runtime.stat),
	checkPath("path.workspace", filepath.Join(options.DataDir, "tasks"), false, runtime.stat),
	checkPath("path.skills", doctorSkillDir(), true, runtime.stat),
	checkPath("path.jwt_key", filepath.Join(jwtKeyDir(), "jwt.key"), false, runtime.stat),
	checkAddress("network.http", options.Addr, runtime.listen),
	checkAddress("network.proxy", options.Proxy, runtime.listen),
	checkExecutable(ctx, "runtime.python", []string{"python3", "python"}, runtime),
	checkExecutable(ctx, "runtime.node", []string{"node"}, runtime),
	checkExecutable(ctx, "runtime.npm", []string{"npm"}, runtime),
	checkExecutable(ctx, "runtime.npx", []string{"npx"}, runtime),
	checkExecutable(ctx, "runtime.playwright", []string{filepath.Join(config.BaseDir(), "web", "node_modules", ".bin", "playwright"), "playwright"}, runtime),
	checkDoctorProfiles(ctx, session),
)
if options.LiveLLM {
	results = append(results, checkLiveLLMProfiles(ctx, session, runtime))
}
```

Close `session.sql` with `defer` and redact every detail/remediation after all checks. The doctor path must not call `db.Open`, `os.MkdirAll`, any migration, or any settings mutation.

- [ ] **Step 6: Test and commit**

```bash
gofmt -w cmd/artex/doctor.go cmd/artex/doctor_test.go
go test ./cmd/artex -run 'TestDoctor|TestRunDoctor' -count=1
git add cmd/artex/doctor.go cmd/artex/doctor_test.go
GIT_AUTHOR_NAME='hypnguyen1209' GIT_AUTHOR_EMAIL='hypnguyen1209@gmail.com' GIT_COMMITTER_NAME='hypnguyen1209' GIT_COMMITTER_EMAIL='hypnguyen1209@gmail.com' git commit -m 'feat(doctor): add read-only diagnostics'
```

Expected: tests PASS.

### Task 3: Add explicit quiet live LLM probes

**Files:**
- Modify: `agent/provider.go:457`
- Modify: `agent/provider_capture_test.go`
- Modify: `cmd/artex/doctor.go`
- Modify: `cmd/artex/doctor_test.go`

- [ ] **Step 1: Add a failing quiet-provider test**

Extend the imports in `agent/provider_capture_test.go` with `bytes`, `context`, `log`, and `github.com/Autumn-27/norma/llm`. Append:

```go
func TestConnectionQuietDoesNotLogWireData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{"id":"fixture","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()

	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)

	_, reply, err := TestConnectionQuiet(context.Background(), Config{
		Format: llm.FormatOpenAI, BaseURL: srv.URL, APIKey: "secret-key", Model: "fixture", Stream: false,
	})
	if err != nil || strings.TrimSpace(reply) != "OK" {
		t.Fatalf("reply=%q err=%v", reply, err)
	}
	if strings.Contains(logs.String(), "choices") || strings.Contains(logs.String(), "secret-key") {
		t.Fatalf("quiet probe logged wire data: %s", logs.String())
	}
}
```

- [ ] **Step 2: Run the test and verify RED**

```bash
go test ./agent -run TestConnectionQuiet -count=1
```

Expected: build failure because `TestConnectionQuiet` does not exist.

- [ ] **Step 3: Refactor the provider probe**

Replace the current exported function with two wrappers over one helper:

```go
func TestConnection(ctx context.Context, c Config) (time.Duration, string, error) {
	return testConnection(ctx, c, true)
}

func TestConnectionQuiet(ctx context.Context, c Config) (time.Duration, string, error) {
	return testConnection(ctx, c, false)
}
```

Move the current implementation into `testConnection(ctx, c, logWire bool)` unchanged except for the capture block:

```go
if logWire {
	var capture *llmrec.Capture
	ctx, capture = llmrec.NewCapture(ctx)
	defer logTestConnection(c, capture)
}
```

The quiet path still uses one 30-second context, one `agentcore.Run` turn, the profile transport mode, and the current connection-test prompt; it only suppresses raw request/response logging.

- [ ] **Step 4: Implement the opt-in profile loop**

Add:

```go
func checkLiveLLMProfiles(ctx context.Context, session *doctorSession, runtime doctorRuntime) doctorResult {
	return timedDoctorResult("llm.live", func() (doctorStatus, string, string) {
		if session == nil || session.sql == nil {
			return doctorWarn, "Skipped because postgres.connectivity failed", "Restore PostgreSQL connectivity and rerun with --live-llm"
		}
		tested := 0
		for _, profile := range session.profiles {
			if profile.APIKey == "" {
				continue
			}
			cfg := agent.ConfigFrom(profile.Format, profile.Model, profile.BaseURL, profile.APIKey, profile.Proxy)
			cfg.ThinkingType, cfg.ReasoningEffort = profile.ThinkingType, profile.ReasoningEffort
			cfg.Stream = profile.Streaming
			cfg.MaxTokens, cfg.MaxTokensField = profile.MaxTokens, profile.MaxTokensField
			cfg.SessionHeaderKey = profile.SessionHeaderKey
			probeCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
			_, _, err := runtime.liveLLM(probeCtx, cfg)
			cancel()
			if err != nil {
				return doctorFail, fmt.Sprintf("Profile %q failed its live probe: %s", profile.Name, err), "Verify endpoint, model, key, proxy, and account quota"
			}
			tested++
		}
		if tested == 0 {
			return doctorWarn, "No keyed profile was available for a live probe", "Store an API key or omit --live-llm"
		}
		return doctorOK, fmt.Sprintf("%d keyed profile(s) completed a live, potentially billable probe", tested), ""
	})
}
```

- [ ] **Step 5: Add and run the no-default-live-call test**

Extend the `cmd/artex/doctor_test.go` imports with `time` and `agent`, then append:

```go
func TestDefaultDoctorNeverCallsLLM(t *testing.T) {
	t.Setenv("ARTEX_PG_DSN", "postgres://user:secret@127.0.0.1:1/artex?sslmode=disable")
	calls := 0
	runtime := doctorRuntime{
		openSQL: func(string, string) (*sql.DB, error) { return nil, errors.New("fixture connection failure") },
		lookPath: func(name string) (string, error) { return name, nil },
		runCommand: func(context.Context, string, ...string) ([]byte, error) { return []byte("fixture version"), nil },
		listen: net.Listen, stat: os.Stat, readFile: os.ReadFile,
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
```

Run:

```bash
go test ./agent -run 'TestConnectionQuiet|TestCapturePropagates' -count=1
go test ./cmd/artex -run 'TestDoctor|TestRunDoctor|TestDefaultChecks' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit live checks**

```bash
git add agent/provider.go agent/provider_capture_test.go cmd/artex/doctor.go cmd/artex/doctor_test.go
GIT_AUTHOR_NAME='hypnguyen1209' GIT_AUTHOR_EMAIL='hypnguyen1209@gmail.com' GIT_COMMITTER_NAME='hypnguyen1209' GIT_COMMITTER_EMAIL='hypnguyen1209@gmail.com' git commit -m 'feat(doctor): add opt-in live provider checks'
```

### Task 4: Prove read-only behavior and document use

**Files:**
- Modify: `cmd/artex/doctor_test.go`
- Modify: `README.md`

- [ ] **Step 1: Add a database/filesystem immutability test**

Add `db` to the `cmd/artex/doctor_test.go` imports, then append this integration test. The advisory lock serializes the count snapshot with the existing `db`, `agent`, and `server` suites that share the same PostgreSQL database:

```go
func TestDoctorDoesNotPersistDatabaseOrFilesystemState(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("test PostgreSQL is not configured")
	}
	migrated, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	_ = migrated.Close()
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	lockConn, err := sqlDB.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lockConn.Close()
	if _, err := lockConn.ExecContext(t.Context(), `SELECT pg_advisory_lock(7337741002)`); err != nil {
		t.Fatal(err)
	}
	defer lockConn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(7337741002)`) //nolint:errcheck

	count := func(table string) int64 {
		t.Helper()
		var value int64
		if err := sqlDB.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	dataDir := t.TempDir()
	before, err := os.Stat(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	beforeSettings, beforeTasks := count("settings"), count("tasks")
	var output bytes.Buffer
	code := runDoctor([]string{"--json", "--addr", "127.0.0.1:0", "--proxy", "", "--data", dataDir}, &output, io.Discard, defaultDoctorDeps())
	if code == 2 {
		t.Fatalf("doctor rejected valid arguments: %s", output.String())
	}
	var report doctorReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("doctor emitted invalid JSON: %v\n%s", err, output.String())
	}
	after, err := os.Stat(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	afterSettings, afterTasks := count("settings"), count("tasks")
	if beforeSettings != afterSettings || beforeTasks != afterTasks || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("doctor mutated state: settings %d/%d tasks %d/%d mtime %v/%v",
			beforeSettings, afterSettings, beforeTasks, afterTasks, before.ModTime(), after.ModTime())
	}
}
```

- [ ] **Step 2: Run the integration test and manual JSON probe**

```bash
go test ./cmd/artex -run TestDoctorDoesNotPersistDatabaseOrFilesystemState -count=1
ARTEX_PG_DSN="${ARTEX_PG_DSN}" go run ./cmd/artex doctor --json --addr 127.0.0.1:0 --proxy ''
```

Expected: test PASS; output is JSON and contains no secrets or ANSI escape.

- [ ] **Step 3: Document operator behavior**

Add `Diagnose an installation` to `README.md` with these exact facts and examples:

- `./artex doctor` produces human-readable output.
- `./artex doctor --json` produces stable JSON.
- Default mode does not migrate, create keys/directories, change settings, or contact providers.
- Exit codes are 0 for no failures, 1 for failed checks, and 2 for invalid usage.
- `./artex doctor --live-llm` is networked, tests every profile with a stored key, and may create provider charges.

- [ ] **Step 4: Run final plan verification and commit**

```bash
gofmt -w cmd/artex/doctor.go cmd/artex/doctor_test.go agent/provider.go agent/provider_capture_test.go
go test ./cmd/artex ./agent -count=1
go vet ./cmd/artex ./agent
git add README.md cmd/artex/doctor_test.go
GIT_AUTHOR_NAME='hypnguyen1209' GIT_AUTHOR_EMAIL='hypnguyen1209@gmail.com' GIT_COMMITTER_NAME='hypnguyen1209' GIT_COMMITTER_EMAIL='hypnguyen1209@gmail.com' git commit -m 'docs: document read-only doctor workflow'
```

Expected: all commands PASS.
