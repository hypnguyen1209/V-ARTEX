package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hypnguyen1209/V-ARTEX/db"
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

func TestAddIntentRejectsMalformedJSONWithoutWriting(t *testing.T) {
	tools, store := intentFixture(t)
	tools.SetIntentBudget(4)
	text, isError := callAddIntent(t, tools, `{"summary":`)
	if !isError || !strings.Contains(text, "invalid") {
		t.Fatalf("result=%q, isError=%v, want invalid JSON error", text, isError)
	}
	frontier, err := store.Frontier(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(frontier) != 0 {
		t.Fatalf("frontier has %d intents, want 0", len(frontier))
	}
}

func TestAddIntentRejectsPriorityOutsideZeroToTen(t *testing.T) {
	for _, priority := range []int{-1, 11} {
		t.Run(fmt.Sprint(priority), func(t *testing.T) {
			tools, store := intentFixture(t)
			text, isError := callAddIntent(t, tools, fmt.Sprintf(`{"summary":"invalid priority","priority":%d}`, priority))
			if !isError || !strings.Contains(text, "between 0 and 10") {
				t.Fatalf("result=%q, isError=%v, want priority error", text, isError)
			}
			frontier, err := store.Frontier(100)
			if err != nil {
				t.Fatal(err)
			}
			if len(frontier) != 0 {
				t.Fatalf("frontier has %d intents, want 0", len(frontier))
			}
		})
	}
}

func TestAddIntentPriorityZeroPersistsAsFive(t *testing.T) {
	tools, store := intentFixture(t)
	text, isError := callAddIntent(t, tools, `{"summary":"default priority","priority":0}`)
	if isError {
		t.Fatalf("add_intent failed: %s", text)
	}
	frontier, err := store.Frontier(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(frontier) != 1 {
		t.Fatalf("frontier has %d intents, want 1", len(frontier))
	}
	if frontier[0].Priority != 5 {
		t.Fatalf("priority=%d, want 5", frontier[0].Priority)
	}
}

func TestAddIntentEnforcesCumulativeRoundLimit(t *testing.T) {
	tools, store := intentFixture(t)
	tools.SetIntentBudget(4)
	for i := range 4 {
		if text, isError := callAddIntent(t, tools, fmt.Sprintf(`{"summary":"intent %d"}`, i)); isError {
			t.Fatalf("add_intent failed: %s", text)
		}
	}
	text, isError := callAddIntent(t, tools, `{"summary":"fifth"}`)
	if !isError || !strings.Contains(text, "remaining 0 of 4") {
		t.Fatalf("result=%q, isError=%v, want exhausted budget error", text, isError)
	}
	frontier, err := store.Frontier(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(frontier) != 4 {
		t.Fatalf("frontier has %d intents, want 4", len(frontier))
	}
}

func TestAddIntentConcurrentCallsNeverExceedFour(t *testing.T) {
	tools, store := intentFixture(t)
	tools.SetIntentBudget(4)
	start := make(chan struct{})
	transportErrors := make(chan error, 20)
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := tools.addIntent().Call(context.Background(), json.RawMessage(fmt.Sprintf(`{"summary":"concurrent %d"}`, i)), nil)
			if err != nil {
				transportErrors <- err
				return
			}
			if !result.IsError {
				accepted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(transportErrors)
	for err := range transportErrors {
		t.Errorf("transport error: %v", err)
	}
	if got := accepted.Load(); got != 4 {
		t.Fatalf("accepted=%d, want 4", got)
	}
	frontier, err := store.Frontier(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(frontier) != 4 {
		t.Fatalf("frontier has %d intents, want 4", len(frontier))
	}
}

func TestAddIntentRejectsOversizedBatchBeforeAnyWrite(t *testing.T) {
	tools, store := intentFixture(t)
	tools.SetIntentBudget(4)
	text, isError := callAddIntent(t, tools, `{"intents":[{"summary":"1"},{"summary":"2"},{"summary":"3"},{"summary":"4"},{"summary":"5"}]}`)
	if !isError || !strings.Contains(text, "requested 5") {
		t.Fatalf("result=%q, isError=%v, want oversized batch error", text, isError)
	}
	frontier, err := store.Frontier(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(frontier) != 0 {
		t.Fatalf("frontier has %d intents, want 0", len(frontier))
	}
}

func TestAddIntentReleasesFailedBatchReservations(t *testing.T) {
	tools, store := intentFixture(t)
	tools.SetIntentBudget(4)
	text, isError := callAddIntent(t, tools, `{"intents":[{"summary":"kept"},{"summary":""},{"summary":"also kept"},{"summary":""}]}`)
	if isError || !strings.Contains(text, "errors") {
		t.Fatalf("result=%q, isError=%v, want per-item errors", text, isError)
	}
	if text, isError := callAddIntent(t, tools, `{"intents":[{"summary":"replacement 1"},{"summary":"replacement 2"}]}`); isError {
		t.Fatalf("replacement batch failed: %s", text)
	}
	frontier, err := store.Frontier(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(frontier) != 4 {
		t.Fatalf("frontier has %d intents, want 4", len(frontier))
	}
}

func TestAddIntentWithoutPlannerBudgetRemainsUnlimited(t *testing.T) {
	tools, store := intentFixture(t)
	for i := range 5 {
		if text, isError := callAddIntent(t, tools, fmt.Sprintf(`{"summary":"human %d"}`, i)); isError {
			t.Fatalf("add_intent failed: %s", text)
		}
	}
	frontier, err := store.Frontier(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(frontier) != 5 {
		t.Fatalf("frontier has %d intents, want 5", len(frontier))
	}
}

func TestAddIntentSchemaAdvertisesFourItemMaximum(t *testing.T) {
	tools := NewToolSet(nil, "planner")
	properties := tools.addIntent().InputSchema()["properties"].(map[string]any)
	intents := properties["intents"].(map[string]any)
	if got := intents["maxItems"]; got != plannerIntentLimit {
		t.Fatalf("maxItems=%v, want %d", got, plannerIntentLimit)
	}
}

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
