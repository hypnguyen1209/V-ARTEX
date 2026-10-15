package agent

import (
	"context"
	"errors"
	"fmt"
)

// AbortCause names why an agent run's context was cancelled. Every cancellation
// site should attach one so the activity trace can report the real initiator.
type AbortCause struct {
	Code  string
	Short string
	Text  string
}

func (c *AbortCause) Error() string { return c.Text }

func cause(code, short, text string) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: text}
}

// Causef builds a cause that includes runtime-specific detail.
func Causef(code, short, format string, args ...any) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: fmt.Sprintf(format, args...)}
}

var (
	// Task-level execution context.
	AbortPausedByUser = cause("paused_by_user", "The user paused the task",
		"The user paused the task through the task-control API (POST /api/tasks/{id}/control, action=pause). This Planner/Worker run was actively canceled; running intents return to frontier(open), then are claimed again and restarted from the beginning when the task resumes.")
	AbortPausedByOrchestrator = cause("paused_by_orchestrator", "The orchestrator paused the task",
		"The orchestrator called pause_task to pause this task. This Planner/Worker run was actively canceled; running intents return to frontier(open) and run again after resuming.")
	AbortTaskDeleted = cause("task_deleted", "The task was deleted",
		"The task is being deleted (DELETE /api/tasks/{id}). The deletion barrier canceled its running Planner, Workers, and main Agent; the results of this run will no longer be used.")
	AbortPausedOnReload = cause("paused_on_reload", "Task pause restored at startup",
		"At startup, the backend restored the task's persisted paused state. This run was canceled; normally no Agent is running during restoration.")
	AbortGoalMet = cause("goal_met", "The planner confirmed the goal was met",
		"The planner determined that the task goal was met, marked the task done, and canceled the remaining Workers. Their intents are marked stopped rather than failed.")
	AbortSettleDrainTimeout = cause("settle_drain_timeout", "Task timeout drain grace expired",
		"After the task timeout, the backend waited for running Workers to settle gracefully, but the 90-second drain grace period expired, requiring hard cancellation. Intents are marked exhausted; facts and assets already written during settlement are preserved.")

	// Per-work context.
	AbortKilledByPlanner = cause("killed_by_planner", "The planner terminated this intent",
		"The planner called kill_work to terminate this intent, usually because it had drifted off course or was no longer worth pursuing. The intent is marked stopped and will not be automatically claimed again.")
	AbortWorkPausedByUser = cause("work_paused_by_user", "The user paused this Worker intent",
		"The user paused the running Worker. This call was canceled and the intent becomes paused. All recorded intents, facts, findings, and activities are preserved; resuming restarts execution from the beginning.")
	AbortWorkCancelledByUser = cause("work_cancelled_by_user", "The user deleted this Worker intent",
		"The user deleted the running Worker. This call was canceled. After the Worker exits its write section, the server applies the user's deletion mode: soft deletion marks the intent deleted and retains all outputs; hard deletion cascades to the intent and downstream nodes supported only by it.")
	AbortWorkFinished = cause("work_finished", "Worker finished; context released",
		"The Worker finished normally and the engine released its context resources in detachWork. This is not an interruption; if shown in an interruption message, cancellation raced with completion.")
	AbortPausedRaceGuard = cause("paused_race_guard", "New run rejected while task paused",
		"While the task is paused, the engine refuses to issue a new execution context to prevent a claim/pause race from starting another Worker. A claimed intent returns to frontier.")

	// Main Agent and standalone conversation contexts.
	AbortChatStoppedByUser = cause("chat_stopped_by_user", "The user stopped this chat turn",
		"The user clicked Stop to cancel this main Agent or session Agent turn. Existing activities are preserved, and another message can be sent.")
	AbortChatPausedWithTask = cause("chat_paused_with_task", "Task pause canceled the main chat",
		"Pausing the task also canceled the running main Agent conversation. Existing activities are preserved; this message will not replay automatically when the task resumes.")
	AbortChatTurnFinished = cause("chat_turn_finished", "Chat turn finished; context released",
		"This chat turn finished normally and the server is releasing its context resources. This is not an interruption; if shown in an interruption message, cancellation raced with completion.")

	// Process-level and per-run hard backstop.
	AbortShutdown = cause("shutdown", "The backend is shutting down",
		"The backend received SIGINT or SIGTERM and is restarting, updating, or shutting down. All running Agents are canceled; after restart, remaining running intents are reset to open and run again.")
	AbortRunHardTimeout = cause("run_hard_timeout", "Run hard timeout triggered",
		"This run exceeded its soft wall-clock budget and additional grace period. A model request or tool failed to return for so long that normal turn-boundary settlement could not run. Check the last tool call that had not returned before the interruption.")
)

// AbortReason resolves the named cause attached to a cancelled run context.
func AbortReason(ctx context.Context) (code, short, text string, ok bool) {
	c := context.Cause(ctx)
	if c == nil {
		return "", "", "", false
	}
	var ac *AbortCause
	if errors.As(c, &ac) {
		return ac.Code, ac.Short, ac.Text, true
	}
	switch {
	case errors.Is(c, context.DeadlineExceeded):
		return "deadline_exceeded", "Upstream context deadline exceeded",
			"The upstream context reached its deadline, but its creator did not attach a named cause with WithTimeoutCause: " + c.Error(), true
	case errors.Is(c, context.Canceled):
		return "canceled_no_cause", "Cancellation has no named cause",
			"The upstream context was canceled without a named cause attached through context.WithCancelCause. Register the cause in agent/cancelcause.go and wire it into this cancellation point.", true
	default:
		return "other", firstLine(c.Error(), 80), c.Error(), true
	}
}
