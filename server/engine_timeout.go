package server

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/hypnguyen1209/V-ARTEX/agent"
	"github.com/hypnguyen1209/V-ARTEX/db"
)

// Task timeout coordinator (docs/task-timeout-and-wrap-up-design.md, sections 4/8.5).
// Absolute wall clock: one timer goroutine per timed task drives ordered settlement:
// 1. settling; 2. workers stop claiming; 3. planner discards normal notifications;
// 4. drain running workers within grace; 5. final planner judgment; 6. guarded terminal state.

const (
	settleDrainGrace     = 90 * time.Second // Maximum worker grace period before hard cancellation.
	deadlinePollInterval = 2 * time.Second  // Poll interval while deadline is unstamped or LLM unavailable.
	deadlineMaxSleep     = 30 * time.Second // Maximum sleep before rechecking terminal state.
)

// ---------- Settlement state ----------

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
	e.deleteMu.Lock()
	defer e.deleteMu.Unlock()
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

// ---------- Running worker.Execute + planner.Plan count for draining ----------

func (e *Engine) inflightCounter(taskID string) *int64 {
	v, _ := e.inflight.LoadOrStore(taskID, new(int64))
	return v.(*int64)
}

// beginTaskOperation atomically registers a task-owned operation unless deletion
// has already installed its barrier. The delete handler can therefore wait for
// inflight==0 without a check-then-start race recreating files after cleanup.
func (e *Engine) beginTaskOperation(taskID string) bool {
	e.deleteMu.RLock()
	defer e.deleteMu.RUnlock()
	if e.closed || e.IsDeleting(taskID) {
		return false
	}
	e.operationWG.Add(1)
	atomic.AddInt64(e.inflightCounter(taskID), 1)
	return true
}

// beginTaskExecution registers normal execution before settlement can begin.
func (e *Engine) beginTaskExecution(taskID string) bool {
	e.deleteMu.RLock()
	defer e.deleteMu.RUnlock()
	if e.closed || e.IsDeleting(taskID) || e.isSettling(taskID) {
		return false
	}
	e.operationWG.Add(1)
	atomic.AddInt64(e.inflightCounter(taskID), 1)
	return true
}

func (e *Engine) decInflight(taskID string) {
	atomic.AddInt64(e.inflightCounter(taskID), -1)
	e.operationWG.Done()
}
func (e *Engine) inflightCount(taskID string) int64 {
	return atomic.LoadInt64(e.inflightCounter(taskID))
}

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

// ---------- deadline ----------

// taskDeadline returns the task's absolute deadline (unix). Prefers the in-process
// map (stamped this session); falls back to the DB-loaded value (restart), seeding
// the map. 0 = no timeout / not yet stamped.
func (e *Engine) taskDeadline(t *Task) int64 {
	if v, ok := e.deadline.Load(t.ID); ok {
		return v.(int64)
	}
	deadlineAt := t.lifecycleSnapshot().DeadlineAt
	if deadlineAt > 0 {
		e.deadline.Store(t.ID, deadlineAt)
		return deadlineAt
	}
	return 0
}

// resetTimeoutRevival clears only the per-run timeout state after PostgreSQL has
// atomically committed timeout -> running and reset first_run_at/deadline_at. The
// configured TimeoutSeconds remains on Task, so the next real Planner/Worker run
// stamps a fresh full budget. coordStarted is reset because the coordinator that
// produced the timeout has already completed (or is in its final return path).
func (e *Engine) resetTimeoutRevival(taskID string) {
	e.settling.Delete(taskID)
	e.deadline.Delete(taskID)
	e.stamped.Delete(taskID)
	e.coordStarted.Delete(taskID)
}

func (e *Engine) resetGoalRevival(taskID string) {
	e.settling.CompareAndDelete(taskID, settlementGoalMet)
}

// stampFirstRun records first_run_at + deadline_at on the FIRST real run (LLM ready)
// of a timeout task, once per process. No-op when the task has no timeout.
func (e *Engine) stampFirstRun(t *Task) {
	if t.TimeoutSeconds <= 0 {
		return
	}
	if _, loaded := e.stamped.LoadOrStore(t.ID, true); loaded {
		return
	}
	dl, err := e.m.StampTaskFirstRun(t.ID)
	if err != nil {
		log.Printf("[deadline] task %s Failed to stamp first_run: %v", t.ID, err)
		e.stamped.Delete(t.ID) // Permit retry next time.
		return
	}
	if dl > 0 {
		e.deadline.Store(t.ID, dl)
		log.Printf("[deadline] task %s First run; deadline %s", t.ID, time.Unix(dl, 0).Format("2006-01-02 15:04:05"))
	}
}

// clockCtx layers the task's TaskClock (absolute deadline) onto a run's context so
// worker/planner can clamp their wall-clock budget and pick per-run vs task-timeout
// wrap-up words. final marks the coordinator-driven terminal planner round.
func (e *Engine) clockCtx(base context.Context, t *Task, final bool) context.Context {
	dl := e.taskDeadline(t)
	if dl <= 0 && !final {
		return base // no timeout → unchanged behavior
	}
	return agent.WithTaskClock(base, agent.TaskClock{DeadlineUnix: dl, Final: final})
}

// ---------- Coordinator ----------

func (e *Engine) completeGoalMet(ctx context.Context, t *Task, reason string) {
	e.completeGoalMetWithin(ctx, t, reason, settleDrainGrace)
}

func (e *Engine) completeGoalMetWithin(ctx context.Context, t *Task, reason string, grace time.Duration) {
	drained := true
	owned := e.markGoalSettling(t.ID)
	if owned {
		log.Printf("[planner] task %s goals met; draining active workers for up to %s", t.ID, grace)
		drained = e.waitForInflightAtMost(ctx, t.ID, 1, grace)
	}
	if ctx.Err() != nil || e.IsDeleting(t.ID) {
		if owned {
			e.settling.CompareAndDelete(t.ID, settlementGoalMet)
		}
		return
	}
	if err := e.m.SetTaskStatus(t.ID, "done"); err != nil {
		log.Printf("[planner] task %s failed to persist completion: %v", t.ID, err)
		if owned {
			e.settling.CompareAndDelete(t.ID, settlementGoalMet)
		}
		t.Notify()
		return
	}
	if drained {
		log.Printf("[planner] task %s completion settlement drained all active workers: %s", t.ID, reason)
		return
	}
	log.Printf("[planner] task %s completion drain timed out; cancelling remaining workers", t.ID)
	e.cancelExec(t.ID, agent.AbortGoalMet)
	e.waitForInflightAtMost(context.WithoutCancel(ctx), t.ID, 1, 3*time.Second)
}

// startDeadlineCoordinator launches the per-task deadline timer once (idempotent).
// Called from Run() and from the restart reload path, so non-active timeout tasks
// still get settled after their deadline even without live planner/worker loops.
func (e *Engine) startDeadlineCoordinator(ctx context.Context, t *Task) {
	if t == nil || t.TimeoutSeconds <= 0 {
		return
	}
	e.deleteMu.RLock()
	if e.closed || e.IsDeleting(t.ID) {
		e.deleteMu.RUnlock()
		return
	}
	if _, loaded := e.coordStarted.LoadOrStore(t.ID, true); loaded {
		e.deleteMu.RUnlock()
		return
	}
	rt := e.registerTaskRoutines(ctx, t.ID, 1)
	e.deleteMu.RUnlock()
	runTaskRoutine(rt, func(loopCtx context.Context) { e.deadlineCoordinator(loopCtx, t) })
}

// deadlineCoordinator waits until the task's absolute deadline, then runs the settle
// sequence. Absolute wall-clock: it keeps counting through pauses.
func (e *Engine) deadlineCoordinator(ctx context.Context, t *Task) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if isTerminalStatus(e.m.TaskStatus(t.ID)) {
			return // already finished (goals met / failed) — nothing to time out
		}
		dl := e.taskDeadline(t)
		if dl <= 0 {
			if sleepCtx(ctx, deadlinePollInterval) { // not yet stamped (task hasn't really run)
				return
			}
			continue
		}
		if remaining := time.Until(time.Unix(dl, 0)); remaining > 0 {
			nap := remaining
			if nap > deadlineMaxSleep {
				nap = deadlineMaxSleep
			}
			if sleepCtx(ctx, nap) {
				return
			}
			continue
		}
		if e.settleTask(ctx, t) || e.isTaskTimeoutSettling(t.ID) {
			return
		}
		// Goal completion can still fail to persist; keep its deadline available for recovery.
		if sleepCtx(ctx, deadlinePollInterval) {
			return
		}
	}
}

// settleTask runs the ordered settle sequence once (§4 steps ①–⑥), returning
// whether it acquired timeout settlement.
func (e *Engine) settleTask(ctx context.Context, t *Task) bool {
	if !e.markSettling(t.ID) {
		return false
	}
	log.Printf("[deadline] task %s Reached timeout; beginning settlement", t.ID)

	// Wait for active workers/planner to drain; their bounded MaxDuration enters settlement.
	// After grace expires, hard-cancel task execution; settlement-aware branches classify it correctly.
	hardStop := time.Now().Add(settleDrainGrace)
	for e.inflightCount(t.ID) > 0 {
		if time.Now().After(hardStop) {
			log.Printf("[deadline] task %s Drain timed out (%s); hard-cancelling active runs", t.ID, settleDrainGrace)
			e.cancelExec(t.ID, agent.AbortSettleDrainTimeout)
			_ = sleepCtx(ctx, 3*time.Second) // Give worker branches time to persist and classify results.
			break
		}
		if sleepCtx(ctx, 500*time.Millisecond) {
			return true // Entire engine is shutting down.
		}
	}

	// Final planner round: timeout instructions and last goal judgment, without new intents.
	met := e.runFinalPlannerRound(ctx, t)
	if !e.beginTaskOperation(t.ID) {
		return true
	}
	defer e.decInflight(t.ID)

	// Guard terminal state: met->done (completed), otherwise timeout. If normal execution already set done,
	// SetTaskStatusGuarded refuses to overwrite it, preserving completed semantics.
	status := "timeout"
	if met {
		status = "done"
	}
	won, err := e.m.SetTaskStatusGuarded(t.ID, status)
	switch {
	case err != nil:
		log.Printf("[deadline] task %s Failed to persist terminal state: %v", t.ID, err)
	case won:
		log.Printf("[deadline] task %s Settlement complete; terminal state=%s", t.ID, status)
	default:
		log.Printf("[deadline] task %s Already terminal during settlement; preserving state", t.ID)
	}
	return true
}

// runFinalPlannerRound drives exactly ONE terminal planner round with the
// task-timeout planner words (final goal judgment; no new intents). Waits for the
// LLM to be ready (bounded by ctx) so a completable task isn't mis-judged timeout.
func (e *Engine) runFinalPlannerRound(ctx context.Context, t *Task) (met bool) {
	if e.IsDeleting(t.ID) {
		return false
	}
	planner, _ := e.snapshotFor(t)
	for planner == nil {
		if sleepCtx(ctx, deadlinePollInterval) {
			return false
		}
		if isTerminalStatus(e.m.TaskStatus(t.ID)) {
			return false
		}
		if e.IsDeleting(t.ID) {
			return false
		}
		planner, _ = e.snapshotFor(t)
	}
	// Independent context outside execCancel so pause/hard cancel cannot interrupt the final round; Final injects timeout instructions.
	fctx := e.clockCtx(ctx, t, true)
	if !e.beginTaskOperation(t.ID) {
		return false
	}
	defer e.decInflight(t.ID)
	emit := func(r db.Activity) { e.emitActivity(t, r) }
	e.emitActivity(t, db.Activity{Worker: "planner", Kind: "round",
		Summary: fmt.Sprintf("Task timeout settlement · final judgment (round %d)", e.nextPlannerRound(t.ID))})
	tTaskID, _ := strconv.ParseInt(t.ID, 10, 64)
	e.BeginLLMCall(t.ID)
	met, reason, err := planner.Plan(fctx, tTaskID, e.m.assets, t.Store, t.Goal, t.drainTriggers(), emit)
	e.EndLLMCall(t.ID)
	if err != nil {
		log.Printf("[deadline] task %s Final planning failed: %v", t.ID, err)
	} else if met {
		log.Printf("[deadline] task %s Final judgment found goals met: %s", t.ID, reason)
	}
	return met
}
