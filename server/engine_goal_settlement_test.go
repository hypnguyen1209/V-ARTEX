package server

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
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
	h := steerHooks{goalSettling: func() bool { return true }, goalSettlePrompted: prompted, nudges: &atomic.Int64{}, limit: defaultEmptyTurnNudges}
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

type goalSettlementGuard struct {
	fakeHooks
	calls int
}

func (g *goalSettlementGuard) PreToolUse(context.Context, string, []byte) (bool, string, []byte) {
	g.calls++
	return true, "guard denied write-back", []byte("guard input")
}

func TestGoalSettlementHookRespectsInnerHooks(t *testing.T) {
	t.Run("write-back still passes through guard", func(t *testing.T) {
		guard := &goalSettlementGuard{}
		h := steerHooks{
			inner:              guard,
			goalSettling:       func() bool { return true },
			goalSettlePrompted: &atomic.Bool{},
			drain:              func() (string, bool) { return "start a new probe", true },
		}
		blocked, msg, input := h.PreToolUse(context.Background(), "record_fact", nil)
		if !blocked || msg != "guard denied write-back" || string(input) != "guard input" || guard.calls != 1 {
			t.Fatalf("write-back bypassed guard: blocked=%v msg=%q input=%q calls=%d", blocked, msg, input, guard.calls)
		}
	})

	for _, inner := range []fakeHooks{{prevent: true, msg: "guard hard stop"}, {blocking: []string{"guard continuation"}}} {
		prompted := &atomic.Bool{}
		h := steerHooks{inner: inner, goalSettling: func() bool { return true }, goalSettlePrompted: prompted}
		prevent, blocking, msg := h.Stop(context.Background(), nil)
		if prevent != inner.prevent || len(blocking) != len(inner.blocking) || msg != inner.msg || prompted.Load() {
			t.Fatalf("inner stop changed: prevent=%v blocking=%v msg=%q prompted=%v", prevent, blocking, msg, prompted.Load())
		}
	}
}

func TestGoalSettlementAfterEmptyTurnContinuation(t *testing.T) {
	for _, name := range []string{"without transcript", "with transcript", "task cancelled", "work killed", "not settling", "wrap-up error"} {
		t.Run(name, func(t *testing.T) {
			persist := name == "with transcript"
			m, err := NewManager(t.TempDir(), "")
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			task, err := m.CreateTask("Goal wrap-up", "Persist identified results", nil, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer m.DeleteTask(task.ID, DeleteTaskOptions{})
			iid, err := task.Store.AddNode(db.KindIntent, map[string]any{"summary": "Keep identified evidence"}, 1, "running", "test", nil)
			if err != nil {
				t.Fatal(err)
			}
			intent, err := task.Store.GetNode(iid)
			if err != nil {
				t.Fatal(err)
			}
			e := NewEngine(m)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			provider := retestProvider{complete: func(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
				calls++
				blob, _ := json.Marshal(req.Messages)
				switch calls {
				case 1:
					return assistantThinking("identified evidence marker"), "end_turn", llm.Usage{}, nil
				case 2:
					if !strings.Contains(string(blob), emptyTurnNudge) {
						t.Fatal("thinking-only turn did not receive the empty-turn nudge")
					}
					if name != "not settling" {
						e.markSettling(task.ID)
					}
					if name == "task cancelled" {
						cancel()
					}
					if name == "work killed" {
						if err := e.KillWork(iid); err != nil {
							t.Fatal(err)
						}
					}
					return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("Partial summary")}}, "end_turn", llm.Usage{}, nil
				case 3:
					if strings.Count(string(blob), goalMetWorkerWrapUpPrompt) != 1 {
						t.Fatalf("worker did not receive exactly one wrap-up instruction: %s", blob)
					}
					if persist && !strings.Contains(string(blob), "identified evidence marker") {
						t.Fatal("wrap-up lost the existing transcript")
					}
					if name == "wrap-up error" {
						return llm.Message{}, "", llm.Usage{}, errors.New("wrap-up model failure")
					}
					return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "write-back", Name: "record_fact", Input: json.RawMessage(`{"summary":"Saved identified evidence"}`)}}}, "tool_use", llm.Usage{}, nil
				default:
					return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("Identified evidence saved.")}}, "end_turn", llm.Usage{}, nil
				}
			}}
			var tx *transcript.Store
			if persist {
				tx = transcript.NewStore(filepath.Join(t.TempDir(), "transcripts"))
			}
			worker := agent.NewWorker(provider, "test", m.dir, tx, 10000, 10)
			worker.SetNonStreaming(func() bool { return true })
			e.runIntent(ctx, task, "worker-test", worker, intent, "", "")
			if name == "task cancelled" || name == "work killed" || name == "not settling" {
				if calls != 2 {
					t.Fatalf("unexpected wrap-up execution: model calls=%d, want 2", calls)
				}
				return
			}
			if name == "wrap-up error" {
				intent, err := task.Store.GetNode(iid)
				if err != nil {
					t.Fatal(err)
				}
				if calls != 3 || intent.State != "blocked" {
					t.Fatalf("wrap-up failure lost: calls=%d state=%q", calls, intent.State)
				}
				return
			}
			if calls != 4 {
				t.Fatalf("worker silently completed without a wrap-up opportunity: model calls=%d, want 4", calls)
			}
			nodes, err := task.Store.Nodes(100)
			if err != nil {
				t.Fatal(err)
			}
			facts := 0
			for _, node := range nodes {
				if node.Kind == db.KindFact && strings.Contains(string(node.Payload), "Saved identified evidence") {
					facts++
				}
			}
			if facts != 1 {
				t.Fatalf("durable wrap-up facts=%d, want 1", facts)
			}
		})
	}
}
