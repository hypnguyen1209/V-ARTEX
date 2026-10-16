package server

import (
	"context"
	"testing"
	"time"
)

func lifecycleTestServer() *Server {
	ctx, cancel := context.WithCancelCause(context.Background())
	return &Server{ctx: ctx, cancel: cancel, engine: NewEngine(nil)}
}

func requireCloseBlocked(t *testing.T, closed <-chan struct{}, worker string) {
	t.Helper()
	select {
	case <-closed:
		t.Fatalf("Server.Close returned before its %s stopped", worker)
	case <-time.After(100 * time.Millisecond):
	}
}

func requireCloseReturns(t *testing.T, closed <-chan struct{}, worker string) {
	t.Helper()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatalf("Server.Close did not return after its %s stopped", worker)
	}
}

func TestServerCloseWaitsForTaskRoutines(t *testing.T) {
	s := lifecycleTestServer()
	cancelObserved := make(chan struct{})
	release := make(chan struct{})
	rt := s.engine.registerTaskRoutines(s.ctx, "shutdown-probe", 1)
	runTaskRoutine(rt, func(ctx context.Context) {
		<-ctx.Done()
		close(cancelObserved)
		<-release
	})

	closed := make(chan struct{})
	go func() {
		s.Close()
		close(closed)
	}()
	<-cancelObserved
	requireCloseBlocked(t, closed, "task runtime")
	close(release)
	requireCloseReturns(t, closed, "task runtime")
}

func TestServerCloseWaitsForStartupWorkers(t *testing.T) {
	s := lifecycleTestServer()
	cancelObserved := make(chan struct{})
	release := make(chan struct{})
	s.startBackground(func() {
		<-s.ctx.Done()
		close(cancelObserved)
		<-release
	})

	closed := make(chan struct{})
	go func() {
		s.Close()
		close(closed)
	}()
	<-cancelObserved
	requireCloseBlocked(t, closed, "startup worker")
	close(release)
	requireCloseReturns(t, closed, "startup worker")
}

func TestEngineCloseWaitsForAdmittedBootstrapOperation(t *testing.T) {
	e := NewEngine(nil)
	// startAdmittedTask's bootstrap goroutine uses this admission pair around goal decomposition.
	if !e.beginTaskOperation("bootstrap") {
		t.Fatal("bootstrap operation was not admitted")
	}
	release := make(chan struct{})
	go func() {
		<-release
		e.decInflight("bootstrap")
	}()

	closed := make(chan struct{})
	go func() {
		e.Close()
		close(closed)
	}()
	requireCloseBlocked(t, closed, "admitted bootstrap operation")
	if e.beginTaskOperation("late") {
		t.Fatal("task operation was admitted after Engine.Close started")
	}
	close(release)
	requireCloseReturns(t, closed, "admitted bootstrap operation")
}
