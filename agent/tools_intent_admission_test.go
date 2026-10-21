package agent

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestIntentBudgetCountsOnlySuccessfulCreations(t *testing.T) {
	b := newIntentBudget(4)
	finish, err := b.reserve(4)
	if err != nil {
		t.Fatal(err)
	}
	finish(2)
	if got := b.remaining(); got != 2 {
		t.Fatalf("remaining=%d, want 2", got)
	}
	if _, err := b.reserve(3); err == nil {
		t.Fatal("reservation larger than remaining capacity succeeded")
	}
}

func TestIntentBudgetConcurrentReservationsNeverExceedLimit(t *testing.T) {
	b := newIntentBudget(4)
	start := make(chan struct{})
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			finish, err := b.reserve(1)
			if err == nil {
				accepted.Add(1)
				finish(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := accepted.Load(); got != 4 {
		t.Fatalf("accepted=%d, want 4", got)
	}
}
