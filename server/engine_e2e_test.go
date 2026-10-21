package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
	"github.com/hypnguyen1209/V-ARTEX/agent"
	"github.com/hypnguyen1209/V-ARTEX/db"
)

func toolReply(id, name string, input []byte) (llm.Message, string, llm.Usage, error) {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{
		Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(input),
	}}}, "tool_use", llm.Usage{}, nil
}

func textReply(text string) (llm.Message, string, llm.Usage, error) {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(text)}}, "end_turn", llm.Usage{}, nil
}

func waitForTaskStatus(t *testing.T, manager *Manager, taskID, want string) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if got := manager.TaskStatus(taskID); got == want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("task %s status=%q, want %q", taskID, manager.TaskStatus(taskID), want)
		case <-ticker.C:
		}
	}
}

func onlyNodeID(t *testing.T, store *db.ExplorationStore, kind string) int64 {
	t.Helper()
	nodes, err := store.ListByKind(kind, 100)
	if err != nil {
		t.Fatalf("list kind %s: %v", kind, err)
	}
	var matching []*db.Node
	for _, node := range nodes {
		// Task creation seeds one origin fact; lifecycle assertions target agent output.
		if node.State != "origin" {
			matching = append(matching, node)
		}
	}
	if len(matching) != 1 {
		t.Fatalf("kind %s non-origin nodes=%d", kind, len(matching))
	}
	return matching[0].ID
}

func TestEngineLifecycleWithScriptedProviders(t *testing.T) {
	manager, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	manager.agentRuns.configure(false, defaultAgentConcurrencyLimit)
	engine := NewEngine(manager)
	ctx, cancel := context.WithCancel(t.Context())
	var task *Task
	t.Cleanup(func() {
		cancel()
		engine.Close()
		if task != nil {
			if _, err := manager.DeleteTask(task.ID, DeleteTaskOptions{}); err != nil {
				t.Errorf("delete task: %v", err)
			}
		}
		if err := manager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})

	task, err = manager.CreateTask("Deterministic engine fixture", "Prove the scripted lifecycle", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := task.Store.AddNode(db.KindGoal, map[string]any{
		"text": "Prove the deterministic orchestration lifecycle",
	}, 0, "open", "human", nil); err != nil {
		t.Fatal(err)
	}

	var plannerCalls, workerCalls atomic.Int32
	plannerSawFinding := make(chan struct{})
	plannerProvider := retestProvider{complete: func(_ context.Context, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		switch plannerCalls.Add(1) {
		case 1:
			return toolReply("plan-intent", "add_intent", []byte(`{"intents":[{"summary":"Exercise the deterministic worker lifecycle","priority":1}]}`))
		case 2:
			return textReply("Worker direction dispatched.")
		case 3:
			close(plannerSawFinding)
			return textReply("The finding arrived while its worker was still running; wait for the completion trigger.")
		case 4:
			goalID := onlyNodeID(t, task.Store, db.KindGoal)
			factID := onlyNodeID(t, task.Store, db.KindFact)
			input, err := json.Marshal(map[string]any{
				"goal_id": goalID, "evidence_id": factID, "reason": "The scripted evidence proves the fixture goal.",
			})
			if err != nil {
				t.Fatal(err)
			}
			return toolReply("prove-goal", "prove_goal", input)
		case 5:
			return textReply("The deterministic goal is proven.")
		default:
			return llm.Message{}, "", llm.Usage{}, fmt.Errorf("unexpected planner call")
		}
	}}

	workerProvider := retestProvider{complete: func(ctx context.Context, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		intentID := onlyNodeID(t, task.Store, db.KindIntent)
		switch workerCalls.Add(1) {
		case 1:
			input, _ := json.Marshal(map[string]any{
				"summary": "Observed deterministic fixture behavior", "detail": "The scripted worker executed the assigned intent.",
				"evidence": "fixture command: deterministic success", "confidence": "observed", "intent_id": intentID,
			})
			return toolReply("record-fact", "record_fact", input)
		case 2:
			input, _ := json.Marshal(map[string]any{
				"vulnclass": "TEST", "severity": "low", "summary": "Deterministic lifecycle finding",
				"evidence": "Scripted proof generated without network access.", "intent_id": intentID,
			})
			return toolReply("report-finding", "report_finding", input)
		case 3:
			select {
			case <-plannerSawFinding:
			case <-ctx.Done():
				return llm.Message{}, "", llm.Usage{}, ctx.Err()
			}
			return textReply("The fixture fact and finding were persisted.")
		default:
			return llm.Message{}, "", llm.Usage{}, fmt.Errorf("unexpected worker call")
		}
	}}

	planner := agent.NewPlanner(plannerProvider, "test", manager.dir, nil, 10000, 10)
	planner.SetNonStreaming(func() bool { return true })
	worker := agent.NewWorker(workerProvider, "test", manager.dir, nil, 10000, 10)
	worker.SetNonStreaming(func() bool { return true })
	engine.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) {
		return planner, worker
	})
	engine.debounce = 0
	engine.Run(ctx, task)

	waitForTaskStatus(t, manager, task.ID, "done")
	intents, err := task.Store.ListByKind(db.KindIntent, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 1 || intents[0].State != "done" {
		t.Fatalf("intents=%v, want one done intent", intents)
	}
	_ = onlyNodeID(t, task.Store, db.KindFact)
	findings, err := task.Store.ListByKind(db.KindFinding, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings=%d, want 1", len(findings))
	}
	if got := plannerCalls.Load(); got != 5 {
		t.Fatalf("planner calls=%d, want 5", got)
	}
	if got := workerCalls.Load(); got != 3 {
		t.Fatalf("worker calls=%d, want 3", got)
	}

	activity, _, err := task.Store.ActivityList(nil, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	succeeded := make(map[string]bool)
	for _, entry := range activity {
		if entry.Kind == "tool_result" && !entry.IsError {
			succeeded[entry.Tool] = true
		}
	}
	for _, tool := range []string{"add_intent", "record_fact", "report_finding", "prove_goal"} {
		if !succeeded[tool] {
			raw, _ := json.Marshal(activity)
			t.Fatalf("missing successful %s tool result in activity: %s", tool, raw)
		}
	}
}
