# Planner Intent Admission Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enforce a concurrency-safe maximum of four successfully persisted intents per planner round and reject invalid priorities or malformed payloads before storage.

**Architecture:** Add a small per-run reservation counter to `ToolSet`, configure it only from `Planner.Plan`, and keep non-planner tool sets unlimited. `add_intent` reserves an entire call before writes, releases failed entries, and retains its existing per-item storage-error response shape.

**Tech Stack:** Go, `sync.Mutex`, Norma `actool`, PostgreSQL exploration store, Go race detector.

**Design:** `docs/superpowers/specs/2026-10-21-runtime-safety-delivery-foundation-design.md`

**Sequence:** Implement this plan before `2026-10-21-global-agent-run-budget.md`.

---

## File Map

- Create `agent/intent_budget.go`: per-planner-round atomic reservation/finalization logic.
- Create `agent/tools_intent_admission_test.go`: runtime, schema, priority, malformed JSON, isolation, and race tests.
- Modify `agent/tools.go`: attach an optional budget to `ToolSet`, validate input, advertise the schema limit, and account for successful writes.
- Modify `agent/planner.go`: install a fresh four-intent budget on every `Planner.Plan` call.

### Task 1: Build the concurrency-safe reservation primitive

**Files:**
- Create: `agent/intent_budget.go`
- Create: `agent/tools_intent_admission_test.go`

- [ ] **Step 1: Write failing reservation tests**

Create `agent/tools_intent_admission_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests and verify RED**

```bash
go test ./agent -run 'TestIntentBudget' -count=1
```

Expected: build failure because `newIntentBudget`, `reserve`, and `remaining` do not exist.

- [ ] **Step 3: Implement the minimal budget type**

Create `agent/intent_budget.go`:

```go
package agent

import (
	"fmt"
	"sync"
)

const plannerIntentLimit = 4

type intentBudget struct {
	mu    sync.Mutex
	limit int
	used  int // successful creations plus in-flight reservations
}

func newIntentBudget(limit int) *intentBudget {
	return &intentBudget{limit: limit}
}

func (b *intentBudget) remaining() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limit - b.used
}

func (b *intentBudget) reserve(n int) (func(successful int), error) {
	if b == nil {
		return func(int) {}, nil
	}
	if n < 1 {
		return nil, fmt.Errorf("at least one intent is required")
	}
	b.mu.Lock()
	remaining := b.limit - b.used
	if n > remaining {
		b.mu.Unlock()
		return nil, fmt.Errorf("planner intent limit exceeded: requested %d, remaining %d of %d this round", n, remaining, b.limit)
	}
	b.used += n
	b.mu.Unlock()

	var once sync.Once
	return func(successful int) {
		once.Do(func() {
			if successful < 0 {
				successful = 0
			}
			if successful > n {
				successful = n
			}
			b.mu.Lock()
			b.used -= n - successful
			b.mu.Unlock()
		})
	}, nil
}
```

- [ ] **Step 4: Run the budget tests under the race detector**

```bash
go test -race ./agent -run 'TestIntentBudget' -count=1
```

Expected: PASS with no race report.

- [ ] **Step 5: Commit the primitive**

```bash
git add agent/intent_budget.go agent/tools_intent_admission_test.go
GIT_AUTHOR_NAME='hypnguyen1209' GIT_AUTHOR_EMAIL='hypnguyen1209@gmail.com' GIT_COMMITTER_NAME='hypnguyen1209' GIT_COMMITTER_EMAIL='hypnguyen1209@gmail.com' git commit -m 'feat(agent): add planner intent budget'
```

### Task 2: Enforce payload, priority, and cumulative limits in `add_intent`

**Files:**
- Modify: `agent/tools.go:53`
- Modify: `agent/tools.go:1018`
- Modify: `agent/tools_intent_admission_test.go`

- [ ] **Step 1: Replace the test imports and add fixture helpers**

Replace the import block in `agent/tools_intent_admission_test.go`:

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hypnguyen1209/V-ARTEX/db"
)
```

Append:

```go
func intentFixture(t *testing.T) (*ToolSet, *db.ExplorationStore) {
	t.Helper()
	d := testDB(t)
	task, err := d.CreateTask(fmt.Sprintf("intent-admission-%d", time.Now().UnixNano()), "test quota", nil, 0, 0)
	if err != nil {
		d.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = d.DeleteTask(task.ID)
		d.Close()
	})
	store := d.Exploration(task.ExplorationID)
	tools := NewToolSet(store, "planner")
	tools.SetTaskID(task.ID)
	return tools, store
}

func callAddIntent(t *testing.T, tools *ToolSet, input string) (string, bool) {
	t.Helper()
	result, err := tools.addIntent().Call(context.Background(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	return result.Flatten(), result.IsError
}
```

- [ ] **Step 2: Add failing behavior tests**

Append:

```go
func TestAddIntentRejectsMalformedJSONWithoutWriting(t *testing.T) {
	tools, store := intentFixture(t)
	tools.SetIntentBudget(plannerIntentLimit)
	text, isError := callAddIntent(t, tools, `{"summary":`)
	if !isError || !strings.Contains(text, "invalid") {
		t.Fatalf("result error=%v text=%q", isError, text)
	}
	if frontier, _ := store.Frontier(20); len(frontier) != 0 {
		t.Fatalf("malformed payload created %d intents", len(frontier))
	}
}

func TestAddIntentRejectsPriorityOutsideZeroToTen(t *testing.T) {
	for _, priority := range []int{-1, 11} {
		t.Run(fmt.Sprint(priority), func(t *testing.T) {
			tools, store := intentFixture(t)
			text, isError := callAddIntent(t, tools, fmt.Sprintf(`{"summary":"invalid priority","priority":%d}`, priority))
			if !isError || !strings.Contains(text, "between 0 and 10") {
				t.Fatalf("result error=%v text=%q", isError, text)
			}
			if frontier, _ := store.Frontier(20); len(frontier) != 0 {
				t.Fatal("invalid priority was persisted")
			}
		})
	}
}

func TestAddIntentPriorityZeroPersistsAsFive(t *testing.T) {
	tools, store := intentFixture(t)
	if text, isError := callAddIntent(t, tools, `{"summary":"default priority","priority":0}`); isError {
		t.Fatalf("add intent: %s", text)
	}
	frontier, err := store.Frontier(20)
	if err != nil || len(frontier) != 1 || frontier[0].Priority != 5 {
		t.Fatalf("frontier=%+v err=%v", frontier, err)
	}
}

func TestAddIntentEnforcesCumulativeRoundLimit(t *testing.T) {
	tools, store := intentFixture(t)
	tools.SetIntentBudget(plannerIntentLimit)
	for i := range plannerIntentLimit {
		if text, isError := callAddIntent(t, tools, fmt.Sprintf(`{"summary":"intent %d"}`, i)); isError {
			t.Fatalf("intent %d: %s", i, text)
		}
	}
	text, isError := callAddIntent(t, tools, `{"summary":"fifth"}`)
	if !isError || !strings.Contains(text, "remaining 0 of 4") {
		t.Fatalf("fifth error=%v text=%q", isError, text)
	}
	if frontier, _ := store.Frontier(20); len(frontier) != plannerIntentLimit {
		t.Fatalf("frontier=%d, want %d", len(frontier), plannerIntentLimit)
	}
}

func TestAddIntentConcurrentCallsNeverExceedFour(t *testing.T) {
	tools, store := intentFixture(t)
	tools.SetIntentBudget(plannerIntentLimit)
	start := make(chan struct{})
	errCh := make(chan error, 20)
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := tools.addIntent().Call(context.Background(), json.RawMessage(fmt.Sprintf(`{"summary":"concurrent %d"}`, i)), nil)
			if err != nil {
				errCh <- err
				return
			}
			if !result.IsError {
				accepted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("transport error: %v", err)
	}
	frontier, err := store.Frontier(100)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Load() != plannerIntentLimit || len(frontier) != plannerIntentLimit {
		t.Fatalf("accepted=%d frontier=%d, want %d", accepted.Load(), len(frontier), plannerIntentLimit)
	}
}

func TestAddIntentRejectsOversizedBatchBeforeAnyWrite(t *testing.T) {
	tools, store := intentFixture(t)
	tools.SetIntentBudget(plannerIntentLimit)
	text, isError := callAddIntent(t, tools, `{"intents":[{"summary":"1"},{"summary":"2"},{"summary":"3"},{"summary":"4"},{"summary":"5"}]}`)
	if !isError || !strings.Contains(text, "requested 5") {
		t.Fatalf("oversized error=%v text=%q", isError, text)
	}
	if frontier, _ := store.Frontier(20); len(frontier) != 0 {
		t.Fatal("oversized batch partially persisted")
	}
}

func TestAddIntentReleasesFailedBatchReservations(t *testing.T) {
	tools, store := intentFixture(t)
	tools.SetIntentBudget(plannerIntentLimit)
	text, isError := callAddIntent(t, tools, `{"intents":[{"summary":"kept"},{"summary":""},{"summary":"also kept"},{"summary":""}]}`)
	if isError || !strings.Contains(text, "errors") {
		t.Fatalf("partial batch error=%v text=%q", isError, text)
	}
	if text, isError = callAddIntent(t, tools, `{"intents":[{"summary":"replacement 1"},{"summary":"replacement 2"}]}`); isError {
		t.Fatalf("released capacity was not reusable: %s", text)
	}
	if frontier, _ := store.Frontier(20); len(frontier) != 4 {
		t.Fatalf("frontier=%d, want 4", len(frontier))
	}
}

func TestAddIntentWithoutPlannerBudgetRemainsUnlimited(t *testing.T) {
	tools, store := intentFixture(t)
	for i := range 5 {
		if text, isError := callAddIntent(t, tools, fmt.Sprintf(`{"summary":"human %d"}`, i)); isError {
			t.Fatalf("uncapped call %d: %s", i, text)
		}
	}
	if frontier, _ := store.Frontier(20); len(frontier) != 5 {
		t.Fatalf("uncapped frontier=%d, want 5", len(frontier))
	}
}

func TestAddIntentSchemaAdvertisesFourItemMaximum(t *testing.T) {
	tools := NewToolSet(nil, "planner")
	props := tools.addIntent().InputSchema()["properties"].(map[string]any)
	intents := props["intents"].(map[string]any)
	if got := intents["maxItems"]; got != plannerIntentLimit {
		t.Fatalf("maxItems=%v, want %d", got, plannerIntentLimit)
	}
}
```

- [ ] **Step 3: Run tool tests and verify RED**

```bash
go test ./agent -run 'TestAddIntent' -count=1
```

Expected: build failure for `SetIntentBudget`, followed by behavior failures until runtime enforcement exists.

- [ ] **Step 4: Attach an optional budget and validate priority**

Add `intentBudget` immediately after the existing `writes WriteCounts` field in `ToolSet`, then add its setter next to the other `ToolSet` setters in `agent/tools.go`:

```go
intentBudget *intentBudget

func (t *ToolSet) SetIntentBudget(limit int) {
	t.intentBudget = newIntentBudget(limit)
}
```

At the start of `addOneIntent`, after summary validation and before parent lookups:

```go
if it.Priority < 0 || it.Priority > 10 {
	return 0, fmt.Errorf("priority must be between 0 and 10")
}
```

Keep the existing `priority == 0` normalization to 5.

- [ ] **Step 5: Enforce schema, decoding, reservation, and accounting**

Change the `intents` schema:

```go
"intents": map[string]any{
	"type": "array", "maxItems": plannerIntentLimit,
	"description": "Preferred: new exploration directions processed in order. Each element uses summary/asset_ids/parent_ids/priority.",
	"items": map[string]any{"type": "object"},
},
```

Replace the ignored unmarshal and reserve before the write loop:

```go
if err := json.Unmarshal(in, &a); err != nil {
	return actool.Errorf("invalid add_intent JSON: " + err.Error()), nil
}
batch := len(a.Intents) > 0
items := a.Intents
if !batch {
	items = []intentItem{a.intentItem}
}
finishBudget, err := t.intentBudget.reserve(len(items))
if err != nil {
	return actool.Errorf(err.Error()), nil
}
successful := 0
defer func() { finishBudget(successful) }()
```

Increment `successful` only after `addOneIntent` returns a persisted ID:

```go
ids[i] = id
successful++
createdAny = true
```

- [ ] **Step 6: Run focused tests**

```bash
go test ./agent -run 'TestIntentBudget|TestAddIntent' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit runtime enforcement**

```bash
git add agent/tools.go agent/tools_intent_admission_test.go
GIT_AUTHOR_NAME='hypnguyen1209' GIT_AUTHOR_EMAIL='hypnguyen1209@gmail.com' GIT_COMMITTER_NAME='hypnguyen1209' GIT_COMMITTER_EMAIL='hypnguyen1209@gmail.com' git commit -m 'feat(agent): enforce planner intent admission'
```

### Task 3: Scope the cap to each planner round

**Files:**
- Modify: `agent/planner.go:355`
- Modify: `agent/tools_intent_admission_test.go`

- [ ] **Step 1: Add a failing wiring regression test**

Add `os` to the test import block, then append:

```go
func TestPlannerInstallsFreshIntentBudget(t *testing.T) {
	source, err := os.ReadFile("planner.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)
	newToolSet := strings.Index(body, `tsx := NewToolSet(ts, "planner")`)
	budget := strings.Index(body, `tsx.SetIntentBudget(plannerIntentLimit)`)
	if newToolSet < 0 || budget < newToolSet {
		t.Fatal("Planner.Plan must install a fresh budget after creating its per-round ToolSet")
	}
}
```

- [ ] **Step 2: Run the wiring test and verify RED**

```bash
go test ./agent -run TestPlannerInstallsFreshIntentBudget -count=1
```

Expected: FAIL because `Planner.Plan` does not install a budget.

- [ ] **Step 3: Install the fresh per-round budget**

In `Planner.Plan`, immediately after constructing the tool set:

```go
tsx := NewToolSet(ts, "planner")
tsx.SetIntentBudget(plannerIntentLimit)
```

- [ ] **Step 4: Run focused, package, and race verification**

```bash
go test ./agent -run 'TestIntentBudget|TestAddIntent|TestPlannerInstallsFreshIntentBudget' -count=1
go test ./agent -count=1
go test -race ./agent -run 'TestIntentBudget|TestAddIntent' -count=1
```

Expected: all commands PASS with no race report.

- [ ] **Step 5: Commit planner wiring**

```bash
git add agent/planner.go agent/tools_intent_admission_test.go
GIT_AUTHOR_NAME='hypnguyen1209' GIT_AUTHOR_EMAIL='hypnguyen1209@gmail.com' GIT_COMMITTER_NAME='hypnguyen1209' GIT_COMMITTER_EMAIL='hypnguyen1209@gmail.com' git commit -m 'feat(planner): cap intents per round'
```
