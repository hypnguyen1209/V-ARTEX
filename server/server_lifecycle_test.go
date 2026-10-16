package server

import (
	"context"
	"testing"
	"time"
)

func TestServerCloseWaitsForTaskRoutines(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()

	td := t.TempDir()
	s := New(context.Background(), m, td, td, td)
	cancelObserved := make(chan struct{})
	release := make(chan struct{})
	stopped := make(chan struct{})
	rt := s.engine.registerTaskRoutines(s.ctx, "shutdown-probe", 1)
	runTaskRoutine(rt, func(ctx context.Context) {
		<-ctx.Done()
		close(cancelObserved)
		<-release
		close(stopped)
	})

	closed := make(chan struct{})
	go func() {
		s.Close()
		close(closed)
	}()
	<-cancelObserved
	returnedEarly := false
	select {
	case <-closed:
		returnedEarly = true
	default:
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Server.Close did not return after its task runtime stopped")
	}
	if returnedEarly {
		t.Fatal("Server.Close returned before its task runtime stopped")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("task runtime did not stop before Server.Close returned")
	}
}

func TestServerCloseWaitsForStartupWorkers(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()

	td := t.TempDir()
	s := New(context.Background(), m, td, td, td)
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
	returnedEarly := false
	select {
	case <-closed:
		returnedEarly = true
	default:
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Server.Close did not return after its startup worker stopped")
	}
	if returnedEarly {
		t.Fatal("Server.Close returned before its startup worker stopped")
	}
}
