package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagerCloseStopsEnrichmentWorkers(t *testing.T) {
	const activeWorkers = 4
	const totalRequests = 32
	requestStarted := make(chan struct{}, totalRequests)
	releaseRequest := make(chan struct{})
	var releaseOnce sync.Once
	var startedRequests atomic.Int32
	httpServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		startedRequests.Add(1)
		requestStarted <- struct{}{}
		<-releaseRequest
		panic(http.ErrAbortHandler)
	}))
	var manager *Manager
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseRequest) })
		if manager != nil {
			_ = manager.Close()
		}
		httpServer.Close()
	})

	var err error
	manager, err = NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	enrichment := manager.enrich
	for i := 0; i < totalRequests; i++ {
		manager.enrich.ProbeSite(int64(i+1), fmt.Sprintf("%s/%d", httpServer.URL, i))
	}
	for i := 0; i < activeWorkers; i++ {
		select {
		case <-requestStarted:
		case <-time.After(2 * time.Second):
			t.Fatalf("enrichment workers started %d requests, want %d", i, activeWorkers)
		}
	}

	closeResult := make(chan error, 1)
	go func() { closeResult <- manager.Close() }()
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("manager close: %v", err)
		}
		t.Fatal("manager close returned before its enrichment worker stopped")
	case <-time.After(100 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(releaseRequest) })
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("manager close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("manager close did not return after enrichment worker stopped")
	}
	select {
	case <-enrichment.Done():
	default:
		t.Fatal("manager close returned before enrichment engine reported stopped")
	}
	if got := startedRequests.Load(); got != activeWorkers {
		t.Fatalf("requests started during shutdown=%d, want only %d already active requests", got, activeWorkers)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("second manager close: %v", err)
	}
}

func TestTaskLifecycleSnapshotCopiesContextSlices(t *testing.T) {
	task := &Task{
		Status:        "running",
		SourceTaskIDs: []int64{11, 12},
		CompanyIDs:    []int64{21, 22},
	}

	snapshot := task.lifecycleSnapshot()
	snapshot.SourceTaskIDs[0] = 99
	snapshot.CompanyIDs[0] = 98

	fresh := task.lifecycleSnapshot()
	if got := fresh.SourceTaskIDs[0]; got != 11 {
		t.Fatalf("snapshot source IDs alias task storage: got %d", got)
	}
	if got := fresh.CompanyIDs[0]; got != 21 {
		t.Fatalf("snapshot company IDs alias task storage: got %d", got)
	}

	sources := []int64{31, 32}
	companies := []int64{41, 42}
	task.updateLifecycle(func(state *taskLifecycleState) {
		state.SourceTaskIDs = sources
		state.CompanyIDs = companies
	})
	sources[0] = 97
	companies[0] = 96

	fresh = task.lifecycleSnapshot()
	if got := fresh.SourceTaskIDs[0]; got != 31 {
		t.Fatalf("lifecycle update retained caller source slice: got %d", got)
	}
	if got := fresh.CompanyIDs[0]; got != 41 {
		t.Fatalf("lifecycle update retained caller company slice: got %d", got)
	}
}

func TestTaskLifecycleSnapshotConcurrentConsistency(t *testing.T) {
	task := &Task{}
	writeState := func(generation int64, queued bool) {
		task.updateLifecycle(func(state *taskLifecycleState) {
			state.Status = map[bool]string{true: "queued", false: "paused"}[queued]
			state.Queued = queued
			state.Paused = !queued
			state.QueuedAt = generation
			state.QueueMode = map[bool]string{true: "resume", false: "bootstrap"}[queued]
			state.CompletedAt = generation
			state.FirstRunAt = generation
			state.DeadlineAt = generation
			state.SourceTaskIDs = []int64{generation, generation + 1}
			state.CompanyIDs = []int64{generation, generation + 1}
		})
	}
	writeState(1, true)

	var wg sync.WaitGroup
	for writer := 0; writer < 4; writer++ {
		writer := writer
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 1; i <= 1000; i++ {
				generation := int64(writer*1000 + i)
				writeState(generation, generation%2 == 0)
			}
		}()
	}

	errCh := make(chan error, 4)
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				state := task.lifecycleSnapshot()
				if state.Queued == state.Paused {
					errCh <- fmt.Errorf("queued=%v paused=%v", state.Queued, state.Paused)
					return
				}
				wantStatus := map[bool]string{true: "queued", false: "paused"}[state.Queued]
				wantMode := map[bool]string{true: "resume", false: "bootstrap"}[state.Queued]
				if state.Status != wantStatus || state.QueueMode != wantMode {
					errCh <- fmt.Errorf("inconsistent status/mode: %+v", state)
					return
				}
				generation := state.QueuedAt
				if state.CompletedAt != generation || state.FirstRunAt != generation || state.DeadlineAt != generation ||
					len(state.SourceTaskIDs) != 2 || len(state.CompanyIDs) != 2 ||
					state.SourceTaskIDs[0] != generation || state.CompanyIDs[0] != generation {
					errCh <- fmt.Errorf("mixed lifecycle snapshot: %+v", state)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}
