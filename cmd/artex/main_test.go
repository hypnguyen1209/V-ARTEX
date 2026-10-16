package main

import (
	"context"
	"testing"

	"github.com/Autumn-27/artex/agent"
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
