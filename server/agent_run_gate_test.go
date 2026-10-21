package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func waitGate(t *testing.T, gate *agentRunGate, active, waiting int) {
	t.Helper()
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		stats := gate.snapshot()
		if stats.Active == active && stats.Waiting == waiting {
			return
		}
		select {
		case <-ticker.C:
		case <-timeout.C:
			t.Fatalf("gate active/waiting = %d/%d, want %d/%d", stats.Active, stats.Waiting, active, waiting)
		}
	}
}

func acquireAsync(gate *agentRunGate, taskID string, kind agentRunKind) <-chan func() {
	result := make(chan func(), 1)
	go func() {
		release, err := gate.acquire(context.Background(), taskID, kind)
		if err == nil {
			result <- release
		}
	}()
	return result
}

func TestAgentRunGateReservesControlCapacity(t *testing.T) {
	gate := newAgentRunGate(true, 3)
	a, err := gate.acquire(context.Background(), "a", agentRunWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer a()
	b, err := gate.acquire(context.Background(), "b", agentRunWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer b()
	c := acquireAsync(gate, "c", agentRunWorker)
	waitGate(t, gate, 2, 1)
	control := acquireAsync(gate, "planner", agentRunControl)
	var releaseControl func()
	select {
	case releaseControl = <-control:
	case <-time.After(time.Second):
		t.Fatal("control did not acquire reserved capacity")
	}
	defer releaseControl()
	select {
	case release := <-c:
		release()
		t.Fatal("worker acquired reserved control capacity")
	default:
	}
	releaseControl()
	a()
	b()
	select {
	case release := <-c:
		release()
	case <-time.After(time.Second):
		t.Fatal("queued worker did not acquire")
	}
	waitGate(t, gate, 0, 0)
}

func TestAgentRunGateRoundRobinsWorkerTasks(t *testing.T) {
	gate := newAgentRunGate(true, 2)
	held, err := gate.acquire(context.Background(), "held", agentRunWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	a1 := acquireAsync(gate, "a", agentRunWorker)
	waitGate(t, gate, 1, 1)
	b1 := acquireAsync(gate, "b", agentRunWorker)
	waitGate(t, gate, 1, 2)
	a2 := acquireAsync(gate, "a", agentRunWorker)
	waitGate(t, gate, 1, 3)
	held()
	select {
	case release := <-a1:
		release()
	case <-time.After(time.Second):
		t.Fatal("first worker for task a did not acquire next")
	}
	select {
	case release := <-b1:
		select {
		case releaseA := <-a2:
			releaseA()
			release()
			t.Fatal("second worker for task a acquired before task b released")
		default:
		}
		release()
	case <-a2:
		t.Fatal("second worker for task a acquired before task b")
	case <-time.After(time.Second):
		t.Fatal("worker for task b did not acquire")
	}
	select {
	case release := <-a2:
		release()
	case <-time.After(time.Second):
		t.Fatal("second worker for task a did not acquire")
	}
	waitGate(t, gate, 0, 0)
}

func TestAgentRunGateLimitOnePrioritizesQueuedControl(t *testing.T) {
	gate := newAgentRunGate(true, 1)
	held, err := gate.acquire(context.Background(), "held", agentRunWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	worker := acquireAsync(gate, "worker", agentRunWorker)
	waitGate(t, gate, 1, 1)
	control := acquireAsync(gate, "planner", agentRunControl)
	waitGate(t, gate, 1, 2)
	held()
	select {
	case release := <-control:
		select {
		case releaseWorker := <-worker:
			releaseWorker()
			release()
			t.Fatal("worker acquired while control held the only permit")
		default:
		}
		release()
	case <-worker:
		t.Fatal("worker acquired before queued control")
	case <-time.After(time.Second):
		t.Fatal("queued control did not acquire")
	}
	select {
	case release := <-worker:
		release()
	case <-time.After(time.Second):
		t.Fatal("queued worker did not acquire after control released")
	}
	waitGate(t, gate, 0, 0)
}

func TestAgentRunGateCancellationRemovesWaiter(t *testing.T) {
	gate := newAgentRunGate(true, 1)
	held, err := gate.acquire(context.Background(), "held", agentRunControl)
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		release, err := gate.acquire(ctx, "worker", agentRunWorker)
		if err == nil {
			release()
		}
		result <- err
	}()
	waitGate(t, gate, 1, 1)
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("acquire error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled acquire did not return")
	}
	waitGate(t, gate, 1, 0)
	held()
	waitGate(t, gate, 0, 0)
}

func TestAgentRunGateLiveReconfigurationDoesNotCancelActive(t *testing.T) {
	gate := newAgentRunGate(false, 8)
	releases := make([]func(), 3)
	for i := range releases {
		release, err := gate.acquire(context.Background(), "held", agentRunControl)
		if err != nil {
			t.Fatal(err)
		}
		releases[i] = release
		defer release()
	}
	gate.configure(true, 1)
	next := acquireAsync(gate, "next", agentRunControl)
	waitGate(t, gate, 3, 1)
	releases[0]()
	releases[1]()
	waitGate(t, gate, 1, 1)
	select {
	case release := <-next:
		release()
		t.Fatal("next control acquired while active runs met the lowered limit")
	default:
	}
	releases[2]()
	select {
	case release := <-next:
		gate.configure(false, 8)
		release()
	case <-time.After(time.Second):
		t.Fatal("next control did not acquire after active runs released")
	}
	waitGate(t, gate, 0, 0)
}

func TestAgentRunGateDisableWakesAllWaiters(t *testing.T) {
	gate := newAgentRunGate(true, 1)
	held, err := gate.acquire(context.Background(), "held", agentRunControl)
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	a := acquireAsync(gate, "a", agentRunWorker)
	b := acquireAsync(gate, "b", agentRunWorker)
	waitGate(t, gate, 1, 2)
	gate.configure(false, 8)
	var releaseA, releaseB func()
	select {
	case releaseA = <-a:
	case <-time.After(time.Second):
		t.Fatal("worker a did not acquire after disabling gate")
	}
	defer releaseA()
	select {
	case releaseB = <-b:
	case <-time.After(time.Second):
		t.Fatal("worker b did not acquire after disabling gate")
	}
	defer releaseB()
	waitGate(t, gate, 3, 0)
	held()
	releaseA()
	releaseB()
	waitGate(t, gate, 0, 0)
}

func TestAgentRunReleaseIsIdempotent(t *testing.T) {
	gate := newAgentRunGate(true, 1)
	release, err := gate.acquire(context.Background(), "control", agentRunControl)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release()
		}()
	}
	wg.Wait()
	waitGate(t, gate, 0, 0)
}
