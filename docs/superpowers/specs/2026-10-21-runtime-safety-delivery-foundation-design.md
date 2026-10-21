# V-ARTEX Runtime Safety and Delivery Foundation Design

**Date:** 2026-10-21

**Status:** Approved for implementation planning

## Context

V-ARTEX already has per-task worker counts and an optional cap on simultaneously running tasks, but neither control limits the total number of planner and worker model runs across all admitted tasks. The planner prompt asks for at most four new intents per round, yet the `add_intent` tool does not enforce that limit and does not reject priorities outside the documented range. Operational diagnosis currently requires starting the full server and interpreting logs. The repository also lacks a mandatory CI path that exercises the orchestration lifecycle, the race detector, and the browser.

This project creates a bounded safety and delivery foundation inspired by Cairn while preserving V-ARTEX's existing engine, PostgreSQL state model, frontend, and upgrade behavior. The changes are additive and independently testable.

## Goals

- Enforce the planner's four-intent-per-round contract at runtime.
- Validate every new intent priority before persistence.
- Bound total planner and worker model runs across all tasks with fair scheduling.
- Keep capacity available for planning and completion control work.
- Add a deterministic, read-only-by-default `artex doctor` command.
- Add orchestration, race, frontend, and browser gates to pull requests and releases.
- Preserve existing deployments unless an operator explicitly enables the new global run cap.

## Non-goals

- Containerizing individual tools or adding a sandbox policy engine.
- Implementing graph replay, time travel, or a new event store.
- Replacing PostgreSQL, the task admission queue, worker hooks, or provider abstractions.
- Limiting intents created directly by a human or by non-planner administrative APIs.
- Changing the meaning of task concurrency or per-task worker count.
- Redesigning the settings pages beyond the controls and status needed for this feature.

## Selected Architecture

The work is divided into four slices that share no new persistence schema:

1. A planner-local intent budget attached to the planner's tool set.
2. A manager-owned global run gate around complete planner and worker executions.
3. A standalone diagnostic command with injectable checks and stable output.
4. Deterministic backend and browser tests wired into a reusable CI workflow.

The slices should land in that order so each safety boundary exists before the broader concurrency and release gates depend on it.

## Planner Intent Admission

Each invocation of `Planner.Plan` receives a fresh, concurrency-safe budget of four successful intent creations. All `add_intent` calls made during that invocation share the same budget. Provider retries inside the same invocation retain the budget; the next genuine planner round receives a new budget.

The tool enforces the limit in addition to describing it in the prompt and JSON schema:

- The array schema declares `maxItems: 4`.
- A call whose batch is larger than the remaining budget is rejected before any database write.
- Multiple calls cannot cumulatively create more than four intents.
- Capacity is reserved atomically before writes and released for entries that fail to persist. Only successfully created intents consume the round budget.
- Storage failures may still produce the existing per-item partial result, but quota rejection itself is all-or-nothing and never creates a partial batch.
- Malformed JSON is returned as an explicit tool error rather than being treated as an empty request.

Priority accepts integers from 0 through 10. Zero retains the existing compatibility meaning of "unspecified" and is normalized to priority 5 before persistence. Negative values and values greater than 10 are rejected before persistence. The same validation applies to single-item and batch requests.

Only planner tool sets receive this budget. Human-created intents and trusted non-planner tool sets remain uncapped so incident response and manual graph repair cannot be blocked by a planner policy.

## Global Agent Run Budget

### Configuration

Add two settings independent of the existing task concurrency and worker settings:

- `agent_concurrency_enabled`, disabled when absent.
- `agent_concurrency_limit`, with a displayed default of 8 and a minimum accepted value of 1.

The settings API and frontend expose both fields. The API rejects invalid limits with HTTP 400. The settings response also exposes the read-only aggregate counters `agent_runs_active` and `agent_runs_waiting` so an operator can distinguish saturation from a stalled task.

The feature is disabled by default for upgrade compatibility. Enabling it affects new acquisitions immediately. Reducing the limit never cancels active model runs; new acquisitions wait until active usage falls below the configured limit. Disabling it wakes all queued acquisitions while the existing per-task controls continue to apply.

### Admission Unit

A permit covers one complete `Planner.Plan` or `Worker.Execute` invocation, not the lifetime of its goroutine and not each individual provider request. This bounds the expensive agent work while allowing idle loops and database coordination to remain cheap.

An admitted worker keeps its permit through goal-completion or timeout wrap-up. Settlement must not reacquire a permit, which avoids deadlock when the system is saturated. A separate final planner judgment is classified as control work and acquires a control permit normally.

Every acquire is context-aware. Cancellation or shutdown removes the waiter and returns without leaking a permit. Every successful acquisition has exactly one deferred release, including panic and error paths.

### Fairness and Reserved Capacity

The gate has control waiters and worker waiters:

- Planner and final-judgment runs are control work and are served FIFO.
- Worker waiters are FIFO within a task and round-robin across task IDs, preventing one high-worker task from monopolizing the global budget.
- When the configured limit is greater than one, ordinary workers may occupy at most `limit - 1` permits. The remaining permit is reserved for control work even when no planner is currently waiting.
- With a limit of one, planner and worker work share the sole permit; control waiters are selected before the next worker when it becomes free.

The reserved slot deliberately trades at most one unit of worker throughput for guaranteed planner progress. Existing task admission remains FIFO and continues deciding which tasks may enter the engine; the new gate only schedules agent runs within admitted tasks.

The gate is manager-owned so every engine in the process shares one budget. Settings updates reconfigure the same live gate and wake its scheduler. No database migration is required because the existing settings store holds the two new keys.

## `artex doctor`

### Command Contract

The binary recognizes a dedicated command before starting the server:

```text
artex doctor [server path/address flags] [--json] [--live-llm]
```

Relevant data, address, and proxy flag defaults match normal server startup. The command never starts the HTTP server, launches engines, runs database migrations, creates a JWT key, rewrites configuration, or persists settings.

Without `--live-llm`, it makes no model-provider request and therefore cannot create billable LLM usage. `--live-llm` is an explicit opt-in that performs the smallest supported request against each configured provider profile with a stored API key and labels those checks as networked and potentially billable.

### Checks

The default command performs bounded checks for:

- configuration path resolution and parseability;
- PostgreSQL DSN presence, sanitized parseability, connectivity, and `SELECT 1`;
- presence and readable version of the expected schema without running migrations;
- data, workspace, skills, and JWT-key paths, including readable ownership/mode information without creating probe files;
- availability of the selected HTTP and proxy addresses by transiently binding and closing them; an address already in use is reported as `warn`, because it may belong to the running V-ARTEX instance, and includes remediation for identifying its owner;
- Python interpreter resolution and version;
- Node, npm/npx, and locally installed Playwright availability used by browser skills, without downloading packages;
- stored LLM profile shape and required secret presence without printing secret values; and
- the existence and readability of configured skill directories.

Checks that depend on an earlier failed check become `warn` with a dependency explanation rather than emitting misleading secondary failures. Each external check has a short context deadline.

### Output and Exit Codes

Every result contains:

```json
{
  "name": "postgres.connectivity",
  "status": "ok",
  "detail": "PostgreSQL accepted a read-only probe",
  "remediation": "",
  "duration_ms": 12
}
```

Status is one of `ok`, `warn`, or `fail`. Human-readable aligned output is the default. `--json` emits one stable object containing the V-ARTEX version, overall status, whether live checks were enabled, and the ordered result list; it contains no ANSI escape sequences.

Exit codes are:

- `0`: no failed checks;
- `1`: one or more checks failed;
- `2`: invalid command-line usage. Configuration parse failures are normal failed checks and return `1`.

DSN passwords, API keys, setup tokens, JWT material, authorization headers, and full provider responses are always redacted from human and JSON output.

## Deterministic Orchestration E2E

A backend end-to-end test uses the real PostgreSQL test fixture and a scripted provider, not a live model. It drives one complete lifecycle:

1. A planner response calls `add_intent`.
2. The intent is persisted and claimed by a worker.
3. The worker records a fact and finding and reaches its terminal intent state.
4. The persisted change wakes the planner.
5. The next scripted planner response proves the goal.
6. Goal-completion settlement drains and the task becomes `done`.

The test asserts database state and structured tool events rather than matching prompt prose. Synchronization uses explicit channels and bounded eventual assertions; fixed sleeps are prohibited. The provider fails the test on any unplanned call, making extra rounds and scheduling regressions visible.

Focused tests additionally cover planner quota races, priority boundaries, fair run-gate ordering, control-slot reservation, live reconfiguration, cancellation cleanup, and permit release on every error path.

## Browser E2E

The frontend adds a minimal Playwright test dependency, configuration, and smoke suite. CI starts the application in its deterministic mock mode on loopback and installs only Chromium. The smoke suite verifies that:

- the application loads without uncaught browser errors;
- task navigation and the primary task view render;
- settings expose the global agent-concurrency switch and limit;
- changing the controls sends the expected settings payload in mock mode; and
- the layout remains usable at one desktop and one mobile viewport.

The suite never connects to a production backend and does not depend on network services beyond the local test server.

## CI and Release Gates

Add a reusable `.github/workflows/ci.yml` triggered by pull requests, pushes to the primary branch, and `workflow_call`. It contains independent jobs so failures are attributable:

- Backend: run `go test ./...` against a PostgreSQL service.
- Race: run `go test -race ./...` against a PostgreSQL service.
- Frontend: run clean dependency installation, static checks, existing frontend tests, and the production/static build.
- Browser: install Chromium and run the Playwright smoke suite in mock mode.

All jobs use lockfile-pinned dependencies and explicit timeouts. They use only ephemeral test credentials and no repository or production secrets.

The release workflow calls the reusable CI workflow and makes artifact publication depend on its success. A tag can therefore build artifacts only after backend, race, frontend, and browser gates pass for that exact revision.

## Failure and Recovery Behavior

- Intent quota rejection leaves the graph unchanged and gives the planner a concrete remaining-capacity error it can recover from in the same round.
- A canceled run-gate waiter never consumes capacity. A canceled admitted run releases its permit through the normal deferred path.
- A process restart reconstructs the run gate from stored settings; queued waiters are in-memory scheduling state and are safely rebuilt by normal engine recovery.
- Doctor failures never modify the condition they diagnose. A missing or old schema includes remediation to start or upgrade V-ARTEX normally.
- CI test providers, mock browser APIs, and PostgreSQL fixtures are deterministic and cannot call billable model endpoints.

## Compatibility and Rollout

No schema migration is introduced. Existing installations read a missing `agent_concurrency_enabled` setting as false and retain their current concurrency. Existing task concurrency and worker values keep their meanings.

The new settings fields are additive, so older frontends ignore them and newer frontends display the default without silently enabling it. Operators can first observe normal peak activity through the read-only counters, select a limit, and then enable enforcement.

CLI startup without the `doctor` subcommand retains its current flag behavior. Automation can adopt `doctor --json` without scraping logs.

## Acceptance Criteria

- A planner round can never persist more than four intents, including through repeated or concurrent tool calls.
- Priorities below 0 or above 10 create no intent; priority 0 persists as the existing default priority 5.
- With the global cap enabled, active planner plus worker executions never exceed the configured limit.
- Worker admission is round-robin across tasks, and planner progress remains possible under worker saturation.
- Changing or disabling the limit takes effect without restarting and without canceling active work.
- `artex doctor --json` is deterministic, secret-safe, read-only by default, and makes no LLM call without `--live-llm`.
- The scripted PostgreSQL orchestration lifecycle passes without a real provider or timing sleeps.
- Go tests, the full race suite, frontend checks/build, and Playwright smoke tests are required before release artifact publication.

## Follow-up Projects

Two Cairn-inspired improvements remain intentionally separate:

1. A container-backed tool runner with explicit filesystem, network, resource, and credential policy.
2. Durable orchestration event replay and graph time-travel diagnostics.

Both require independent threat models, data-retention decisions, and migration designs. They should build on the diagnostic and deterministic-test foundation defined here rather than expanding this project's risk surface.
