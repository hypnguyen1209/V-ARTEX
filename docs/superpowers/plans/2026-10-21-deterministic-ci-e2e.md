# Deterministic CI and E2E Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove the real task lifecycle without networked LLMs, cover the two critical browser surfaces at desktop and mobile widths, and make the same checks block pull requests and releases.

**Architecture:** A PostgreSQL-backed engine test drives planner and worker agents with scripted, non-streaming providers and waits on persisted terminal state. Playwright runs the existing Next.js mock mode in Chromium at two viewport sizes. A reusable GitHub Actions workflow owns backend, race, frontend, and browser gates; tag releases call it before producing artifacts.

**Tech Stack:** Go, PostgreSQL, Norma LLM test doubles, Next.js 16, TypeScript, Playwright, GitHub Actions.

**Design:** `docs/superpowers/specs/2026-10-21-runtime-safety-delivery-foundation-design.md`

**Sequence:** Implement after the planner admission, global run budget, and doctor plans so this suite can exercise all three changes.

---

## File Map

- Create `server/engine_e2e_test.go`: real PostgreSQL lifecycle test using deterministic model responses.
- Modify `web/package.json` and `web/package-lock.json`: direct Playwright dependency and stable test scripts.
- Create `web/playwright.config.ts`: mock-mode server and desktop/mobile Chromium projects.
- Create `web/e2e/smoke.spec.ts`: tasks and system-settings smoke coverage with console/page error checks.
- Modify `.gitignore`: Playwright output directories.
- Create `.github/workflows/ci.yml`: reusable verification workflow for pushes and pull requests.
- Modify `.github/workflows/release.yml`: make tag releases depend on the reusable verification workflow.

### Task 1: Add a deterministic PostgreSQL-backed orchestration test

**Files:**
- Create: `server/engine_e2e_test.go`

- [ ] **Step 1: Write the lifecycle test helpers and failing test**

Create `server/engine_e2e_test.go`. Reuse the package-local `retestProvider` so any accidental streaming call fails. Keep model steps serialized with atomics and derive runtime node IDs from the store rather than hard-coding database IDs:

```go
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hypnguyen1209/V-ARTEX/agent"
	"github.com/hypnguyen1209/V-ARTEX/db"
	"github.com/Autumn-27/norma/llm"
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
	if err != nil || len(nodes) != 1 {
		t.Fatalf("kind %s nodes=%d err=%v", kind, len(nodes), err)
	}
	return nodes[0].ID
}
```

Append `TestEngineLifecycleWithScriptedProviders`. Its setup and cleanup are exact:

1. Call `NewManager(t.TempDir(), "")`; the focused command below supplies `ARTEX_PG_DSN`, and CI supplies the same variable through its PostgreSQL service.
2. Call `manager.agentRuns.configure(false, defaultAgentConcurrencyLimit)` so a developer's persisted local cap cannot change fixture scheduling; do not mutate the persisted worker or gate settings.
3. Create one task and add one open `db.KindGoal` before starting the engine.
4. Build one planner provider and one worker provider as below, then enable non-streaming mode on both agents.
5. Install them with `engine.SetAuthoritativeAgentResolver`, set `engine.debounce = 0`, run the engine with a cancelable context, and wait for status `done`.
6. Assert the intent is `done`, exactly one fact and one finding exist, planner call count is five, worker call count is three, and persisted activity includes successful `add_intent`, `record_fact`, `report_finding`, and `prove_goal` tool results.

Use these scripted responses:

```go
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
```

When checking activity, call `task.Store.ActivityList(nil, 0, 1000)` and build a set from entries whose `Kind == "tool_result" && !entry.IsError`. Fail with the serialized activity list when any required tool is missing. This verifies actual agent-tool-engine wiring instead of only checking final rows.

- [ ] **Step 2: Run the focused test and verify the lifecycle contract**

```bash
ARTEX_PG_DSN='postgres://postgres:postgres@127.0.0.1:5432/artex_test?sslmode=disable' go test ./server -run TestEngineLifecycleWithScriptedProviders -count=1
```

Expected after the preceding plans land: PASS. If an assertion exposes an integration defect, keep the assertion and fix the owning runtime path before proceeding.

- [ ] **Step 3: Make the fixture lifecycle deterministic**

If the test initially races planner wake-ups, keep the scripted call counts unchanged and fix only the fixture wiring:

- Add the open goal before `engine.Run` so the planner branch is active.
- Set `engine.debounce = 0` before `Run`.
- Set both `planner.SetNonStreaming(func() bool { return true })` and `worker.SetNonStreaming(func() bool { return true })`.
- In one `t.Cleanup`, cancel the engine context, call `engine.Close()`, delete the task, and close the manager, in that order.
- Use the polling helper above; do not add fixed sleeps.

Run the test ten times to prove repeatability:

```bash
ARTEX_PG_DSN='postgres://postgres:postgres@127.0.0.1:5432/artex_test?sslmode=disable' go test ./server -run TestEngineLifecycleWithScriptedProviders -count=10
```

Expected: PASS ten times with no external HTTP or LLM request.

- [ ] **Step 4: Commit the backend E2E test**

```bash
git add server/engine_e2e_test.go
GIT_AUTHOR_NAME='hypnguyen1209' GIT_AUTHOR_EMAIL='hypnguyen1209@gmail.com' GIT_COMMITTER_NAME='hypnguyen1209' GIT_COMMITTER_EMAIL='hypnguyen1209@gmail.com' git commit -m 'test(server): cover deterministic task lifecycle'
```

### Task 2: Add desktop and mobile browser smoke tests

**Files:**
- Modify: `web/package.json`
- Modify: `web/package-lock.json`
- Create: `web/playwright.config.ts`
- Create: `web/e2e/smoke.spec.ts`
- Modify: `.gitignore`

- [ ] **Step 1: Install Playwright and add explicit test scripts**

```bash
cd web
npm install --save-dev @playwright/test
npm pkg set scripts.test='node --test'
npm pkg set scripts.test:e2e='playwright test'
cd ..
```

Expected: `package.json` has direct `@playwright/test`, `test`, and `test:e2e` entries; `package-lock.json` records exact resolved versions.

- [ ] **Step 2: Configure a mock-mode web server and two Chromium projects**

Create `web/playwright.config.ts`:

```ts
import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: process.env.CI ? [["github"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL: "http://127.0.0.1:4173",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    { name: "desktop-chromium", use: { ...devices["Desktop Chrome"] } },
    { name: "mobile-chromium", use: { ...devices["Pixel 7"] } },
  ],
  webServer: {
    command: "npm run dev -- --hostname 127.0.0.1 --port 4173",
    env: { ...process.env, NEXT_PUBLIC_MOCK: "1" },
    url: "http://127.0.0.1:4173/function/tasks",
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
});
```

- [ ] **Step 3: Write the browser smoke test**

Create `web/e2e/smoke.spec.ts`:

```ts
import { expect, test, type Page } from "@playwright/test";

const browserErrors = new WeakMap<Page, string[]>();

test.beforeEach(async ({ page }) => {
  const failures: string[] = [];
  page.on("pageerror", (error) => failures.push(error.message));
  page.on("console", (message) => {
    if (message.type() === "error") failures.push(message.text());
  });
  browserErrors.set(page, failures);
});

test.afterEach(async ({ page }) => {
  const failures = browserErrors.get(page) ?? [];
  expect(failures, "browser errors").toEqual([]);
});

async function expectNoHorizontalOverflow(page: Page) {
  const fits = await page.evaluate(
    () => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1,
  );
  expect(fits).toBe(true);
}

test("tasks and global run-budget settings work in mock mode", async ({ page }) => {
  await page.goto("/function/tasks");
  await expect(page.getByRole("tab", { name: "Current Tasks" })).toBeVisible();
  await expectNoHorizontalOverflow(page);

  await page.goto("/system/settings");
  await expect(page.getByRole("heading", { name: "System Settings" })).toBeVisible();
  const card = page.locator('[aria-labelledby="agent-concurrency-heading"]');
  await expect(card).toBeVisible();
  await card.getByRole("switch").click();
  await card.getByRole("spinbutton").fill("3");
  await card.getByRole("button", { name: "Save" }).click();
  await expect(page.getByText("Global agent concurrency limited to 3")).toBeVisible();
  await expectNoHorizontalOverflow(page);
});
```

The locator relies on the `aria-labelledby="agent-concurrency-heading"` contract in the run-budget plan. Keep that accessible relationship instead of adding a test-only ID.

- [ ] **Step 4: Ignore only generated browser artifacts**

Append to `.gitignore`:

```gitignore
# Playwright artifacts
/web/playwright-report/
/web/test-results/
```

- [ ] **Step 5: Run browser and frontend checks**

```bash
cd web
npx playwright install chromium
npm test
npm run check
npm run build:static
npm run test:e2e
cd ..
```

Expected: Node tests, Biome, static build, and both Playwright projects PASS. The browser run reports two passing tests because the same smoke case runs once per project.

- [ ] **Step 6: Commit browser coverage**

```bash
git add .gitignore web/package.json web/package-lock.json web/playwright.config.ts web/e2e/smoke.spec.ts
GIT_AUTHOR_NAME='hypnguyen1209' GIT_AUTHOR_EMAIL='hypnguyen1209@gmail.com' GIT_COMMITTER_NAME='hypnguyen1209' GIT_COMMITTER_EMAIL='hypnguyen1209@gmail.com' git commit -m 'test(web): add desktop and mobile smoke coverage'
```

### Task 3: Add a reusable CI verification workflow

**Files:**
- Create: `.github/workflows/ci.yml`

- [ ] **Step 1: Create the workflow with independent evidence-producing jobs**

Create `.github/workflows/ci.yml`:

```yaml
name: ci

on:
  workflow_call:
  pull_request:
  push:
    branches: [main]

concurrency:
  group: ci-${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true

permissions:
  contents: read

jobs:
  backend:
    runs-on: ubuntu-latest
    timeout-minutes: 20
    services:
      postgres:
        image: postgres:17
        env:
          POSTGRES_PASSWORD: postgres
          POSTGRES_DB: artex_test
        ports: [5432:5432]
        options: >-
          --health-cmd "pg_isready -U postgres -d artex_test"
          --health-interval 5s
          --health-timeout 5s
          --health-retries 20
    env:
      ARTEX_PG_DSN: postgres://postgres:postgres@127.0.0.1:5432/artex_test?sslmode=disable
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true
      - run: go test ./...

  race:
    runs-on: ubuntu-latest
    timeout-minutes: 30
    services:
      postgres:
        image: postgres:17
        env:
          POSTGRES_PASSWORD: postgres
          POSTGRES_DB: artex_test
        ports: [5432:5432]
        options: >-
          --health-cmd "pg_isready -U postgres -d artex_test"
          --health-interval 5s
          --health-timeout 5s
          --health-retries 20
    env:
      ARTEX_PG_DSN: postgres://postgres:postgres@127.0.0.1:5432/artex_test?sslmode=disable
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true
      - run: go test -race ./...

  frontend:
    runs-on: ubuntu-latest
    timeout-minutes: 20
    defaults:
      run:
        working-directory: web
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: "22"
          cache: npm
          cache-dependency-path: web/package-lock.json
      - run: npm ci
      - run: npm test
      - run: npm run check
      - run: npm run build:static
        env:
          NEXT_PUBLIC_MOCK: "1"

  browser:
    runs-on: ubuntu-latest
    timeout-minutes: 20
    defaults:
      run:
        working-directory: web
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: "22"
          cache: npm
          cache-dependency-path: web/package-lock.json
      - run: npm ci
      - run: npx playwright install --with-deps chromium
      - run: npm run test:e2e
      - if: failure()
        uses: actions/upload-artifact@v4
        with:
          name: playwright-report
          path: web/playwright-report
          retention-days: 7
```

- [ ] **Step 2: Validate workflow syntax and commands locally**

```bash
git diff --check -- .github/workflows/ci.yml
go test ./...
go test -race ./...
cd web
npm ci
npm test
npm run check
NEXT_PUBLIC_MOCK=1 npm run build:static
npm run test:e2e
cd ..
```

Expected: no whitespace errors and every command used by CI passes locally against the configured PostgreSQL instance.

- [ ] **Step 3: Commit CI**

```bash
git add .github/workflows/ci.yml
GIT_AUTHOR_NAME='hypnguyen1209' GIT_AUTHOR_EMAIL='hypnguyen1209@gmail.com' GIT_COMMITTER_NAME='hypnguyen1209' GIT_COMMITTER_EMAIL='hypnguyen1209@gmail.com' git commit -m 'ci: add deterministic verification gates'
```

### Task 4: Make releases depend on the same verification contract

**Files:**
- Modify: `.github/workflows/release.yml`

- [ ] **Step 1: Add the reusable verification job**

Add this job before `frontend`:

```yaml
  verify:
    uses: ./.github/workflows/ci.yml
```

Add the dependency to the existing frontend job:

```yaml
  frontend:
    needs: verify
    runs-on: ubuntu-latest
```

The existing `binaries -> frontend` and `release/docker -> binaries` dependency chain then prevents all tag artifacts and images from publishing after any verification failure.

- [ ] **Step 2: Add doctor smoke coverage to release binaries**

Extend the existing Linux amd64 smoke step:

```yaml
      - name: Smoke test Linux amd64 binary
        if: matrix.goos == 'linux' && matrix.goarch == 'amd64'
        run: |
          dist/artex-linux-amd64/artex -h
          doctor_code=0
          dist/artex-linux-amd64/artex doctor --json --addr 127.0.0.1:0 --proxy '' > doctor.json || doctor_code=$?
          test "$doctor_code" -eq 0 || test "$doctor_code" -eq 1
          python3 -m json.tool doctor.json >/dev/null
```

Exit `1` is accepted because a release runner intentionally has no configured production database; JSON parseability and absence of a crash are the binary smoke contract. Exit `2` remains a failure because it indicates invalid command usage.

- [ ] **Step 3: Review the complete dependency graph**

```text
verify
  -> frontend
      -> binaries
          -> release
          -> docker
```

Confirm no publishing job can run with `verify` skipped or failed.

- [ ] **Step 4: Commit the release gate**

```bash
git add .github/workflows/release.yml
GIT_AUTHOR_NAME='hypnguyen1209' GIT_AUTHOR_EMAIL='hypnguyen1209@gmail.com' GIT_COMMITTER_NAME='hypnguyen1209' GIT_COMMITTER_EMAIL='hypnguyen1209@gmail.com' git commit -m 'ci(release): require verification before publishing'
```

### Task 5: Run the full acceptance matrix

**Files:**
- Verify only; modify owning files if a check exposes a defect.

- [ ] **Step 1: Backend and race evidence**

```bash
go test ./...
go test -race ./...
```

Expected: both commands PASS, including the deterministic lifecycle test and run-gate race tests.

- [ ] **Step 2: CLI evidence**

```bash
go run ./cmd/artex doctor --json --addr 127.0.0.1:0 --proxy '' > /tmp/v-artex-doctor.json || test $? -eq 1
python3 -m json.tool /tmp/v-artex-doctor.json >/dev/null
```

Expected: the command emits valid JSON; with a configured test DSN its database checks are `ok`, otherwise it exits `1` with dependent database checks marked `skipped`/`fail` according to the doctor contract.

- [ ] **Step 3: Frontend and browser evidence**

```bash
cd web
npm ci
npm test
npm run check
NEXT_PUBLIC_MOCK=1 npm run build:static
npm run test:e2e
cd ..
```

Expected: all commands PASS and Playwright reports desktop and mobile Chromium coverage.

- [ ] **Step 4: Final repository checks**

```bash
git diff --check
git status --short
git log -5 --format='%h %an <%ae> | %cn <%ce> | %s'
```

Expected: no whitespace errors, only intentional changes remain, and every implementation commit shows `hypnguyen1209 <hypnguyen1209@gmail.com>` for both author and committer.
