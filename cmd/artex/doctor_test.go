package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
)

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
