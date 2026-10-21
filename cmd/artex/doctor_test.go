package main

import (
	"bytes"
	"context"
	"encoding/json"
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
