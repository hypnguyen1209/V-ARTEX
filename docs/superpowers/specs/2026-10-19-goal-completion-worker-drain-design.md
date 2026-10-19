# Goal Completion Worker Drain Design

## Problem

When the planner proves the final goal, the engine currently persists the task as `done` and immediately cancels every running worker. Tool calls that already returned remain durable, but findings, facts, assets, and the worker summary that the model was about to write are lost. The UI then shows a red `aborted_streaming` result even though task completion caused the cancellation.

The reproduced task proved this sequence: `prove_goal` linked finding 8 to goal 2, `insert_assets` returned successfully for another worker, and that worker was canceled before its planned `report_finding` and `record_fact` calls.

## Selected Approach

Use a bounded graceful drain before terminal cancellation. This reuses the engine's existing settlement gate and worker hooks instead of introducing another execution system.

Alternatives rejected:

- Waiting for workers indefinitely can hang completion and spend unbounded model tokens.
- Sleeping for a fixed delay without steering workers does not tell the model to persist unsaved results and remains timing-dependent.

## Runtime Semantics

1. When a normal planner round reports that all goals are met, atomically mark the task as a goal-completion settlement before changing the persisted task status.
2. The settlement gate immediately prevents new planner rounds and new worker claims.
3. Running workers observe the goal-completion settlement through their existing hook runner:
   - existing `insert_assets`, `record_fact`, and `report_finding` calls remain allowed;
   - new probes and command/tool execution are blocked;
   - the model receives one finalization instruction to persist every identified result and emit its summary.
4. The planner waits up to the existing 90-second drain grace for admitted worker operations to finish and persist their terminal intent states.
5. After all workers drain, persist the task as `done` without canceling completed worker streams.
6. If the grace expires, persist `done`, cancel only the remaining execution with the named `goal_met` cause, and classify those unfinished intents as `stopped`, not timeout-exhausted.

Task timeout settlement remains distinct: it keeps its existing timeout prompt, final planner judgment, and `exhausted` classification after a hard drain timeout.

## State Representation

Keep one per-task settlement map, but store a settlement kind rather than an untyped boolean. Presence continues to mean “do not dispatch or claim”; the value distinguishes `task_timeout` from `goal_met`. Existing cleanup paths delete the same map entry, so no database migration or persistent state is required.

If task-timeout settlement wins the race first, it remains the owner. If goal completion wins first, the deadline coordinator sees the task is already settling and does not start a second drain.

## Failure and Restart Behavior

- Engine shutdown or task deletion aborts the drain through the task runtime context and retains the existing cleanup behavior.
- A provider/tool that does not return within 90 seconds is hard-canceled after durable task completion.
- If the process restarts during the in-memory drain, the task is still persisted as running. On reload, existing graph state and intent states drive the normal planner/frontier recovery instead of leaving a falsely completed task.
- No new configuration is added; the existing 90-second grace is the bounded policy.

## Tests

Regression tests must prove:

- goal-completion settlement blocks probe tools but permits all three write-back tools;
- a worker attempting to finish without seeing settlement receives exactly one finalization continuation;
- ordinary steering and empty-turn handling are unchanged outside goal settlement;
- goal completion waits for an active worker to drain before cancellation/status finalization;
- grace expiry retains the `goal_met` cause and does not use timeout-exhausted classification;
- existing timeout-settlement and cancellation-cause tests remain green.

## Non-Goals

- Re-running or reconstructing findings lost by already completed historical tasks.
- Changing goal semantics or requiring exhaustive testing after a goal is proven.
- Adding UI configuration for drain duration.
- Changing task timeout behavior beyond keeping its classification separate.
