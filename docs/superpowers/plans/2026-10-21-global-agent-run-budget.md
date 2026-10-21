# Global Agent Run Budget Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bound all planner and worker executions across admitted tasks with live configuration, fair worker scheduling, and protected planner capacity.

**Architecture:** A manager-owned in-memory gate always tracks active runs and conditionally limits them. Control work uses a FIFO queue; workers use FIFO queues per task plus round-robin task selection. Engine call sites acquire once around the complete planner round or worker execution/retry/wrap-up sequence.

**Tech Stack:** Go, `context`, `sync.Mutex`, PostgreSQL settings, HTTP JSON API, React/Next.js, TypeScript.

**Design:** `docs/superpowers/specs/2026-10-21-runtime-safety-delivery-foundation-design.md`

**Sequence:** Implement after `2026-10-21-planner-intent-admission.md` and before browser/CI work.

---

## File Map

- Create `server/agent_run_gate.go`: scheduling, cancellation, reconfiguration, and counters.
- Create `server/agent_run_gate_test.go`: deterministic fairness, reservation, cancellation, and race tests.
- Create `server/agent_run_settings_test.go`: persistence and API contract tests.
- Modify `server/manager.go`: settings keys, defaults, manager ownership, and live updates.
- Modify `server/engine.go`: planner and worker acquisition.
- Modify `server/engine_timeout.go`: final planner acquisition.
- Modify `server/server.go`: settings JSON fields and validation.
- Modify `web/src/lib/types.ts`: new settings fields.
- Modify `web/src/lib/mock/data.ts` and `web/src/lib/mock/handler.ts`: deterministic mock state.
- Modify `web/src/app/(main)/system/settings/page.tsx`: global cap controls and counters.
- Create `web/src/components/agent-concurrency-settings.test.mjs`: frontend contract regression test.

### Task 1: Implement the fair, cancellation-safe run gate

**Files:**
- Create: `server/agent_run_gate.go`
- Create: `server/agent_run_gate_test.go`

- [ ] **Step 1: Write failing gate tests**

Create `server/agent_run_gate_test.go`:

```go
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
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		s := gate.snapshot()
		if s.Active == active && s.Waiting == waiting {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("gate active=%d waiting=%d, want %d/%d", s.Active, s.Waiting, active, waiting)
		case <-ticker.C:
		}
	}
}

func acquireAsync(gate *agentRunGate, taskID string, kind agentRunKind) <-chan func() {
	out := make(chan func(), 1)
	go func() {
		release, err := gate.acquire(context.Background(), taskID, kind)
		if err == nil {
			out <- release
		}
	}()
	return out
}

func TestAgentRunGateReservesControlCapacity(t *testing.T) {
	gate := newAgentRunGate(true, 3)
	releaseA, _ := gate.acquire(t.Context(), "a", agentRunWorker)
	releaseB, _ := gate.acquire(t.Context(), "b", agentRunWorker)
	blockedWorker := acquireAsync(gate, "c", agentRunWorker)
	waitGate(t, gate, 2, 1)
	control := acquireAsync(gate, "planner", agentRunControl)
	var releaseControl func()
	select {
	case releaseControl = <-control:
	case <-time.After(time.Second):
		t.Fatal("reserved control permit was unavailable")
	}
	select {
	case <-blockedWorker:
		t.Fatal("worker consumed the reserved control permit")
	default:
	}
	releaseControl()
	releaseA()
	releaseB()
	releaseC := <-blockedWorker
	releaseC()
}

func TestAgentRunGateRoundRobinsWorkerTasks(t *testing.T) {
	gate := newAgentRunGate(true, 2)
	held, _ := gate.acquire(t.Context(), "held", agentRunWorker)
	a1 := acquireAsync(gate, "a", agentRunWorker)
	waitGate(t, gate, 1, 1)
	b1 := acquireAsync(gate, "b", agentRunWorker)
	waitGate(t, gate, 1, 2)
	a2 := acquireAsync(gate, "a", agentRunWorker)
	waitGate(t, gate, 1, 3)
	held()
	releaseA1 := <-a1
	releaseA1()
	releaseB1 := <-b1
	select {
	case <-a2:
		t.Fatal("task a received two turns before task b")
	default:
	}
	releaseB1()
	releaseA2 := <-a2
	releaseA2()
}

func TestAgentRunGateLimitOnePrioritizesQueuedControl(t *testing.T) {
	gate := newAgentRunGate(true, 1)
	held, _ := gate.acquire(t.Context(), "held", agentRunWorker)
	worker := acquireAsync(gate, "worker", agentRunWorker)
	waitGate(t, gate, 1, 1)
	control := acquireAsync(gate, "planner", agentRunControl)
	waitGate(t, gate, 1, 2)
	held()
	releaseControl := <-control
	select {
	case <-worker:
		t.Fatal("worker bypassed queued control work")
	default:
	}
	releaseControl()
	releaseWorker := <-worker
	releaseWorker()
}

func TestAgentRunGateCancellationRemovesWaiter(t *testing.T) {
	gate := newAgentRunGate(true, 1)
	held, _ := gate.acquire(t.Context(), "held", agentRunControl)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := gate.acquire(ctx, "waiting", agentRunControl)
		done <- err
	}()
	waitGate(t, gate, 1, 1)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("acquire error=%v", err)
	}
	waitGate(t, gate, 1, 0)
	held()
	waitGate(t, gate, 0, 0)
}

func TestAgentRunGateLiveReconfigurationDoesNotCancelActive(t *testing.T) {
	gate := newAgentRunGate(false, 8)
	var releases []func()
	for i := range 3 {
		release, err := gate.acquire(t.Context(), string(rune('a'+i)), agentRunControl)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	gate.configure(true, 1)
	waiter := acquireAsync(gate, "next", agentRunControl)
	waitGate(t, gate, 3, 1)
	releases[0]()
	releases[1]()
	select {
	case <-waiter:
		t.Fatal("new run entered before active usage fell below the lowered limit")
	default:
	}
	releases[2]()
	releaseNext := <-waiter
	gate.configure(false, 8)
	releaseNext()
}

func TestAgentRunGateDisableWakesAllWaiters(t *testing.T) {
	gate := newAgentRunGate(true, 1)
	held, _ := gate.acquire(t.Context(), "held", agentRunControl)
	workerA := acquireAsync(gate, "a", agentRunWorker)
	workerB := acquireAsync(gate, "b", agentRunWorker)
	waitGate(t, gate, 1, 2)
	gate.configure(false, 1)
	releaseA, releaseB := <-workerA, <-workerB
	waitGate(t, gate, 3, 0)
	releaseA()
	releaseB()
	held()
	waitGate(t, gate, 0, 0)
}

func TestAgentRunReleaseIsIdempotent(t *testing.T) {
	gate := newAgentRunGate(true, 1)
	release, _ := gate.acquire(t.Context(), "task", agentRunControl)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); release() }()
	}
	wg.Wait()
	waitGate(t, gate, 0, 0)
}
```

- [ ] **Step 2: Run the tests and verify RED**

```bash
go test ./server -run TestAgentRunGate -count=1
```

Expected: build failure because the gate types and methods do not exist.

- [ ] **Step 3: Implement the gate**

Create `server/agent_run_gate.go`:

```go
package server

import (
	"context"
	"sync"
)

type agentRunKind uint8

const (
	agentRunControl agentRunKind = iota
	agentRunWorker
)

type agentRunStats struct {
	Enabled bool
	Limit   int
	Active  int
	Waiting int
}

type agentRunWaiter struct {
	taskID   string
	kind     agentRunKind
	ready    chan struct{}
	granted  bool
	canceled bool
}

type agentRunGate struct {
	mu          sync.Mutex
	enabled     bool
	limit       int
	active      int
	workers     int
	waiting     int
	controlQ    []*agentRunWaiter
	workerQ     map[string][]*agentRunWaiter
	workerOrder []string
}

func newAgentRunGate(enabled bool, limit int) *agentRunGate {
	if limit < 1 {
		limit = defaultAgentConcurrencyLimit
	}
	return &agentRunGate{enabled: enabled, limit: limit, workerQ: map[string][]*agentRunWaiter{}}
}

func (g *agentRunGate) snapshot() agentRunStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return agentRunStats{Enabled: g.enabled, Limit: g.limit, Active: g.active, Waiting: g.waiting}
}

func (g *agentRunGate) configure(enabled bool, limit int) {
	if limit < 1 {
		limit = defaultAgentConcurrencyLimit
	}
	g.mu.Lock()
	g.enabled, g.limit = enabled, limit
	g.scheduleLocked()
	g.mu.Unlock()
}

func (g *agentRunGate) acquire(ctx context.Context, taskID string, kind agentRunKind) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	w := &agentRunWaiter{taskID: taskID, kind: kind, ready: make(chan struct{})}
	g.mu.Lock()
	g.enqueueLocked(w)
	g.scheduleLocked()
	g.mu.Unlock()
	select {
	case <-w.ready:
		if err := ctx.Err(); err != nil {
			g.release(w)
			return nil, err
		}
		return func() { g.release(w) }, nil
	case <-ctx.Done():
		g.mu.Lock()
		if w.granted {
			g.mu.Unlock()
			g.release(w)
		} else if !w.canceled {
			w.canceled = true
			g.waiting--
			g.scheduleLocked()
			g.mu.Unlock()
		} else {
			g.mu.Unlock()
		}
		return nil, ctx.Err()
	}
}

func (g *agentRunGate) enqueueLocked(w *agentRunWaiter) {
	g.waiting++
	if w.kind == agentRunControl {
		g.controlQ = append(g.controlQ, w)
		return
	}
	if len(g.workerQ[w.taskID]) == 0 {
		g.workerOrder = append(g.workerOrder, w.taskID)
	}
	g.workerQ[w.taskID] = append(g.workerQ[w.taskID], w)
}

func (g *agentRunGate) canGrantControlLocked() bool {
	return !g.enabled || g.active < g.limit
}

func (g *agentRunGate) canGrantWorkerLocked() bool {
	if !g.enabled {
		return true
	}
	if g.active >= g.limit {
		return false
	}
	if g.limit == 1 {
		return len(g.controlQ) == 0
	}
	return g.workers < g.limit-1
}

func (g *agentRunGate) popControlLocked() *agentRunWaiter {
	for len(g.controlQ) > 0 {
		w := g.controlQ[0]
		g.controlQ = g.controlQ[1:]
		if !w.canceled {
			return w
		}
	}
	return nil
}

func (g *agentRunGate) popWorkerLocked() *agentRunWaiter {
	for len(g.workerOrder) > 0 {
		taskID := g.workerOrder[0]
		g.workerOrder = g.workerOrder[1:]
		queue := g.workerQ[taskID]
		for len(queue) > 0 && queue[0].canceled {
			queue = queue[1:]
		}
		if len(queue) == 0 {
			delete(g.workerQ, taskID)
			continue
		}
		w := queue[0]
		queue = queue[1:]
		if len(queue) == 0 {
			delete(g.workerQ, taskID)
		} else {
			g.workerQ[taskID] = queue
			g.workerOrder = append(g.workerOrder, taskID)
		}
		return w
	}
	return nil
}

func (g *agentRunGate) grantLocked(w *agentRunWaiter) {
	w.granted = true
	g.waiting--
	g.active++
	if w.kind == agentRunWorker {
		g.workers++
	}
	close(w.ready)
}

func (g *agentRunGate) scheduleLocked() {
	for {
		if g.canGrantControlLocked() {
			if w := g.popControlLocked(); w != nil {
				g.grantLocked(w)
				continue
			}
		}
		if g.canGrantWorkerLocked() {
			if w := g.popWorkerLocked(); w != nil {
				g.grantLocked(w)
				continue
			}
		}
		return
	}
}

func (g *agentRunGate) release(w *agentRunWaiter) {
	g.mu.Lock()
	if !w.granted {
		g.mu.Unlock()
		return
	}
	w.granted = false
	g.active--
	if w.kind == agentRunWorker {
		g.workers--
	}
	g.scheduleLocked()
	g.mu.Unlock()
}
```

- [ ] **Step 4: Run unit and race tests**

```bash
go test ./server -run TestAgentRunGate -count=1
go test -race ./server -run TestAgentRunGate -count=1
```

Expected: PASS with no deadlock or race report.

- [ ] **Step 5: Commit the scheduler**

```bash
git add server/agent_run_gate.go server/agent_run_gate_test.go
GIT_AUTHOR_NAME='hypnguyen1209' GIT_AUTHOR_EMAIL='hypnguyen1209@gmail.com' GIT_COMMITTER_NAME='hypnguyen1209' GIT_COMMITTER_EMAIL='hypnguyen1209@gmail.com' git commit -m 'feat(server): add global agent run gate'
```

### Task 2: Persist and expose live gate settings

**Files:**
- Modify: `server/manager.go:219`
- Modify: `server/manager.go:291`
- Modify: `server/manager.go:345`
- Modify: `server/server.go:3320`
- Modify: `server/server.go:3410`
- Create: `server/agent_run_settings_test.go`

- [ ] **Step 1: Add failing manager and API tests**

Create `server/agent_run_settings_test.go`:

```go
package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func preserveAgentConcurrencySettings(t *testing.T, m *Manager) {
	t.Helper()
	keys := []string{"agent_concurrency_enabled", "agent_concurrency_limit"}
	type savedSetting struct{ value string; present bool }
	saved := map[string]savedSetting{}
	for _, key := range keys {
		value, present, err := m.pg.GetSetting(key)
		if err != nil {
			t.Fatal(err)
		}
		saved[key] = savedSetting{value: value, present: present}
	}
	t.Cleanup(func() {
		for _, key := range keys {
			if previous := saved[key]; previous.present {
				_ = m.pg.SetSetting(key, previous.value)
			} else {
				_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, key)
			}
		}
	})
}

func TestAgentConcurrencyDefaultsDisabledAtEight(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	preserveAgentConcurrencySettings(t, m)
	keys := []string{"agent_concurrency_enabled", "agent_concurrency_limit"}
	_, _ = m.pg.Exec(`DELETE FROM settings WHERE key = ANY($1)`, keys)
	m.agentRuns.configure(false, defaultAgentConcurrencyLimit)
	enabled, limit := m.AgentConcurrency()
	if enabled || limit != 8 {
		t.Fatalf("enabled=%v limit=%d", enabled, limit)
	}
}

func TestSetAgentConcurrencyPersistsAndReconfiguresLiveGate(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	preserveAgentConcurrencySettings(t, m)
	if err := m.SetAgentConcurrency(true, 3); err != nil {
		t.Fatal(err)
	}
	enabled, limit := m.AgentConcurrency()
	stats := m.agentRuns.snapshot()
	if !enabled || limit != 3 || !stats.Enabled || stats.Limit != 3 {
		t.Fatalf("settings=%v/%d stats=%+v", enabled, limit, stats)
	}
	reloaded, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reloaded.Close() })
	reloadedEnabled, reloadedLimit := reloaded.AgentConcurrency()
	if !reloadedEnabled || reloadedLimit != 3 {
		t.Fatalf("reloaded settings=%v/%d, want true/3", reloadedEnabled, reloadedLimit)
	}
	if err := m.SetAgentConcurrency(true, 0); err == nil {
		t.Fatal("zero limit was accepted")
	}
}

func TestPutSettingsValidatesAgentConcurrency(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s := &Server{m: m, engine: NewEngine(m)}
	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{"agent_concurrency_enabled":true,"agent_concurrency_limit":0}`))
	rec := httptest.NewRecorder()
	s.putSettings(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSettingsPayloadIncludesAgentRunCounters(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s := &Server{m: m, engine: NewEngine(m)}
	payload := s.settingsPayload()
	for _, key := range []string{"agent_concurrency_enabled", "agent_concurrency_limit", "agent_runs_active", "agent_runs_waiting"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("settings payload missing %s", key)
		}
	}
}
```

- [ ] **Step 2: Run tests and verify RED**

```bash
go test ./server -run 'TestAgentConcurrency|TestSetAgentConcurrency|TestPutSettingsValidatesAgentConcurrency|TestSettingsPayloadIncludesAgentRunCounters' -count=1
```

Expected: build failure for missing settings fields and methods.

- [ ] **Step 3: Add manager ownership and configuration**

Add `agentRuns *agentRunGate` to `Manager` and add constants:

```go
settingAgentConcurrencyOn    = "agent_concurrency_enabled"
settingAgentConcurrencyLimit = "agent_concurrency_limit"
defaultAgentConcurrencyLimit = 8
```

Add methods:

```go
func agentConcurrencyFromDB(pg *pgdb.DB) (bool, int) {
	enabled := pg.GetBool(settingAgentConcurrencyOn, false)
	limit := defaultAgentConcurrencyLimit
	if raw, ok, _ := pg.GetSetting(settingAgentConcurrencyLimit); ok {
		if parsed, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && parsed >= 1 {
			limit = parsed
		}
	}
	return enabled, limit
}

func (m *Manager) AgentConcurrency() (bool, int) {
	if m == nil || m.agentRuns == nil {
		return false, defaultAgentConcurrencyLimit
	}
	stats := m.agentRuns.snapshot()
	return stats.Enabled, stats.Limit
}

func (m *Manager) SetAgentConcurrency(enabled bool, limit int) error {
	if limit < 1 {
		return fmt.Errorf("agent concurrency limit must be >0")
	}
	if err := m.pg.SetSetting(settingAgentConcurrencyLimit, strconv.Itoa(limit)); err != nil {
		return err
	}
	if err := m.pg.SetSetting(settingAgentConcurrencyOn, strconv.FormatBool(enabled)); err != nil {
		return err
	}
	m.agentRuns.configure(enabled, limit)
	return nil
}
```

Immediately after constructing `m` in `NewManager`:

```go
enabled, limit := agentConcurrencyFromDB(pg)
m.agentRuns = newAgentRunGate(enabled, limit)
```

- [ ] **Step 4: Extend the settings API**

In `settingsPayload`:

```go
agentOn, agentLimit := s.m.AgentConcurrency()
agentStats := s.m.agentRuns.snapshot()
```

Add:

```go
"agent_concurrency_enabled": agentOn,
"agent_concurrency_limit":   agentLimit,
"agent_runs_active":         agentStats.Active,
"agent_runs_waiting":        agentStats.Waiting,
```

Add request fields:

```go
AgentConcurrencyEnabled *bool `json:"agent_concurrency_enabled"`
AgentConcurrencyLimit   *int  `json:"agent_concurrency_limit"`
```

Handle the partial update before Python settings:

```go
if req.AgentConcurrencyEnabled != nil || req.AgentConcurrencyLimit != nil {
	on, limit := s.m.AgentConcurrency()
	if req.AgentConcurrencyEnabled != nil {
		on = *req.AgentConcurrencyEnabled
	}
	if req.AgentConcurrencyLimit != nil {
		limit = *req.AgentConcurrencyLimit
	}
	if err := s.m.SetAgentConcurrency(on, limit); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
}
```

- [ ] **Step 5: Run tests and commit settings**

```bash
go test ./server -run 'TestAgentConcurrency|TestSetAgentConcurrency|TestPutSettingsValidatesAgentConcurrency|TestSettingsPayloadIncludesAgentRunCounters' -count=1
```

Expected: PASS.

```bash
git add server/manager.go server/server.go server/agent_run_settings_test.go
GIT_AUTHOR_NAME='hypnguyen1209' GIT_AUTHOR_EMAIL='hypnguyen1209@gmail.com' GIT_COMMITTER_NAME='hypnguyen1209' GIT_COMMITTER_EMAIL='hypnguyen1209@gmail.com' git commit -m 'feat(settings): configure global agent concurrency'
```

### Task 3: Gate complete planner and worker runs

**Files:**
- Modify: `server/engine.go:421`
- Modify: `server/engine.go:880`
- Modify: `server/engine.go:1040`
- Modify: `server/engine_timeout.go:345`
- Modify: `server/agent_run_gate_test.go`

- [ ] **Step 1: Add a failing engine integration test**

Append to `server/agent_run_gate_test.go`, merging the new imports into the existing block:

```go
func TestRunIntentWaitsForGlobalWorkerPermit(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.agentRuns.configure(true, 1)
	task, err := m.CreateTask("gated worker", "finish one intent", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	intentID, err := task.Store.AddIntent(map[string]any{"summary": "wait for permit"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := task.Store.ClaimIntent(intentID, "test"); err != nil || !ok {
		t.Fatalf("claim=%v err=%v", ok, err)
	}
	intent, _ := task.Store.GetNode(intentID)
	held, _ := m.agentRuns.acquire(t.Context(), task.ID, agentRunControl)
	var calls atomic.Int32
	provider := retestProvider{complete: func(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		calls.Add(1)
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("done")}}, "end_turn", llm.Usage{}, nil
	}}
	worker := agent.NewWorker(provider, "test", m.dir, nil, 10000, 2)
	worker.SetNonStreaming(func() bool { return true })
	engine := NewEngine(m)
	done := make(chan struct{})
	go func() {
		engine.runIntent(t.Context(), task, "test", worker, intent, "", "")
		close(done)
	}()
	waitGate(t, m.agentRuns, 1, 1)
	if calls.Load() != 0 {
		t.Fatal("worker called the provider before a permit was available")
	}
	held()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not run after permit release")
	}
	if calls.Load() == 0 {
		t.Fatal("worker never called the provider")
	}
}
```

Add imports for `sync/atomic`, `github.com/hypnguyen1209/V-ARTEX/agent`, and `github.com/Autumn-27/norma/llm`.

- [ ] **Step 2: Run the integration test and verify RED**

```bash
go test ./server -run TestRunIntentWaitsForGlobalWorkerPermit -count=1
```

Expected: FAIL because `runIntent` reaches the provider while the control permit is held.

- [ ] **Step 3: Add one engine acquisition helper**

Add near `NewEngine`:

```go
func (e *Engine) acquireAgentRun(ctx context.Context, taskID string, kind agentRunKind) (func(), error) {
	if e == nil || e.m == nil || e.m.agentRuns == nil {
		return func() {}, nil
	}
	return e.m.agentRuns.acquire(ctx, taskID, kind)
}
```

- [ ] **Step 4: Gate normal and final planner rounds**

In `plannerLoop.runRound`, replace the existing block from the round-marker emission through `e.EndLLMCall(t.ID)` with this scoped acquisition. Then update the following switch to use `planErr` instead of `err`:

```go
var met bool
var reason string
var planErr error
acquireErr := func() error {
	releaseRun, err := e.acquireAgentRun(ectx, t.ID, agentRunControl)
	if err != nil {
		return err
	}
	defer releaseRun()
	e.emitActivity(t, db.Activity{Worker: "planner", Kind: "round",
		Summary: fmt.Sprintf("Planning round %d", e.nextPlannerRound(t.ID))})
	triggers := t.drainTriggers()
	taskIDInt, _ := strconv.ParseInt(t.ID, 10, 64)
	e.BeginLLMCall(t.ID)
	defer e.EndLLMCall(t.ID)
	met, reason, planErr = planner.Plan(ectx, taskIDInt, e.m.assets, t.Store, t.Goal, triggers, emit)
	return nil
}()
if acquireErr != nil {
	return
}
```

Use `case planErr != nil && ectx.Err() == nil:` and log `planErr` in the existing error branch. This releases the control permit before `completeGoalMet` coordinates task settlement.

In `runFinalPlannerRound`, replace the existing block from the final round marker through `e.EndLLMCall(t.ID)` with:

```go
releaseRun, acquireErr := e.acquireAgentRun(fctx, t.ID, agentRunControl)
if acquireErr != nil {
	return false
}
var reason string
var err error
func() {
	defer releaseRun()
	e.emitActivity(t, db.Activity{Worker: "planner", Kind: "round",
		Summary: fmt.Sprintf("Task timeout settlement · final judgment (round %d)", e.nextPlannerRound(t.ID))})
	taskID, _ := strconv.ParseInt(t.ID, 10, 64)
	e.BeginLLMCall(t.ID)
	defer e.EndLLMCall(t.ID)
	met, reason, err = planner.Plan(fctx, taskID, e.m.assets, t.Store, t.Goal, t.drainTriggers(), emit)
}()
```

- [ ] **Step 5: Gate the whole worker retry and wrap-up sequence**

In `runIntent`, acquire after `workCtx` and hooks are prepared. Replace the current `var err error` declaration with:

```go
releaseRun, acquireErr := e.acquireAgentRun(workCtx, t.ID, agentRunWorker)
var reason harness.TerminalReason
var wrote agent.WriteCounts
var err = acquireErr
```

Insert `if acquireErr == nil { func() {` immediately before the first existing `e.BeginLLMCall(t.ID)`, followed by `defer releaseRun()` as the first statement in the anonymous function. Close it with `}() }` immediately after the existing goal-completion wrap-up block and before `workCause := context.Cause(workCtx)`. This exact boundary keeps the initial `Execute`/`ExecuteWithMessage`, every model-error retry, and the goal-completion `ExecuteWithMessage` under one permit, while releasing it before intent-state settlement. Do not acquire again inside any of those paths. Keep the subsequent settlement logic outside the branch so a canceled waiter is restored or stopped by the same named-cause rules as a canceled provider call.

- [ ] **Step 6: Run integration, settlement, package, and race tests**

```bash
go test ./server -run 'TestRunIntentWaitsForGlobalWorkerPermit|TestGoalSettlement|TestSettlementKinds' -count=1
go test ./server -count=1
go test -race ./server -run 'TestAgentRunGate|TestRunIntentWaitsForGlobalWorkerPermit' -count=1
```

Expected: PASS. Goal wrap-up must not deadlock or create a second waiter.

- [ ] **Step 7: Commit engine admission**

```bash
git add server/engine.go server/engine_timeout.go server/agent_run_gate_test.go
GIT_AUTHOR_NAME='hypnguyen1209' GIT_AUTHOR_EMAIL='hypnguyen1209@gmail.com' GIT_COMMITTER_NAME='hypnguyen1209' GIT_COMMITTER_EMAIL='hypnguyen1209@gmail.com' git commit -m 'feat(engine): enforce global agent run budget'
```

### Task 4: Add frontend controls and live counters

**Files:**
- Modify: `web/src/lib/types.ts:930`
- Modify: `web/src/lib/mock/data.ts:3420`
- Modify: `web/src/lib/mock/handler.ts:2249`
- Modify: `web/src/app/(main)/system/settings/page.tsx:20`
- Modify: `web/src/app/(main)/system/settings/page.tsx:680`
- Create: `web/src/components/agent-concurrency-settings.test.mjs`

- [ ] **Step 1: Add a failing frontend contract test**

Create `web/src/components/agent-concurrency-settings.test.mjs`:

```js
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const types = readFileSync(new URL("../lib/types.ts", import.meta.url), "utf8");
const page = readFileSync(new URL("../app/(main)/system/settings/page.tsx", import.meta.url), "utf8");
const mock = readFileSync(new URL("../lib/mock/handler.ts", import.meta.url), "utf8");

test("global agent concurrency settings stay aligned across type, UI, and mock API", () => {
	for (const field of ["agent_concurrency_enabled", "agent_concurrency_limit", "agent_runs_active", "agent_runs_waiting"]) {
		assert.match(types, new RegExp(`${field}\\?`));
		assert.match(page, new RegExp(field));
	}
	assert.match(page, /Global Agent Concurrency/);
	assert.match(page, /setInterval/);
	assert.match(mock, /Object\.assign\(D\.settings, b\)/);
});
```

- [ ] **Step 2: Run the test and verify RED**

```bash
cd web && node --test src/components/agent-concurrency-settings.test.mjs
```

Expected: FAIL because the fields and UI do not exist.

- [ ] **Step 3: Extend frontend types and mock state**

Add to `Settings`:

```ts
agent_concurrency_enabled?: boolean;
agent_concurrency_limit?: number;
agent_runs_active?: number;
agent_runs_waiting?: number;
```

Add to `D.settings`:

```ts
agent_concurrency_enabled: false,
agent_concurrency_limit: 8,
agent_runs_active: 0,
agent_runs_waiting: 0,
```

Replace the mock PUT branch:

```ts
if (path === "/settings" && m === "PUT") {
	Object.assign(D.settings, b);
	return { ...D.settings };
}
```

- [ ] **Step 4: Add state, apply, and save behavior**

In `SystemSettingsPage`:

```tsx
const [agentConcurrencyEnabled, setAgentConcurrencyEnabled] = React.useState(false);
const [agentConcurrencyLimit, setAgentConcurrencyLimit] = React.useState("8");
const [agentRunsActive, setAgentRunsActive] = React.useState(0);
const [agentRunsWaiting, setAgentRunsWaiting] = React.useState(0);
const [savingAgentConcurrency, setSavingAgentConcurrency] = React.useState(false);
```

Extend `apply`:

```tsx
setAgentConcurrencyEnabled(!!s.agent_concurrency_enabled);
setAgentConcurrencyLimit(String(s.agent_concurrency_limit ?? 8));
setAgentRunsActive(s.agent_runs_active ?? 0);
setAgentRunsWaiting(s.agent_runs_waiting ?? 0);
```

Add:

```tsx
const saveAgentConcurrency = () => {
	const limit = Number(agentConcurrencyLimit);
	if (!Number.isInteger(limit) || limit < 1) {
		toast.error("Global agent concurrency must be an integer greater than 0");
		return;
	}
	setSavingAgentConcurrency(true);
	api
		.setSettings({ agent_concurrency_enabled: agentConcurrencyEnabled, agent_concurrency_limit: limit })
		.then((settings) => {
			apply(settings);
			toast.success(agentConcurrencyEnabled ? `Global agent concurrency limited to ${limit}` : "Global agent concurrency limit disabled");
		})
		.catch((error) => toast.error(`Save failed: ${(error as Error).message}`))
		.finally(() => setSavingAgentConcurrency(false));
};
```

Poll only the read-only counters while the page is open; do not call `apply` from this interval because that would overwrite unsaved form input:

```tsx
React.useEffect(() => {
	if (!loaded) return;
	let disposed = false;
	const refreshCounters = () => {
		api.settings().then((settings) => {
			if (!disposed) {
				setAgentRunsActive(settings.agent_runs_active ?? 0);
				setAgentRunsWaiting(settings.agent_runs_waiting ?? 0);
			}
		}).catch(() => undefined);
	};
	const timer = window.setInterval(refreshCounters, 3000);
	return () => {
		disposed = true;
		window.clearInterval(timer);
	};
}, [loaded]);
```

- [ ] **Step 5: Render the settings card**

Place after `Worker concurrency · Work Agents`:

```tsx
<Card aria-labelledby="agent-concurrency-heading" className="mb-4 break-inside-avoid md:mb-6">
	<CardHeader>
		<CardTitle id="agent-concurrency-heading" className="flex items-center gap-2 text-base">
			<GaugeIcon className="size-4" />
			Global Agent Concurrency
		</CardTitle>
		<CardDescription>
			Bounds planner and Worker runs across all tasks. One slot is reserved for planning when the limit is greater than one.
		</CardDescription>
	</CardHeader>
	<CardContent className="grid gap-4">
		<div className="flex items-center justify-between gap-4">
			<Label htmlFor="agent-concurrency-enabled">Enable global limit</Label>
			<Switch id="agent-concurrency-enabled" checked={agentConcurrencyEnabled}
				onCheckedChange={setAgentConcurrencyEnabled} disabled={!loaded || savingAgentConcurrency} />
		</div>
		<div className="flex items-end gap-3">
			<div className="grid gap-2">
				<Label htmlFor="agent-concurrency-limit">Maximum active runs</Label>
				<Input id="agent-concurrency-limit" type="number" min={1} value={agentConcurrencyLimit}
					onChange={(event) => setAgentConcurrencyLimit(event.target.value)}
					disabled={!loaded || savingAgentConcurrency} />
			</div>
			<Button onClick={saveAgentConcurrency} disabled={!loaded || savingAgentConcurrency}>Save</Button>
		</div>
		<p className="text-xs text-muted-foreground">
			Active: {agentRunsActive} · Waiting: {agentRunsWaiting}
		</p>
	</CardContent>
</Card>
```

Import `GaugeIcon` if it is not already present.

- [ ] **Step 6: Run frontend checks and build**

```bash
cd web
node --test src/components/agent-concurrency-settings.test.mjs
npm run check
npm run build:static
```

Expected: all commands PASS.

- [ ] **Step 7: Commit the frontend**

```bash
git add web/src/lib/types.ts web/src/lib/mock/data.ts web/src/lib/mock/handler.ts web/src/app/'(main)'/system/settings/page.tsx web/src/components/agent-concurrency-settings.test.mjs
GIT_AUTHOR_NAME='hypnguyen1209' GIT_AUTHOR_EMAIL='hypnguyen1209@gmail.com' GIT_COMMITTER_NAME='hypnguyen1209' GIT_COMMITTER_EMAIL='hypnguyen1209@gmail.com' git commit -m 'feat(web): configure global agent concurrency'
```
