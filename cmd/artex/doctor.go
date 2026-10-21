package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/hypnguyen1209/V-ARTEX/agent"
	"github.com/hypnguyen1209/V-ARTEX/config"
	"github.com/hypnguyen1209/V-ARTEX/db"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type doctorStatus string

const (
	doctorOK   doctorStatus = "ok"
	doctorWarn doctorStatus = "warn"
	doctorFail doctorStatus = "fail"
)

type doctorResult struct {
	Name        string       `json:"name"`
	Status      doctorStatus `json:"status"`
	Detail      string       `json:"detail"`
	Remediation string       `json:"remediation"`
	DurationMS  int64        `json:"duration_ms"`
}

type doctorReport struct {
	Version string         `json:"version"`
	Status  doctorStatus   `json:"status"`
	LiveLLM bool           `json:"live_llm"`
	Checks  []doctorResult `json:"checks"`
}

type doctorOptions struct {
	Addr, DataDir, Proxy string
	JSON, LiveLLM        bool
}

type doctorDeps struct {
	runChecks func(context.Context, doctorOptions) []doctorResult
}

func runDoctor(args []string, stdout, stderr io.Writer, deps doctorDeps) int {
	var opts doctorOptions
	flags := flag.NewFlagSet("artex doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.Addr, "addr", defaultHTTPAddr, "HTTP listen address to check")
	flags.StringVar(&opts.DataDir, "data", filepath.Join(config.BaseDir(), "data"), "data directory for SQLite stores")
	flags.StringVar(&opts.Proxy, "proxy", "127.0.0.1:8788", "traffic recording proxy address to check (empty to disable)")
	flags.BoolVar(&opts.JSON, "json", false, "print a JSON report")
	flags.BoolVar(&opts.LiveLLM, "live-llm", false, "run networked, potentially billable LLM checks")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected arguments: %s\n", strings.Join(flags.Args(), " "))
		return 2
	}
	if deps.runChecks == nil {
		deps = defaultDoctorDeps()
	}
	report := doctorReport{
		Version: version,
		Status:  doctorOK,
		LiveLLM: opts.LiveLLM,
		Checks:  deps.runChecks(context.Background(), opts),
	}
	for _, check := range report.Checks {
		if check.Status == doctorFail {
			report.Status = doctorFail
		} else if check.Status == doctorWarn && report.Status != doctorFail {
			report.Status = doctorWarn
		}
	}
	if opts.JSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fmt.Fprintf(stderr, "write doctor report: %v\n", err)
			return 1
		}
	} else {
		var output strings.Builder
		fmt.Fprintf(&output, "V-ARTEX %s doctor (%s)\n", report.Version, report.Status)
		for _, check := range report.Checks {
			fmt.Fprintf(&output, "%-5s %-28s %s", strings.ToUpper(string(check.Status)), check.Name, check.Detail)
			if check.Remediation != "" {
				fmt.Fprintf(&output, " — %s", check.Remediation)
			}
			fmt.Fprintln(&output)
		}
		if _, err := io.WriteString(stdout, output.String()); err != nil {
			fmt.Fprintf(stderr, "write doctor report: %v\n", err)
			return 1
		}
	}
	if report.Status == doctorFail {
		return 1
	}
	return 0
}

func defaultDoctorDeps() doctorDeps {
	runtime := doctorRuntime{
		openSQL: sql.Open, lookPath: exec.LookPath, listen: net.Listen,
		stat: os.Stat, readFile: os.ReadFile,
		runCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			command := exec.CommandContext(ctx, name, args...)
			command.WaitDelay = 250 * time.Millisecond
			return command.CombinedOutput()
		},
	}
	return doctorDeps{runChecks: func(ctx context.Context, opts doctorOptions) []doctorResult {
		return runDefaultDoctorChecks(ctx, opts, runtime)
	}}
}

type doctorRuntime struct {
	openSQL    func(string, string) (*sql.DB, error)
	lookPath   func(string) (string, error)
	runCommand func(context.Context, string, ...string) ([]byte, error)
	listen     func(string, string) (net.Listener, error)
	stat       func(string) (os.FileInfo, error)
	readFile   func(string) ([]byte, error)
	liveLLM    func(context.Context, agent.Config) (time.Duration, string, error)
}

func timedDoctorResult(name string, check func() doctorResult) doctorResult {
	start := time.Now()
	result := check()
	result.Name = name
	result.DurationMS = time.Since(start).Milliseconds()
	return result
}

func checkConfigFile(readFile func(string) ([]byte, error)) doctorResult {
	return timedDoctorResult("config.file", func() doctorResult {
		path := config.Path()
		data, err := readFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return doctorResult{Status: doctorWarn, Detail: "Config file absent; using environment-only configuration", Remediation: "Set ARTEX_PG_DSN or create " + path}
		}
		if err != nil {
			return doctorResult{Status: doctorFail, Detail: "Cannot read config file: " + path, Remediation: "Check the config path and read permissions"}
		}
		var cfg config.Config
		if err := json.Unmarshal(data, &cfg); err != nil {
			return doctorResult{Status: doctorFail, Detail: "Invalid JSON configuration: " + path, Remediation: "Correct the config JSON and field types"}
		}
		return doctorResult{Status: doctorOK, Detail: "Valid configuration: " + path}
	})
}

func checkAddress(name, address string, listen func(string, string) (net.Listener, error)) doctorResult {
	return timedDoctorResult(name, func() doctorResult {
		if strings.TrimSpace(address) == "" {
			return doctorResult{Status: doctorOK, Detail: "Disabled"}
		}
		listener, err := listen("tcp", address)
		if err != nil {
			return doctorResult{Status: doctorWarn, Detail: address + " is already in use or unavailable", Remediation: "It may be a running V-ARTEX instance; check the address or choose another port"}
		}
		_ = listener.Close()
		return doctorResult{Status: doctorOK, Detail: address + " is available"}
	})
}

func checkPath(name, path string, required bool, stat func(string) (os.FileInfo, error)) doctorResult {
	return timedDoctorResult(name, func() doctorResult {
		info, err := stat(path)
		if err != nil {
			status := doctorWarn
			if required {
				status = doctorFail
			}
			return doctorResult{Status: status, Detail: "Path unavailable: " + path, Remediation: "Check that the path exists and is accessible"}
		}
		return doctorResult{Status: doctorOK, Detail: fmt.Sprintf("%s (mode %s, size %d bytes)", path, info.Mode(), info.Size())}
	})
}

func checkExecutable(ctx context.Context, name string, candidates []string, runtime doctorRuntime) doctorResult {
	return timedDoctorResult(name, func() doctorResult {
		for _, candidate := range candidates {
			path, err := runtime.lookPath(candidate)
			if err != nil {
				continue
			}
			probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			output, err := runtime.runCommand(probeCtx, path, "--version")
			cancel()
			if err != nil {
				return doctorResult{Status: doctorWarn, Detail: path + " version probe failed", Remediation: "Check that the executable runs and reports its version"}
			}
			return doctorResult{Status: doctorOK, Detail: path + ": " + strings.TrimSpace(string(output))}
		}
		return doctorResult{Status: doctorWarn, Detail: "Executable not found: " + strings.Join(candidates, ", "), Remediation: "Install it if needed by your workflows"}
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

type doctorSession struct {
	sql      *sql.DB
	profiles []*db.LLMProfile
	secrets  []string
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
	result := timedDoctorResult("postgres.connectivity", func() doctorResult {
		dsn, source, err := db.DSN()
		if err != nil {
			return doctorResult{Status: doctorFail, Detail: "PostgreSQL configuration is missing", Remediation: "Set ARTEX_PG_DSN or the database configuration"}
		}
		session.secrets = doctorSecrets(dsn)
		session.sql, err = runtime.openSQL("pgx", dsn)
		if err != nil {
			return doctorResult{Status: doctorFail, Detail: "Cannot open PostgreSQL", Remediation: "Check the PostgreSQL connection string syntax"}
		}
		session.sql.SetMaxOpenConns(2)
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		err = session.sql.PingContext(probeCtx)
		if err == nil {
			var one int
			err = session.sql.QueryRowContext(probeCtx, "SELECT 1").Scan(&one)
		}
		if err != nil {
			_ = session.sql.Close()
			session.sql = nil
			return doctorResult{Status: doctorFail, Detail: "PostgreSQL connectivity probe failed", Remediation: "Check server availability, credentials, and connection permissions"}
		}
		return doctorResult{Status: doctorOK, Detail: "Connected using " + source}
	})
	return session, result
}

func checkDoctorSchema(ctx context.Context, session *doctorSession) doctorResult {
	return timedDoctorResult("postgres.schema", func() doctorResult {
		if session == nil || session.sql == nil {
			return doctorResult{Status: doctorWarn, Detail: "Skipped due to PostgreSQL connectivity failure", Remediation: "Resolve postgres.connectivity first"}
		}
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		for _, table := range []string{"settings", "tasks", "explorations", "exploration_nodes", "llm_profiles", "agents"} {
			var relation sql.NullString
			if err := session.sql.QueryRowContext(probeCtx, "SELECT to_regclass($1)", table).Scan(&relation); err != nil || !relation.Valid {
				return doctorResult{Status: doctorFail, Detail: "Required table missing or unreadable: " + table, Remediation: "Run the normal V-ARTEX startup migration with an authorized database account"}
			}
		}
		detail := "Required tables are present"
		var version string
		if err := session.sql.QueryRowContext(probeCtx, "SHOW server_version").Scan(&version); err == nil {
			detail += "; PostgreSQL " + version
		}
		return doctorResult{Status: doctorOK, Detail: detail}
	})
}

func checkDoctorProfiles(ctx context.Context, session *doctorSession) doctorResult {
	return timedDoctorResult("llm.profiles", func() doctorResult {
		if session == nil || session.sql == nil {
			return doctorResult{Status: doctorWarn, Detail: "Skipped due to PostgreSQL connectivity failure", Remediation: "Resolve postgres.connectivity first"}
		}
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		rows, err := session.sql.QueryContext(probeCtx, `SELECT id,name,format,COALESCE(base_url,''),COALESCE(proxy,''),model,
COALESCE(api_key,''),COALESCE(thinking_type,''),COALESCE(reasoning_effort,''),COALESCE(streaming,true),
COALESCE(max_tokens,0),COALESCE(max_tokens_field,''),COALESCE(session_header_key,'') FROM llm_profiles ORDER BY id`)
		if err != nil {
			return doctorResult{Status: doctorFail, Detail: "Cannot read LLM profiles", Remediation: "Check the schema and database read permissions"}
		}
		defer rows.Close()
		keyed, invalid := 0, 0
		for rows.Next() {
			profile := &db.LLMProfile{}
			if err := rows.Scan(&profile.ID, &profile.Name, &profile.Format, &profile.BaseURL, &profile.Proxy, &profile.Model,
				&profile.APIKey, &profile.ThinkingType, &profile.ReasoningEffort, &profile.Streaming,
				&profile.MaxTokens, &profile.MaxTokensField, &profile.SessionHeaderKey); err != nil {
				return doctorResult{Status: doctorFail, Detail: "Cannot read an LLM profile", Remediation: "Check the schema and database read permissions"}
			}
			session.profiles = append(session.profiles, profile)
			session.secrets = append(session.secrets, profile.APIKey)
			if strings.TrimSpace(profile.APIKey) != "" {
				keyed++
			}
			validFormat := profile.Format == "anthropic" || profile.Format == "openai" || profile.Format == "openai-responses"
			if strings.TrimSpace(profile.Name) == "" || strings.TrimSpace(profile.Model) == "" || !validFormat {
				invalid++
			}
		}
		if err := rows.Err(); err != nil {
			return doctorResult{Status: doctorFail, Detail: "Cannot read LLM profiles", Remediation: "Check the schema and database read permissions"}
		}
		if len(session.profiles) == 0 {
			return doctorResult{Status: doctorWarn, Detail: "No LLM profiles configured", Remediation: "Configure an LLM profile in Settings"}
		}
		if invalid != 0 {
			return doctorResult{Status: doctorFail, Detail: fmt.Sprintf("%d LLM profiles have invalid configuration", invalid), Remediation: "Set each profile's name, model, and format (anthropic, openai, or openai-responses)"}
		}
		detail := fmt.Sprintf("%d LLM profiles configured; %d with API keys", len(session.profiles), keyed)
		if keyed == 0 {
			return doctorResult{Status: doctorWarn, Detail: detail, Remediation: "Add an API key to an LLM profile if required by the provider"}
		}
		return doctorResult{Status: doctorOK, Detail: detail}
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

func runDefaultDoctorChecks(ctx context.Context, opts doctorOptions, runtime doctorRuntime) []doctorResult {
	results := []doctorResult{checkConfigFile(runtime.readFile)}
	session, connectivity := openDoctorDatabase(ctx, runtime)
	if session.sql != nil {
		defer session.sql.Close()
	}
	results = append(results,
		connectivity,
		checkDoctorSchema(ctx, session),
		checkPath("path.data", opts.DataDir, true, runtime.stat),
		checkPath("path.workspace", filepath.Join(opts.DataDir, "tasks"), false, runtime.stat),
		checkPath("path.skills", doctorSkillDir(), true, runtime.stat),
		checkPath("path.jwt_key", filepath.Join(jwtKeyDir(), "jwt.key"), false, runtime.stat),
		checkAddress("network.http", opts.Addr, runtime.listen),
		checkAddress("network.proxy", opts.Proxy, runtime.listen),
		checkExecutable(ctx, "runtime.python", []string{"python3", "python"}, runtime),
		checkExecutable(ctx, "runtime.node", []string{"node"}, runtime),
		checkExecutable(ctx, "runtime.npm", []string{"npm"}, runtime),
		checkExecutable(ctx, "runtime.npx", []string{"npx"}, runtime),
		checkExecutable(ctx, "runtime.playwright", []string{filepath.Join(config.BaseDir(), "web", "node_modules", ".bin", "playwright"), "playwright"}, runtime),
		checkDoctorProfiles(ctx, session),
	)
	if opts.LiveLLM {
		results = append(results, checkLiveLLMProfiles(ctx, session, runtime))
	}
	for i := range results {
		results[i].Detail = redactDoctorText(results[i].Detail, session.secrets)
		results[i].Remediation = redactDoctorText(results[i].Remediation, session.secrets)
	}
	return results
}

func checkLiveLLMProfiles(context.Context, *doctorSession, doctorRuntime) doctorResult {
	return doctorResult{
		Name: "llm.live", Status: doctorWarn,
		Detail:      "Live LLM diagnostics are not available in this build",
		Remediation: "Omit --live-llm",
	}
}
