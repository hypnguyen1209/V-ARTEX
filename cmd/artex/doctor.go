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
		fmt.Fprintf(stdout, "V-ARTEX %s doctor (%s)\n", report.Version, report.Status)
		for _, check := range report.Checks {
			fmt.Fprintf(stdout, "%s %-28s %s", strings.ToUpper(string(check.Status)), check.Name, check.Detail)
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

func defaultDoctorDeps() doctorDeps {
	return doctorDeps{runChecks: func(context.Context, doctorOptions) []doctorResult { return nil }}
}
