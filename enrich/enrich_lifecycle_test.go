package enrich

import "testing"

func TestEnqueueAfterCloseIsRejected(t *testing.T) {
	engine := &Engine{
		jobs:   make(chan job, 1),
		closed: make(chan struct{}),
		done:   make(chan struct{}),
	}
	engine.Close()

	engine.ResolveDomain(1, "queued.example.test")
	if got := len(engine.jobs); got != 0 {
		t.Fatalf("jobs queued after close=%d, want 0", got)
	}
}
