# Goal Completion Worker Drain Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve every identified worker result when the planner proves the final goal, while still completing the task within a bounded 90-second grace period.

**Architecture:** Reuse the engine's existing per-task settlement gate, but give it a typed reason (`task_timeout` or `goal_met`). Worker hooks observe goal settlement dynamically, prohibit new probes, permit the three durable write-back tools, and force one final wrap-up turn. The planner waits for admitted workers before persisting `done`; only workers that exceed the grace period are canceled.

**Tech Stack:** Go, `context`, `sync.Map`, `sync/atomic`, existing Norma harness hooks, PostgreSQL-backed task lifecycle, Docker Compose.

**Design:** `docs/superpowers/specs/2026-10-19-goal-completion-worker-drain-design.md`

---

## File Map

- Create `server/engine_goal_settlement_test.go`: focused regression tests for hook behavior, settlement ownership, and bounded inflight draining.
- Modify `server/engine.go`: goal-wrap-up hook fields/prompt, write-back allowlist, worker hook wiring, planner completion call, and terminal intent classification.
- Modify `server/engine_timeout.go`: typed settlement state, bounded inflight wait, and goal-completion coordinator.
- Modify `.env` only during deployment: select the rebuilt local image tag; this file stays ignored and must retain mode `600`.

### Task 1: Force a safe worker wrap-up after goal completion

**Files:**
- Create: `server/engine_goal_settlement_test.go`
- Modify: `server/engine.go:558-650`
- Modify: `server/engine.go:1038-1049`

- [ ] **Step 1: Write the failing hook tests**

Create `server/engine_goal_settlement_test.go` with tests that use the desired hook API before it exists:

```go
package server

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func TestGoalSettlementHookBlocksProbesAndAllowsWriteback(t *testing.T) {
	for _, tool := range []string{"insert_assets", "record_fact", "report_finding"} {
		h := steerHooks{goalSettling: func() bool { return true }, goalSettlePrompted: &atomic.Bool{}}
		blocked, _, _ := h.PreToolUse(context.Background(), tool, nil)
		if blocked {
			t.Fatalf("goal settlement blocked durable write-back tool %q", tool)
		}
	}

	prompted := &atomic.Bool{}
	h := steerHooks{goalSettling: func() bool { return true }, goalSettlePrompted: prompted}
	blocked, msg, _ := h.PreToolUse(context.Background(), "Bash", nil)
	if !blocked || msg != goalMetWorkerWrapUpPrompt || !prompted.Load() {
		t.Fatalf("probe was not converted to wrap-up: blocked=%v prompted=%v msg=%q", blocked, prompted.Load(), msg)
	}
}

func TestGoalSettlementHookInjectsExactlyOneFinalTurn(t *testing.T) {
	prompted := &atomic.Bool{}
	h := steerHooks{
		goalSettling: func() bool { return true }, goalSettlePrompted: prompted,
		nudges: &atomic.Int64{}, limit: defaultEmptyTurnNudges,
	}
	normal := []llm.Message{{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("Partial summary")}}}
	_, blocking, _ := h.Stop(context.Background(), normal)
	if len(blocking) != 1 || blocking[0] != goalMetWorkerWrapUpPrompt {
		t.Fatalf("first stop blocking=%v", blocking)
	}
	_, blocking, _ = h.Stop(context.Background(), normal)
	if blocking != nil {
		t.Fatalf("goal wrap-up injected more than once: %v", blocking)
	}
}

func TestGoalSettlementHookIsInactiveDuringNormalExecution(t *testing.T) {
	h := steerHooks{goalSettling: func() bool { return false }, goalSettlePrompted: &atomic.Bool{}}
	blocked, msg, _ := h.PreToolUse(context.Background(), "Bash", nil)
	if blocked || msg != "" {
		t.Fatalf("normal execution changed: blocked=%v msg=%q", blocked, msg)
	}
}
```

- [ ] **Step 2: Run the tests and verify RED**

```bash
go test ./server -run 'TestGoalSettlementHook' -count=1
```

Expected: build failure because `goalSettling`, `goalSettlePrompted`, and `goalMetWorkerWrapUpPrompt` do not exist.

- [ ] **Step 3: Implement the minimal hook behavior**

In `server/engine.go`, add:

```go
const goalMetWorkerWrapUpPrompt = "[Goal completion wrap-up] The task goal has been proven and the task is finalizing. Start no new commands or probes. Persist every identified but unsaved result now with insert_assets, record_fact, and report_finding. Then finish with one plain-text sentence summarizing the key conclusions for this intent."

func goalMetWritebackTool(name string) bool {
	switch name {
	case "insert_assets", "record_fact", "report_finding":
		return true
	default:
		return false
	}
}
```

Extend `steerHooks`:

```go
goalSettling       func() bool
goalSettlePrompted *atomic.Bool
```

At the start of `PreToolUse`, before ordinary planner steering:

```go
if h.goalSettling != nil && h.goalSettling() {
	if goalMetWritebackTool(name) {
		if h.inner != nil {
			return h.inner.PreToolUse(ctx, name, input)
		}
		return false, "", nil
	}
	if h.goalSettlePrompted != nil {
		h.goalSettlePrompted.Store(true)
	}
	return true, goalMetWorkerWrapUpPrompt, nil
}
```

In `Stop`, after respecting an inner hard stop/continuation and before empty-turn handling:

```go
if h.goalSettling != nil && h.goalSettling() && h.goalSettlePrompted != nil && h.goalSettlePrompted.CompareAndSwap(false, true) {
	return false, []string{goalMetWorkerWrapUpPrompt}, ""
}
```

Wire the hook in `runIntent`:

```go
goalSettlePrompted := &atomic.Bool{}
hooks := steerHooks{
	inner: t.Guard.Hooks(), drain: func() (string, bool) { return e.drainSteer(iid) },
	nudges: &atomic.Int64{}, limit: e.emptyTurnNudgeLimit(), label: label,
	goalSettling: func() bool { return e.isGoalSettling(t.ID) },
	goalSettlePrompted: goalSettlePrompted,
}
```

- [ ] **Step 4: Run focused and adjacent hook tests**

```bash
go test ./server -run 'TestGoalSettlementHook|TestSteerHooksStop|TestIsThinkingOnlyTurn' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit Task 1**

```bash
git add server/engine.go server/engine_goal_settlement_test.go
GIT_AUTHOR_NAME='hypnguyen1209' GIT_AUTHOR_EMAIL='hypnguyen1209@gmail.com' GIT_COMMITTER_NAME='hypnguyen1209' GIT_COMMITTER_EMAIL='hypnguyen1209@gmail.com' git commit -m 'fix(engine): wrap up workers when goals complete'
```

### Task 2: Distinguish goal settlement from task timeout and wait for workers

**Files:**
- Modify: `server/engine_goal_settlement_test.go`
- Modify: `server/engine_timeout.go:18-42`

- [ ] **Step 1: Add failing state and drain tests**

Add `time` to the test import block, then append:

```go
func TestSettlementKindsRemainDistinct(t *testing.T) {
	goal := NewEngine(nil)
	if !goal.markGoalSettling("goal") || !goal.isSettling("goal") || !goal.isGoalSettling("goal") || goal.isTaskTimeoutSettling("goal") {
		t.Fatal("goal settlement kind was not retained")
	}
	timed := NewEngine(nil)
	if !timed.markSettling("timeout") || !timed.isSettling("timeout") || timed.isGoalSettling("timeout") || !timed.isTaskTimeoutSettling("timeout") {
		t.Fatal("task-timeout settlement kind was not retained")
	}
}

func TestGoalSettlementWaitsForAdmittedWorker(t *testing.T) {
	e := NewEngine(nil)
	counter := e.inflightCounter("task")
	atomic.StoreInt64(counter, 2)
	go func() {
		time.Sleep(20 * time.Millisecond)
		atomic.StoreInt64(counter, 1)
	}()
	if !e.waitForInflightAtMost(context.Background(), "task", 1, time.Second) {
		t.Fatal("worker drain timed out")
	}
}

func TestGoalSettlementDrainIsBounded(t *testing.T) {
	e := NewEngine(nil)
	atomic.StoreInt64(e.inflightCounter("task"), 2)
	if e.waitForInflightAtMost(context.Background(), "task", 1, 20*time.Millisecond) {
		t.Fatal("stuck worker incorrectly reported as drained")
	}
}
```

- [ ] **Step 2: Run the tests and verify RED**

```bash
go test ./server -run 'TestSettlementKinds|TestGoalSettlementWaits|TestGoalSettlementDrain' -count=1
```

Expected: build failure for missing typed-settlement and wait methods.

- [ ] **Step 3: Implement typed settlement state**

Replace the boolean stored in `Engine.settling` with:

```go
type settlementKind string

const (
	settlementTaskTimeout settlementKind = "task_timeout"
	settlementGoalMet     settlementKind = "goal_met"
)

func (e *Engine) isSettling(taskID string) bool {
	_, ok := e.settling.Load(taskID)
	return ok
}
func (e *Engine) settlementKind(taskID string) settlementKind {
	v, _ := e.settling.Load(taskID)
	kind, _ := v.(settlementKind)
	return kind
}
func (e *Engine) markSettlement(taskID string, kind settlementKind) bool {
	_, loaded := e.settling.LoadOrStore(taskID, kind)
	return !loaded
}
func (e *Engine) markSettling(taskID string) bool {
	return e.markSettlement(taskID, settlementTaskTimeout)
}
func (e *Engine) markGoalSettling(taskID string) bool {
	return e.markSettlement(taskID, settlementGoalMet)
}
func (e *Engine) isGoalSettling(taskID string) bool {
	return e.settlementKind(taskID) == settlementGoalMet
}
func (e *Engine) isTaskTimeoutSettling(taskID string) bool {
	return e.settlementKind(taskID) == settlementTaskTimeout
}
```

- [ ] **Step 4: Implement bounded inflight waiting**

Add:

```go
func (e *Engine) waitForInflightAtMost(ctx context.Context, taskID string, maximum int64, grace time.Duration) bool {
	if e.inflightCount(taskID) <= maximum {
		return true
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return e.inflightCount(taskID) <= maximum
		case <-ticker.C:
			if e.inflightCount(taskID) <= maximum {
				return true
			}
		}
	}
}
```

- [ ] **Step 5: Run tests and race-check the focused package**

```bash
go test ./server -run 'TestSettlementKinds|TestGoalSettlementWaits|TestGoalSettlementDrain' -count=1
go test -race ./server -run 'TestSettlementKinds|TestGoalSettlementWaits|TestGoalSettlementDrain' -count=1
```

Expected: both commands PASS.

- [ ] **Step 6: Commit Task 2**

```bash
git add server/engine_timeout.go server/engine_goal_settlement_test.go
GIT_AUTHOR_NAME='hypnguyen1209' GIT_AUTHOR_EMAIL='hypnguyen1209@gmail.com' GIT_COMMITTER_NAME='hypnguyen1209' GIT_COMMITTER_EMAIL='hypnguyen1209@gmail.com' git commit -m 'fix(engine): distinguish goal and timeout settlement'
```

### Task 3: Replace immediate goal cancellation with graceful completion

**Files:**
- Modify: `server/engine.go:910-930`
- Modify: `server/engine.go:1125-1150`
- Modify: `server/engine_timeout.go`
- Modify: `server/engine_goal_settlement_test.go`

- [ ] **Step 1: Add failing coordinator tests**

Add `github.com/hypnguyen1209/V-ARTEX/agent` to the test imports (the file already imports `time` from Task 2), then append:

```go
func TestCompleteGoalMetWaitsForWorkerBeforeDone(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	task, err := m.CreateTask("goal drain", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()

	e := NewEngine(m)
	atomic.StoreInt64(e.inflightCounter(task.ID), 2) // current planner plus one worker
	execCtx := e.execContextFor(context.Background(), task.ID)
	done := make(chan struct{})
	go func() {
		e.completeGoalMetWithin(context.Background(), task, "proved", time.Second)
		close(done)
	}()
	time.Sleep(30 * time.Millisecond)
	if status := m.TaskStatus(task.ID); status == "done" {
		t.Fatal("task completed before its active worker drained")
	}
	atomic.StoreInt64(e.inflightCounter(task.ID), 1)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("goal completion did not finish after worker drain")
	}
	if status := m.TaskStatus(task.ID); status != "done" {
		t.Fatalf("status=%q, want done", status)
	}
	if execCtx.Err() != nil {
		t.Fatalf("gracefully drained execution was canceled: %v", context.Cause(execCtx))
	}
}

func TestCompleteGoalMetCancelsOnlyAfterGrace(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	task, err := m.CreateTask("goal drain timeout", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()

	e := NewEngine(m)
	atomic.StoreInt64(e.inflightCounter(task.ID), 2)
	execCtx := e.execContextFor(context.Background(), task.ID)
	go func() {
		<-execCtx.Done()
		atomic.StoreInt64(e.inflightCounter(task.ID), 1)
	}()
	e.completeGoalMetWithin(context.Background(), task, "proved", 20*time.Millisecond)
	if status := m.TaskStatus(task.ID); status != "done" {
		t.Fatalf("status=%q, want done before hard cancellation", status)
	}
	if code, _, _, _ := agent.AbortReason(execCtx); code != "goal_met" {
		t.Fatalf("cancel code=%q, want goal_met", code)
	}
}
```

- [ ] **Step 2: Run the tests and verify RED**

```bash
go test ./server -run 'TestCompleteGoalMet' -count=1
```

Expected: build failure because `completeGoalMetWithin` does not exist.

- [ ] **Step 3: Add the goal completion coordinator**

Add to `server/engine_timeout.go`:

```go
func (e *Engine) completeGoalMet(ctx context.Context, t *Task, reason string) {
	e.completeGoalMetWithin(ctx, t, reason, settleDrainGrace)
}

func (e *Engine) completeGoalMetWithin(ctx context.Context, t *Task, reason string, grace time.Duration) {
	drained := true
	if e.markGoalSettling(t.ID) {
		log.Printf("[planner] task %s goals met; draining active workers for up to %s", t.ID, grace)
		drained = e.waitForInflightAtMost(ctx, t.ID, 1, grace)
	}
	if ctx.Err() != nil || e.IsDeleting(t.ID) {
		return
	}
	if err := e.m.SetTaskStatus(t.ID, "done"); err != nil {
		log.Printf("[planner] task %s failed to persist completion: %v", t.ID, err)
		e.settling.Delete(t.ID)
		t.Notify()
		return
	}
	if drained {
		log.Printf("[planner] task %s completion settlement drained all active workers: %s", t.ID, reason)
		return
	}
	log.Printf("[planner] task %s completion drain timed out; cancelling remaining workers", t.ID)
	e.cancelExec(t.ID, agent.AbortGoalMet)
	e.waitForInflightAtMost(ctx, t.ID, 1, 3*time.Second)
}
```

Replace the planner `case met` body in `server/engine.go` with:

```go
case met:
	log.Printf("[planner] task %s goals judged met: %s", t.ID, reason)
	e.completeGoalMet(ctx, t, reason)
```

- [ ] **Step 4: Keep goal cancellation out of timeout classification**

Change:

```go
if ectx.Err() != nil && e.isSettling(t.ID) {
```

to:

```go
if ectx.Err() != nil && e.isTaskTimeoutSettling(t.ID) {
```

This lets the following terminal-task branch classify a grace-expired goal worker as `stopped`. Confirm the unsafe generic branch existed before this edit and disappears afterward:

```bash
if rg -n 'ectx.Err\(\) != nil && e\.isSettling\(t\.ID\)' server/engine.go; then exit 1; fi
```

- [ ] **Step 5: Run focused regression tests and confirm the unsafe source pattern is gone**

```bash
go test ./server -run 'TestGoalSettlement|TestCompleteGoalMet|TestSettlementKinds|TestSettleDrainAndGoalMetCausesAreDistinct' -count=1
if rg -n 'ectx.Err\(\) != nil && e\.isSettling\(t\.ID\)' server/engine.go; then exit 1; fi
```

Expected: PASS and no source match.

- [ ] **Step 6: Run the complete Go test suite and race detector**

```bash
go test ./...
go test -race ./agent ./server
```

Expected: all packages PASS with no race reports.

- [ ] **Step 7: Commit Task 3**

```bash
git add server/engine.go server/engine_timeout.go server/engine_goal_settlement_test.go
GIT_AUTHOR_NAME='hypnguyen1209' GIT_AUTHOR_EMAIL='hypnguyen1209@gmail.com' GIT_COMMITTER_NAME='hypnguyen1209' GIT_COMMITTER_EMAIL='hypnguyen1209@gmail.com' git commit -m 'fix(engine): drain workers before goal completion'
```

### Task 4: Build, redeploy, and verify the live service

**Files:**
- Modify locally, ignored: `.env`
- Generate locally, ignored: `dist/amd64/artex`

- [ ] **Step 1: Verify source and build the embedded binary**

```bash
git diff --check
go test ./...
version=$(git rev-parse --short=7 HEAD)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags embedui -trimpath -ldflags "-s -w -X main.version=$version" -o dist/amd64/artex ./cmd/artex
./dist/amd64/artex -h
```

- [ ] **Step 2: Build a commit-specific Docker image**

```bash
version=$(git rev-parse --short=7 HEAD)
docker build --build-arg TARGETARCH=amd64 -t "autumn27/artex:v-artex-local-$version" .
docker image inspect "autumn27/artex:v-artex-local-$version" --format '{{.Id}} {{.Size}}'
```

- [ ] **Step 3: Select the image without exposing secrets**

Use `apply_patch` to replace only `ARTEX_TAG=` in `.env`, then run:

```bash
grep '^ARTEX_TAG=' .env
test "$(stat -c '%a' .env)" = 600
docker compose --env-file .env config --quiet
```

- [ ] **Step 4: Recreate only V-ARTEX and preserve JWT state**

```bash
key_before=$(docker exec v-artex-artex-1 sha256sum /app/auth/jwt.key | awk '{print $1}')
docker compose --env-file .env up -d --no-deps --force-recreate --pull never artex
for attempt in $(seq 1 30); do
  curl --noproxy '*' --silent --fail --max-time 2 http://127.0.0.1:8787/api/health && break
  sleep 1
done
key_after=$(docker exec v-artex-artex-1 sha256sum /app/auth/jwt.key | awk '{print $1}')
test "$key_before" = "$key_after"
```

- [ ] **Step 5: Verify the public listener and deployed version**

```bash
version=$(git rev-parse --short=7 HEAD)
test "$(curl --noproxy '*' --silent --fail http://127.0.0.1:8787/api/health)" = "{\"ok\":true,\"service\":\"artex\",\"version\":\"$version\"}"
docker port v-artex-artex-1 | grep -Fx '8787/tcp -> 0.0.0.0:8787'
docker port v-artex-artex-1 | grep -Fx '8788/tcp -> 127.0.0.1:8788'
docker compose --env-file .env ps
```

- [ ] **Step 6: Verify repository and commit identity**

```bash
git status --short --branch
git log -4 --format='%h %an <%ae> | %cn <%ce> | %s'
```

Expected: no uncommitted tracked changes; every new commit uses `hypnguyen1209 <hypnguyen1209@gmail.com>` as author and committer.

Do not push or open a PR unless the user explicitly requests that external action.
