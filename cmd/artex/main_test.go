package main

import (
	"context"
	"os"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/config"
)

func TestShutdownContextPreservesNamedCause(t *testing.T) {
	signalCtx, signalCancel := context.WithCancel(context.Background())
	ctx, shutdown := shutdownContext(signalCtx)
	defer shutdown(nil)
	signalCancel()
	<-ctx.Done()
	if code, _, _, ok := agent.AbortReason(ctx); !ok || code != "shutdown" {
		t.Fatalf("code=%q ok=%v, want shutdown", code, ok)
	}
}

func TestDefaultHTTPAddrIsLoopbackOnly(t *testing.T) {
	if defaultHTTPAddr != "127.0.0.1:8787" {
		t.Fatalf("default HTTP address = %q", defaultHTTPAddr)
	}
}

func TestJWTKeyDir(t *testing.T) {
	for _, tc := range []struct {
		name, env, want string
		unset           bool
	}{
		{name: "unset", want: config.BaseDir(), unset: true},
		{name: "empty", want: config.BaseDir()},
		{name: "whitespace", env: " \t\n", want: config.BaseDir()},
		{name: "override", env: "/app/auth", want: "/app/auth"},
		{name: "trimmed override", env: " \t/app/auth \n", want: "/app/auth"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ARTEX_JWT_KEY_DIR", tc.env)
			if tc.unset {
				if err := os.Unsetenv("ARTEX_JWT_KEY_DIR"); err != nil {
					t.Fatal(err)
				}
			}
			if got := jwtKeyDir(); got != tc.want {
				t.Fatalf("JWT key directory = %q, want %q", got, tc.want)
			}
		})
	}
}
