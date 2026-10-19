package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/hypnguyen1209/V-ARTEX/agent"
	"github.com/hypnguyen1209/V-ARTEX/config"
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

func TestShutdownHTTPServerForceClosesStalledRequest(t *testing.T) {
	handlerStarted := make(chan struct{})
	handlerDone := make(chan struct{})
	httpSrv := &http.Server{Handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(handlerStarted)
		_, _ = io.Copy(io.Discard, r.Body)
		close(handlerDone)
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- httpSrv.Serve(listener) }()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "POST / HTTP/1.1\r\nHost: localhost\r\nContent-Length: 100\r\n\r\nx"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("partial-body request did not reach the handler")
	}

	shutdownCtx, cancel := context.WithCancel(context.Background())
	cancel()
	shutdownDone := make(chan struct{})
	go func() {
		shutdownHTTPServer(httpSrv, shutdownCtx)
		close(shutdownDone)
	}()
	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("HTTP shutdown remained blocked on the partial request body")
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("forced HTTP close did not release the stalled handler")
	}
	if err := <-serveDone; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Serve returned %v, want http.ErrServerClosed", err)
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
