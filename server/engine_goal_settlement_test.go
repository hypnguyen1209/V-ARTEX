package server

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hypnguyen1209/V-ARTEX/agent"
	"github.com/hypnguyen1209/V-ARTEX/db"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

func TestSettlementKindsRemainDistinct(t *testing.T) {
	goal := NewEngine(nil)
	if !goal.markGoalSettling("goal") || !goal.isSettling("goal") || !goal.isGoalSettling("goal") || goal.isTaskTimeoutSettling("goal") {
		t.Fatal("goal settlement kind was not retained")
	}
	timed := NewEngine(nil)
	if !timed.markSettling("timeout") || !timed.isSettling("timeout") || timed.isGoalSettling("timeout") || !timed.isTaskTimeoutSettling("timeout") {
		t.Fatal("task-timeout settlement kind was not retained")
	}
	if goal.markSettling("goal") || !goal.isGoalSettling("goal") || timed.markGoalSettling("timeout") || !timed.isTaskTimeoutSettling("timeout") {
		t.Fatal("later settlement overwrote the first owner")
	}
	legacy := NewEngine(nil)
	legacy.settling.Store("legacy", true)
	if !legacy.isSettling("legacy") || legacy.markGoalSettling("legacy") {
		t.Fatal("legacy settlement lost its ownership")
	}
}

func TestGoalSettlementExecutionAdmission(t *testing.T) {
	e := NewEngine(nil)
	const taskID = "task"
	if !e.beginTaskExecution(taskID) || e.inflightCount(taskID) != 1 {
		t.Fatal("execution was not registered before settlement")
	}
	if !e.markGoalSettling(taskID) {
		t.Fatal("goal settlement was not installed")
	}
	if e.beginTaskExecution(taskID) || e.inflightCount(taskID) != 1 {
		t.Fatal("execution entered after the settlement gate was installed")
	}
	e.decInflight(taskID)
	if e.inflightCount(taskID) != 0 {
		t.Fatal("already-admitted execution did not drain during settlement")
	}
	if !e.beginTaskOperation(taskID) || e.inflightCount(taskID) != 1 {
		t.Fatal("settlement blocked coordinator or administrative work")
	}
	e.decInflight(taskID)
}

func TestGoalSettlementConcurrentExecutionAdmission(t *testing.T) {
	for range 1000 {
		e := NewEngine(nil)
		start := make(chan struct{})
		admitted := make(chan bool, 1)
		inflightAtSettlement := make(chan int64, 1)
		go func() {
			<-start
			admitted <- e.beginTaskExecution("task")
		}()
		go func() {
			<-start
			e.markGoalSettling("task")
			inflightAtSettlement <- e.inflightCount("task")
		}()
		close(start)
		accepted, count := <-admitted, <-inflightAtSettlement
		if accepted {
			e.decInflight("task")
			if count != 1 {
				t.Fatal("settlement missed an admitted execution")
			}
		} else if count != 0 {
			t.Fatal("rejected execution changed the inflight count")
		}
	}
}

func TestGoalSettlementWaitsForExecutionAdmissionLock(t *testing.T) {
	e := NewEngine(nil)
	e.deleteMu.RLock()
	started, done := make(chan struct{}), make(chan struct{})
	go func() {
		close(started)
		e.markGoalSettling("task")
		close(done)
	}()
	<-started
	select {
	case <-done:
		e.deleteMu.RUnlock()
		t.Fatal("settlement bypassed the execution admission lock")
	case <-time.After(20 * time.Millisecond):
	}
	e.deleteMu.RUnlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("settlement did not proceed after execution admission unlocked")
	}
}

func TestGoalSettlementAdmissionRejectionKeepsWorkerLoopAlive(t *testing.T) {
	task := &Task{ID: "task", Status: "running"}
	m := &Manager{tasks: map[string]*Task{task.ID: task}}
	e := NewEngine(m)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var resolutions atomic.Int32
	resumed := make(chan struct{})
	e.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) {
		if resolutions.Add(1) == 2 {
			close(resumed)
			cancel()
			return nil, nil
		}
		return nil, &agent.Worker{}
	})
	// TaskStatus holds m.mu while waiting for lifecycleMu, after the cheap settlement check.
	task.lifecycleMu.Lock()
	done := make(chan struct{})
	go func() {
		e.workerLoop(ctx, task, "worker-test")
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for m.mu.TryLock() {
		m.mu.Unlock()
		if time.Now().After(deadline) {
			e.markGoalSettling(task.ID)
			task.lifecycleMu.Unlock()
			t.Fatal("worker did not reach the status check")
		}
		time.Sleep(time.Millisecond)
	}
	e.markGoalSettling(task.ID)
	task.lifecycleMu.Unlock()
	select {
	case <-done:
		t.Fatal("worker exited after settlement rejected its admission")
	case <-time.After(20 * time.Millisecond):
	}
	e.settling.Delete(task.ID)
	select {
	case <-resumed:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not resume after settlement was cleared")
	}
	<-done
}

func TestCompleteGoalMetWaitsForWorkerBeforeDone(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	task, err := m.CreateTask("Goal completion", "Persist worker results", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	e := NewEngine(m)
	counter := e.inflightCounter(task.ID)
	atomic.StoreInt64(counter, 2) // Current planner and one admitted worker.
	execCtx := e.execContextFor(t.Context(), task.ID)
	done := make(chan struct{})
	go func() {
		e.completeGoalMetWithin(t.Context(), task, "goal proven", time.Second)
		close(done)
	}()
	time.Sleep(30 * time.Millisecond)
	if m.TaskStatus(task.ID) == "done" {
		t.Error("task completed before its worker drained")
	}
	atomic.StoreInt64(counter, 1)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("goal completion did not return after worker drained")
	}
	if m.TaskStatus(task.ID) != "done" || !e.isGoalSettling(task.ID) {
		t.Fatal("task did not complete with its goal admission gate retained")
	}
	if execCtx.Err() != nil {
		t.Fatalf("normal worker drain cancelled execution: %v", context.Cause(execCtx))
	}
}

func TestCompleteGoalMetCancelsOnlyAfterGrace(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	task, err := m.CreateTask("Goal completion", "Stop a worker after grace", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	e := NewEngine(m)
	counter := e.inflightCounter(task.ID)
	atomic.StoreInt64(counter, 2)
	execCtx := e.execContextFor(t.Context(), task.ID)
	statusAtCancel := make(chan string, 1)
	go func() {
		<-execCtx.Done()
		statusAtCancel <- m.TaskStatus(task.ID)
		time.Sleep(20 * time.Millisecond)
		atomic.StoreInt64(counter, 1)
	}()
	start := time.Now()
	e.completeGoalMetWithin(execCtx, task, "goal proven", 20*time.Millisecond)
	if time.Since(start) < 20*time.Millisecond {
		t.Fatal("worker was cancelled before its grace elapsed")
	}
	if m.TaskStatus(task.ID) != "done" || <-statusAtCancel != "done" {
		t.Fatal("completion was not persisted before cancelling the worker")
	}
	if code, _, _, ok := agent.AbortReason(execCtx); !ok || code != "goal_met" {
		t.Fatalf("abort code=%q ok=%v, want goal_met", code, ok)
	}
	if !e.isGoalSettling(task.ID) || e.isTaskTimeoutSettling(task.ID) {
		t.Fatal("goal hard cancellation was classified as task timeout settlement")
	}
	if e.inflightCount(task.ID) != 1 {
		t.Fatal("self-cancellation skipped bounded worker cleanup")
	}
}

func TestGoalSettlementExplicitAdmissionRevivesDoneTask(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	defer restoreConcurrencySetting(t, m)()
	if err := m.SetConcurrency(false, defaultConcurrencyLimit); err != nil {
		t.Fatal(err)
	}
	task, err := m.CreateTask("Completed goal", "Accept explicit follow-up work", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	if err := m.SetTaskStatus(task.ID, "done"); err != nil {
		t.Fatal(err)
	}
	s := newAdmissionTestServer(m, nil)
	defer s.engine.Close()
	s.engine.execContextFor(t.Context(), task.ID)
	s.engine.markGoalSettling(task.ID)
	s.engine.cancelExec(task.ID, agent.AbortGoalMet)
	queued, err := s.admitTask(task, "resume")
	if err != nil {
		t.Fatal(err)
	}
	if queued || m.TaskStatus(task.ID) != "running" || s.engine.isSettling(task.ID) {
		t.Fatalf("explicit goal revival remained gated: queued=%v status=%q kind=%q", queued, m.TaskStatus(task.ID), s.engine.settlementKind(task.ID))
	}
	if ctx := s.engine.execContextFor(t.Context(), task.ID); ctx.Err() != nil {
		t.Fatalf("revived goal execution remained cancelled: %v", context.Cause(ctx))
	}
}

func TestCompleteGoalMetFailurePreservesTimeoutOwnership(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	task, err := m.CreateTask("Timeout-owned task", "Keep timeout settlement after failed goal write", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	dsn, _, err := db.DSN()
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup.Close()
	defer cleanup.DeleteTask(mustTaskID(t, task.ID))
	e := NewEngine(m)
	e.markSettling(task.ID)
	if err := m.pg.Close(); err != nil {
		t.Fatal(err)
	}
	e.completeGoalMetWithin(t.Context(), task, "goal proven", 0)
	if !e.isTaskTimeoutSettling(task.ID) {
		t.Fatal("failed goal completion removed timeout settlement ownership")
	}
}

func TestGoalSettlementAbortedDeleteReopensInterruptedCompletion(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	task, err := m.CreateTask("Interrupted goal completion", "Recover after deletion fails", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	if err := m.SetTaskStatus(task.ID, "running"); err != nil {
		t.Fatal(err)
	}
	goalID, err := task.Store.AddNode(db.KindGoal, map[string]any{"summary": "Prove the task goal"}, 1, "open", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	evidenceID, err := task.Store.AddNode(db.KindFact, map[string]any{"summary": "Goal evidence"}, 1, "confirmed", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := json.Marshal(map[string]any{"goal_id": goalID, "evidence_id": evidenceID})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	provider := retestProvider{complete: func(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		if calls.Add(1) == 1 {
			return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "prove", Name: "prove_goal", Input: proof}}}, "tool_use", llm.Usage{}, nil
		}
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("Goal proven.")}}, "end_turn", llm.Usage{}, nil
	}}
	planner := agent.NewPlanner(provider, "test", m.dir, nil, 10000, 10)
	planner.SetNonStreaming(func() bool { return true })
	e := NewEngine(m)
	e.debounce = 0
	var resolutions atomic.Int32
	e.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) {
		if resolutions.Add(1) == 1 {
			return planner, nil
		}
		return nil, nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	e.execContextFor(ctx, task.ID)
	counter := e.inflightCounter(task.ID)
	atomic.StoreInt64(counter, 1) // One admitted worker; the planner registers itself.
	stopped := make(chan struct{})
	go func() {
		e.plannerLoop(ctx, task)
		close(stopped)
	}()
	defer func() {
		cancel()
		<-stopped
	}()
	task.Notify()
	until := time.Now().Add(time.Second)
	for !e.isGoalSettling(task.ID) {
		if time.Now().After(until) {
			t.Fatal("goal completion did not install settlement")
		}
		time.Sleep(time.Millisecond)
	}
	if !e.BeginDelete(task.ID) {
		t.Fatal("delete barrier was not installed during goal drain")
	}
	e.AbortDelete(task.ID, false)
	until = time.Now().Add(time.Second)
	for e.isGoalSettling(task.ID) {
		if time.Now().After(until) {
			t.Fatal("cancelled goal coordinator did not clear its settlement")
		}
		time.Sleep(time.Millisecond)
	}
	if e.IsDeleting(task.ID) || e.IsPaused(task.ID) || e.isSettling(task.ID) || isTerminalStatus(m.TaskStatus(task.ID)) {
		t.Fatal("aborted deletion left the interrupted task gated or terminal")
	}
	if !e.beginTaskExecution(task.ID) {
		t.Fatal("execution could not resume after deletion was aborted")
	}
	e.decInflight(task.ID)
	recovered := e.execContextFor(ctx, task.ID)
	if recovered.Err() != nil {
		t.Fatalf("aborted deletion retained cancelled execution: %v", context.Cause(recovered))
	}
	atomic.AddInt64(counter, -1) // Release the worker only after delete abort and recovery.
	until = time.Now().Add(2 * time.Second)
	for e.inflightCount(task.ID) != 0 {
		if time.Now().After(until) {
			t.Fatal("old goal coordinator did not return after its worker drained")
		}
		time.Sleep(time.Millisecond)
	}
	if m.TaskStatus(task.ID) != "running" || recovered.Err() != nil {
		t.Fatalf("old goal coordinator changed the recovered run: status=%q cause=%v", m.TaskStatus(task.ID), context.Cause(recovered))
	}
}

func TestGoalSettlementAbortedDeletePreservesTimeoutAndPause(t *testing.T) {
	t.Run("timeout ownership", func(t *testing.T) {
		e := NewEngine(nil)
		e.markSettling("task")
		e.BeginDelete("task")
		e.AbortDelete("task", false)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		e.completeGoalMetWithin(ctx, &Task{ID: "task"}, "goal proven", 0)
		if !e.isTaskTimeoutSettling("task") || e.IsDeleting("task") || e.IsPaused("task") {
			t.Fatal("aborted deletion removed timeout ownership or retained its delete barrier")
		}
	})
	t.Run("existing pause", func(t *testing.T) {
		e := NewEngine(nil)
		e.Pause("task", agent.AbortPausedByUser)
		e.markGoalSettling("task")
		e.BeginDelete("task")
		e.AbortDelete("task", true)
		if !e.IsPaused("task") || e.IsDeleting("task") || !e.isGoalSettling("task") {
			t.Fatal("aborted deletion changed goal ownership or failed to retain the pause")
		}
		if ctx := e.execContextFor(t.Context(), "task"); ctx.Err() == nil {
			t.Fatal("aborted deletion admitted live execution despite the retained pause")
		}
	})
}

func TestCompleteGoalMetFailureRetainsExpiredDeadlineCoordinator(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	task, err := m.CreateTask("Expired goal completion", "Recover timeout after failed goal write", nil, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	dsn, _, err := db.DSN()
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup.Close()
	defer cleanup.DeleteTask(mustTaskID(t, task.ID))
	e := NewEngine(m)
	defer e.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e.deadline.Store(task.ID, time.Now().Add(-time.Second).Unix())
	counter := e.inflightCounter(task.ID)
	atomic.StoreInt64(counter, 2)
	completed := make(chan struct{})
	go func() {
		e.completeGoalMetWithin(ctx, task, "goal proven", time.Second)
		close(completed)
	}()
	until := time.Now().Add(time.Second)
	for !e.isGoalSettling(task.ID) {
		if time.Now().After(until) {
			t.Fatal("goal completion did not install settlement")
		}
		time.Sleep(time.Millisecond)
	}
	e.startDeadlineCoordinator(ctx, task)
	e.runtimeMu.Lock()
	rt := e.runtimes[task.ID]
	e.runtimeMu.Unlock()
	stopped := make(chan struct{})
	go func() {
		rt.wg.Wait()
		close(stopped)
	}()
	time.Sleep(20 * time.Millisecond)
	if err := m.pg.Close(); err != nil {
		t.Fatal(err)
	}
	atomic.StoreInt64(counter, 1)
	<-completed
	atomic.StoreInt64(counter, 0)
	until = time.Now().Add(3 * time.Second)
	for !e.isTaskTimeoutSettling(task.ID) {
		select {
		case <-stopped:
			t.Fatal("expired deadline coordinator exited after losing to goal settlement")
		default:
		}
		if time.Now().After(until) {
			t.Fatal("expired deadline coordinator did not reacquire timeout settlement")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("deadline coordinator did not stop after cancellation")
	}
	if e.inflightCount(task.ID) != 0 {
		t.Fatal("deadline coordinator retained task operations after cancellation")
	}
}

func TestGoalSettlementHardCancelStopsIntent(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	task, err := m.CreateTask("Goal completion", "Stop a worker after grace", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	iid, err := task.Store.AddNode(db.KindIntent, map[string]any{"summary": "Running worker"}, 1, "running", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := task.Store.GetNode(iid)
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(m)
	provider := retestProvider{complete: func(ctx context.Context, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		e.markGoalSettling(task.ID)
		if err := m.SetTaskStatus(task.ID, "done"); err != nil {
			t.Fatal(err)
		}
		e.cancelExec(task.ID, agent.AbortGoalMet)
		return llm.Message{}, "", llm.Usage{}, ctx.Err()
	}}
	worker := agent.NewWorker(provider, "test", m.dir, nil, 10000, 10)
	worker.SetNonStreaming(func() bool { return true })
	e.runIntent(t.Context(), task, "worker-test", worker, intent, "", "")
	intent, err = task.Store.GetNode(iid)
	if err != nil {
		t.Fatal(err)
	}
	if intent.State != "stopped" {
		t.Fatalf("goal hard cancellation left intent %q, want stopped", intent.State)
	}
}

func TestSettlementCanceledExecutionContextIsRetained(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  settlementKind
		cause *agent.AbortCause
		code  string
	}{
		{"goal", settlementGoalMet, agent.AbortGoalMet, "goal_met"},
		{"timeout", settlementTaskTimeout, agent.AbortSettleDrainTimeout, "settle_drain_timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEngine(nil)
			e.execContextFor(t.Context(), "task")
			e.markSettlement("task", tc.kind)
			e.cancelExec("task", tc.cause)
			ctx := e.execContextFor(t.Context(), "task")
			if code, _, _, ok := agent.AbortReason(ctx); ctx.Err() == nil || !ok || code != tc.code {
				t.Fatalf("settlement recreated execution: err=%v code=%q ok=%v", ctx.Err(), code, ok)
			}
			if tc.kind == settlementTaskTimeout {
				e.resetTimeoutRevival("task")
				if fresh := e.execContextFor(t.Context(), "task"); fresh.Err() != nil {
					t.Fatalf("timeout revival retained cancelled execution: %v", context.Cause(fresh))
				}
			}
		})
	}
}

func TestGoalSettlementDelayedClaimedWorkerClassification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   settlementKind
		cause  *agent.AbortCause
		status string
		want   string
	}{
		{"goal", settlementGoalMet, agent.AbortGoalMet, "done", "stopped"},
		{"timeout", settlementTaskTimeout, agent.AbortSettleDrainTimeout, "timeout", "exhausted"},
		{"paused goal", settlementGoalMet, agent.AbortPausedByUser, "", "open"},
		{"deleted goal", settlementGoalMet, agent.AbortTaskDeleted, "", "open"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := NewManager(t.TempDir(), "")
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			task, err := m.CreateTask("Delayed worker", "Honor settlement cancellation before execution", nil, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer m.DeleteTask(task.ID, DeleteTaskOptions{})
			iid, err := task.Store.AddNode(db.KindIntent, map[string]any{"summary": "Delayed claimed worker"}, 1, "running", "test", nil)
			if err != nil {
				t.Fatal(err)
			}
			intent, err := task.Store.GetNode(iid)
			if err != nil {
				t.Fatal(err)
			}
			e := NewEngine(m)
			e.execContextFor(t.Context(), task.ID)
			e.markSettlement(task.ID, tc.kind)
			if tc.status != "" {
				if err := m.SetTaskStatus(task.ID, tc.status); err != nil {
					t.Fatal(err)
				}
			}
			switch tc.cause {
			case agent.AbortPausedByUser:
				e.Pause(task.ID, tc.cause)
			case agent.AbortTaskDeleted:
				e.BeginDelete(task.ID)
			default:
				e.cancelExec(task.ID, tc.cause)
			}
			calls := 0
			provider := retestProvider{complete: func(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
				calls++
				return llm.Message{}, "", llm.Usage{}, errors.New("unexpected LLM call after hard cancellation")
			}}
			worker := agent.NewWorker(provider, "test", m.dir, nil, 10000, 10)
			worker.SetNonStreaming(func() bool { return true })
			e.runIntent(t.Context(), task, "worker-test", worker, intent, "", "")
			intent, err = task.Store.GetNode(iid)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 0 || intent.State != tc.want {
				t.Fatalf("delayed worker made %d LLM calls and ended %q, want 0 calls and %q", calls, intent.State, tc.want)
			}
		})
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

func TestGoalSettlementDrainRespectsCancellation(t *testing.T) {
	e := NewEngine(nil)
	atomic.StoreInt64(e.inflightCounter("task"), 2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if e.waitForInflightAtMost(ctx, "task", 1, time.Second) {
		t.Fatal("cancelled worker drain reported success")
	}
	if time.Since(start) >= 500*time.Millisecond {
		t.Fatal("cancelled worker drain waited for the grace period")
	}
}

func TestGoalSettlementDrainAlreadyComplete(t *testing.T) {
	e := NewEngine(nil)
	if !e.waitForInflightAtMost(context.Background(), "task", 1, 0) {
		t.Fatal("already drained task reported a timeout")
	}
}

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
						e.markGoalSettling(task.ID)
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
