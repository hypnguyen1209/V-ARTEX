# Targeted artex-global Improvements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Port six proven reliability fixes into V-ARTEX while keeping the product English-only and dependency-neutral.

**Architecture:** Each fix stays at its existing boundary: distribution configuration, skill-path normalization, agent tool assembly, task-session transport, report UI state, and responsive task layout. Every behavior receives a focused regression test before the minimum production change, followed by the repository's complete Go, frontend, race, and browser gates.

**Tech Stack:** Go 1.26, GitHub Actions, Docker Compose/GHCR, React 19, Next.js 16, TypeScript, Node test runner, Playwright, Tailwind CSS 4.

---

## File map

- `.github/workflows/release.yml`: publish the existing multi-architecture image to V-ARTEX's GHCR namespace.
- `.github/workflows/ci.yml`: run portable skill-path tests on native Windows and macOS runners.
- `docker-compose.yml`, `README.md`: consume and document the owned image.
- `cmd/artex/distribution_test.go`: prevent distribution files from returning to an upstream image namespace.
- `server/server_mgmt.go`, `server/skill_upload_test.go`: normalize API paths independently of the host OS.
- `agent/toolcatalog.go`, `agent/toolalias_test.go`, `agent/assembly.go`: register hidden, non-conflicting aliases for CamelCase tools.
- `web/src/components/task-resilience.test.mjs`: source-contract regressions for transcript fallback and report states.
- `web/src/app/(main)/function/tasks/detail/_tabs/sessions-tab.tsx`: poll the active transcript only while SSE is down.
- `web/src/app/(main)/function/tasks/detail/_tabs/report-tab.tsx`: model loading/error/ready states and retry.
- `web/src/hooks/use-mobile.ts`, `web/src/components/ui/sidebar.tsx`: detect tablet widths and collapse the sidebar at that breakpoint.
- `web/src/app/(main)/function/tasks/page.tsx`: use container-width column priorities.
- `web/playwright.config.ts`, `web/e2e/smoke.spec.ts`: add and assert the 1024×768 viewport.

### Task 1: Own the container distribution path

**Files:**
- Create: `cmd/artex/distribution_test.go`
- Modify: `.github/workflows/release.yml`
- Modify: `docker-compose.yml`
- Modify: `README.md`

- [ ] **Step 1: Write the failing distribution regression test**

```go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDistributionUsesOwnedContainerImage(t *testing.T) {
	root := filepath.Join("..", "..")
	want := "ghcr.io/hypnguyen1209/v-artex"
	for _, name := range []string{"docker-compose.yml", ".github/workflows/release.yml", "README.md"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if strings.Contains(text, "autumn27/artex") {
			t.Errorf("%s still references the upstream image", name)
		}
		if !strings.Contains(text, want) {
			t.Errorf("%s does not reference %s", name, want)
		}
	}

	release, err := os.ReadFile(filepath.Join(root, ".github/workflows/release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(release)
	for _, required := range []string{"packages: write", "registry: ghcr.io", "password: ${{ secrets.GITHUB_TOKEN }}"} {
		if !strings.Contains(text, required) {
			t.Errorf("release workflow is missing %q", required)
		}
	}
	if strings.Contains(text, "DOCKERHUB_") {
		t.Error("release workflow still requires Docker Hub secrets")
	}
}
```

- [ ] **Step 2: Run the test and verify RED**

Run: `go test ./cmd/artex -run TestDistributionUsesOwnedContainerImage -count=1`

Expected: FAIL because Compose, the release workflow, and README still reference `autumn27/artex` and Docker Hub credentials.

- [ ] **Step 3: Make the minimum distribution changes**

In `docker-compose.yml` use:

```yaml
image: ghcr.io/hypnguyen1209/v-artex:${ARTEX_TAG:-latest}
```

In the `docker` job in `.github/workflows/release.yml`, add job-scoped permission and replace the login/metadata configuration:

```yaml
permissions:
  contents: read
  packages: write

steps:
  # existing checkout/download/setup steps remain unchanged
  - uses: docker/login-action@dbcb813823bdd20940b903addbd779551569679f # v4.6.0
    with:
      registry: ghcr.io
      username: ${{ github.actor }}
      password: ${{ secrets.GITHUB_TOKEN }}
  - uses: docker/metadata-action@dc802804100637a589fabce1cb79ff13a1411302 # v6.2.0
    id: meta
    with:
      images: ghcr.io/hypnguyen1209/v-artex
      tags: |
        type=ref,event=tag
        type=raw,value=latest
```

Change the Docker comment from Docker Hub to GitHub Container Registry and update the README command comment to say it pulls `ghcr.io/hypnguyen1209/v-artex`.

- [ ] **Step 4: Verify GREEN and validate Compose**

Run:

```bash
gofmt -w cmd/artex/distribution_test.go
go test ./cmd/artex -run TestDistributionUsesOwnedContainerImage -count=1
POSTGRES_PASSWORD=test docker compose config >/dev/null
```

Expected: PASS and Compose exits 0.

- [ ] **Step 5: Commit**

```bash
git add cmd/artex/distribution_test.go .github/workflows/release.yml docker-compose.yml README.md
git commit -m "fix(release): publish the V-ARTEX container"
```

### Task 2: Make skill paths portable

**Files:**
- Modify: `server/skill_upload_test.go`
- Modify: `server/server_mgmt.go`
- Modify: `.github/workflows/ci.yml`

- [ ] **Step 1: Write failing path-normalization tests**

Add to `server/skill_upload_test.go`:

```go
func TestNormalizeSkillAPIPath(t *testing.T) {
	for in, want := range map[string]string{
		`scripts\run.py`:      "scripts/run.py",
		`references\api\x.md`: "references/api/x.md",
		"assets/icon.png":     "assets/icon.png",
	} {
		if got := normalizeSkillAPIPath(in); got != want {
			t.Errorf("normalizeSkillAPIPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWalkSkillFilesReturnsAPIPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "run.py"), []byte("pass"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := walkSkillFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"scripts/": true, "scripts/run.py": true}
	for _, entry := range entries {
		delete(want, entry)
		if strings.Contains(entry, `\`) {
			t.Errorf("walkSkillFiles returned a backslash path %q", entry)
		}
	}
	if len(want) != 0 {
		t.Fatalf("walkSkillFiles missing entries: %v", want)
	}
}
```

- [ ] **Step 2: Run the test and verify RED**

Run: `go test ./server -run '^(TestNormalizeSkillAPIPath|TestWalkSkillFilesReturnsAPIPaths)$' -count=1`

Expected: build FAIL because `normalizeSkillAPIPath` does not exist.

- [ ] **Step 3: Implement one canonical API-path helper**

Add to `server/server_mgmt.go` and use it in both `skillRelPath` and `walkSkillFiles`:

```go
func normalizeSkillAPIPath(file string) string {
	return path.Clean(strings.ReplaceAll(filepath.ToSlash(file), `\`, "/"))
}
```

Replace `clean := filepath.Clean(file)` with `clean := normalizeSkillAPIPath(file)`. Immediately after `filepath.Rel`, normalize `rel` before appending it:

```go
rel = normalizeSkillAPIPath(rel)
if d.IsDir() {
	entries = append(entries, rel+"/")
} else {
	entries = append(entries, rel)
}
```

Add this native CI step before the native build:

```yaml
- name: Test native skill paths
  run: go test -count=1 ./server -run '^(TestSkillRelPath|TestNormalizeSkillAPIPath|TestWalkSkillFilesReturnsAPIPaths)$'
```

- [ ] **Step 4: Verify GREEN**

Run:

```bash
gofmt -w server/server_mgmt.go server/skill_upload_test.go
go test ./server -run '^(TestSkillRelPath|TestNormalizeSkillAPIPath|TestWalkSkillFilesReturnsAPIPaths)$' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server/server_mgmt.go server/skill_upload_test.go .github/workflows/ci.yml
git commit -m "fix(server): normalize skill API paths"
```

### Task 3: Tolerate model tool-name drift

**Files:**
- Create: `agent/toolalias_test.go`
- Modify: `agent/toolcatalog.go`
- Modify: `agent/assembly.go`

- [ ] **Step 1: Write failing alias tests**

Create `agent/toolalias_test.go` with tests that require the new API:

```go
package agent

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

func TestToolAliasNames(t *testing.T) {
	for name, want := range map[string][]string{
		"WebFetch":  {"webfetch", "web_fetch", "WEBFETCH"},
		"MultiEdit": {"multiedit", "multi_edit", "MULTIEDIT"},
		"LS":        {"ls"},
		"sleep":     nil,
	} {
		if got := toolAliasNames(name); !slices.Equal(got, want) {
			t.Errorf("toolAliasNames(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestAddToolAliasesDelegatesAndAvoidsCollisions(t *testing.T) {
	called := false
	base := actool.Build(actool.Spec{
		Name: "WebFetch", Description: "fetch",
		Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			called = true
			return actool.Text("ok"), nil
		},
	})
	existing := actool.Build(actool.Spec{Name: "webfetch", Description: "existing"})
	tools, deferred := addToolAliases([]actool.CoreTool{base, existing}, nil)

	byName := map[string]actool.CoreTool{}
	for _, tool := range tools {
		byName[tool.Name()] = tool
	}
	if byName["webfetch"] != existing {
		t.Fatal("alias replaced an existing tool")
	}
	alias := byName["web_fetch"]
	if alias == nil || !slices.Contains(deferred, "web_fetch") {
		t.Fatal("snake-case alias was not registered as deferred")
	}
	if _, err := alias.Call(context.Background(), json.RawMessage(`{}`), nil); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("alias did not delegate to the canonical handler")
	}
}
```

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./agent -run '^(TestToolAliasNames|TestAddToolAliasesDelegatesAndAvoidsCollisions)$' -count=1`

Expected: build FAIL because `toolAliasNames` and `addToolAliases` do not exist.

- [ ] **Step 3: Implement aliases in `agent/toolcatalog.go`**

Add `strings` to imports, then add:

```go
type aliasTool struct {
	actool.CoreTool
	name string
}

func (a *aliasTool) Name() string { return a.name }

func toolAliasNames(name string) []string {
	if name == strings.ToLower(name) {
		return nil
	}
	candidates := []string{strings.ToLower(name), camelToSnake(name), strings.ToUpper(name)}
	out := make([]string, 0, len(candidates))
	seen := map[string]bool{name: true}
	for _, candidate := range candidates {
		if candidate != "" && !seen[candidate] {
			seen[candidate] = true
			out = append(out, candidate)
		}
	}
	return out
}

func camelToSnake(value string) string {
	runes := []rune(value)
	var out strings.Builder
	for i, current := range runes {
		if current >= 'A' && current <= 'Z' {
			if i > 0 {
				previous := runes[i-1]
				var next rune
				if i+1 < len(runes) {
					next = runes[i+1]
				}
				if (previous >= 'a' && previous <= 'z') || (next >= 'a' && next <= 'z') {
					out.WriteByte('_')
				}
			}
			out.WriteRune(current + ('a' - 'A'))
			continue
		}
		out.WriteRune(current)
	}
	return out.String()
}

func addToolAliases(tools []actool.CoreTool, deferred []string) ([]actool.CoreTool, []string) {
	taken := map[string]bool{}
	for _, tool := range tools {
		taken[tool.Name()] = true
	}
	for _, name := range deferred {
		taken[name] = true
	}
	out := tools
	extra := deferred
	for _, tool := range tools {
		for _, alias := range toolAliasNames(tool.Name()) {
			if taken[alias] {
				continue
			}
			taken[alias] = true
			out = append(out, &aliasTool{CoreTool: tool, name: alias})
			extra = append(extra, alias)
		}
	}
	return out, extra
}
```

Wire it immediately after `findingWorkflowTools` and before `guardPanic` in `agent/assembly.go`:

```go
out, def.FindingGuidance = findingWorkflowTools(agentKey, out)
out, def.Deferred = addToolAliases(out, def.Deferred)
```

- [ ] **Step 4: Verify GREEN and the wider agent package**

Run:

```bash
gofmt -w agent/toolcatalog.go agent/toolalias_test.go agent/assembly.go
go test ./agent -run '^(TestToolAliasNames|TestAddToolAliasesDelegatesAndAvoidsCollisions)$' -count=1
go test ./agent -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/toolcatalog.go agent/toolalias_test.go agent/assembly.go
git commit -m "fix(agent): accept safe built-in tool aliases"
```

### Task 4: Make transcript updates resilient to SSE loss

**Files:**
- Create: `web/src/components/task-resilience.test.mjs`
- Modify: `web/src/app/(main)/function/tasks/detail/_tabs/sessions-tab.tsx`

- [ ] **Step 1: Write a failing source-contract test**

```js
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const sessions = readFileSync(
  new URL("../app/(main)/function/tasks/detail/_tabs/sessions-tab.tsx", import.meta.url),
  "utf8",
);

test("sessions poll the active transcript only while SSE is down", () => {
  assert.match(sessions, /fallbackMaxRef/);
  assert.match(sessions, /if \(sseLive\) return/);
  assert.match(sessions, /api\.activityHistory\(taskId, key, 0, PAGE\)/);
  assert.match(sessions, /setTimeout\(\(\) => void poll\(\), 2000\)/);
  assert.match(sessions, /clearTimeout\(timer\)/);
});
```

- [ ] **Step 2: Run the test and verify RED**

Run: `cd web && node --test src/components/task-resilience.test.mjs`

Expected: FAIL because `fallbackMaxRef` and the fallback effect are absent.

- [ ] **Step 3: Add the SSE-down fallback effect**

Add this ref beside the existing SSE refs:

```tsx
const fallbackMaxRef = React.useRef<Record<string, number>>({});
```

After the SSE effect, add:

```tsx
React.useEffect(() => {
  if (sseLive) return;
  let alive = true;
  let timer: ReturnType<typeof setTimeout>;
  const poll = async () => {
    const key = activeKeyRef.current;
    try {
      const response = await api.activityHistory(taskId, key, 0, PAGE);
      if (!alive) return;
      const maxSeq = response.items.reduce((maximum, item) => Math.max(maximum, item.seq), -1);
      if (maxSeq > (fallbackMaxRef.current[key] ?? -1)) {
        fallbackMaxRef.current[key] = maxSeq;
        patchStore(key, (state) => ({ ...state, items: mergeBySeq(state.items, response.items) }));
      }
    } catch {
      // Best effort: the next poll or SSE reconnection compensates.
    } finally {
      if (alive) timer = setTimeout(() => void poll(), 2000);
    }
  };
  timer = setTimeout(() => void poll(), 2000);
  return () => {
    alive = false;
    clearTimeout(timer);
  };
}, [sseLive, taskId, patchStore]);
```

- [ ] **Step 4: Verify GREEN**

Run:

```bash
cd web
node --test src/components/task-resilience.test.mjs
npx biome check 'src/app/(main)/function/tasks/detail/_tabs/sessions-tab.tsx' src/components/task-resilience.test.mjs
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/components/task-resilience.test.mjs 'web/src/app/(main)/function/tasks/detail/_tabs/sessions-tab.tsx'
git commit -m "fix(web): poll transcripts while SSE is down"
```

### Task 5: Model report load failures explicitly

**Files:**
- Modify: `web/src/components/task-resilience.test.mjs`
- Modify: `web/src/app/(main)/function/tasks/detail/_tabs/report-tab.tsx`

- [ ] **Step 1: Extend the test with the missing report contract**

Append:

```js
const report = readFileSync(
  new URL("../app/(main)/function/tasks/detail/_tabs/report-tab.tsx", import.meta.url),
  "utf8",
);

test("reports separate load errors from empty success and remount by task", () => {
  assert.match(report, /<TaskReport key=\{taskId\} taskId=\{taskId\}/);
  assert.match(report, /status: "loading"/);
  assert.match(report, /status: "error"/);
  assert.match(report, /status: "ready"/);
  assert.match(report, /role="alert"/);
  assert.match(report, />Retry</);
});
```

- [ ] **Step 2: Run the test and verify RED**

Run: `cd web && node --test src/components/task-resilience.test.mjs`

Expected: the transcript test passes and the report test FAILS.

- [ ] **Step 3: Replace the report's parallel state with a discriminated state**

Use this component boundary and state:

```tsx
export function ReportTab({ taskId }: { taskId: string }) {
  return <TaskReport key={taskId} taskId={taskId} />;
}

type ReportState = { status: "loading" | "error" } | { status: "ready"; text: string };

function TaskReport({ taskId }: { taskId: string }) {
  const [state, setState] = React.useState<ReportState>({ status: "loading" });
  const [attempt, setAttempt] = React.useState(0);
  const [copied, setCopied] = React.useState(false);
  const report = state.status === "ready" ? state.text : "";

  React.useEffect(() => {
    let active = true;
    setState({ status: "loading" });
    api.report(taskId).then(
      (text) => active && setState({ status: "ready", text }),
      () => active && setState({ status: "error" }),
    );
    return () => {
      active = false;
    };
  }, [taskId, attempt]);

  function retry() {
    setAttempt((current) => current + 1);
  }
```

Use these first two render branches before the existing ready-state branches:

```tsx
if (state.status === "loading") {
  content = (
    <div role="status" className="flex flex-col items-center justify-center gap-2 rounded-md border border-dashed py-16 text-muted-foreground text-sm">
      <FileTextIcon className="size-8 opacity-40" />
      Loading…
    </div>
  );
} else if (state.status === "error") {
  content = (
    <div role="alert" className="flex flex-col items-center justify-center gap-2 rounded-md border border-dashed py-16 text-sm">
      <p>Failed to load the report.</p>
      <Button size="sm" variant="outline" onClick={retry}>Retry</Button>
    </div>
  );
} else if (report) {
  content = (
    <div className="max-h-[60vh] overflow-auto rounded-md border bg-muted/20 p-4">
      <Markdown text={report} />
    </div>
  );
} else {
  content = (
    <div className="flex flex-col items-center justify-center gap-2 rounded-md border border-dashed py-16 text-muted-foreground text-sm">
      <FileTextIcon className="size-8 opacity-40" />
      No report yet
    </div>
  );
}
```

Keep Copy visible only when `report` is non-empty.

- [ ] **Step 4: Verify GREEN**

Run:

```bash
cd web
node --test src/components/task-resilience.test.mjs
npx biome check 'src/app/(main)/function/tasks/detail/_tabs/report-tab.tsx' src/components/task-resilience.test.mjs
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/components/task-resilience.test.mjs 'web/src/app/(main)/function/tasks/detail/_tabs/report-tab.tsx'
git commit -m "fix(web): expose report load failures"
```

### Task 6: Fit the tasks page at tablet widths

**Files:**
- Modify: `web/playwright.config.ts`
- Modify: `web/e2e/smoke.spec.ts`
- Modify: `web/src/hooks/use-mobile.ts`
- Modify: `web/src/components/ui/sidebar.tsx`
- Modify: `web/src/app/(main)/function/tasks/page.tsx`

- [ ] **Step 1: Add a tablet project and a failing table-width assertion**

Add to `projects` in `web/playwright.config.ts`:

```ts
{ name: "tablet-chromium", use: { ...devices["Desktop Chrome"], viewport: { width: 1024, height: 768 } } },
```

Append to `web/e2e/smoke.spec.ts`:

```ts
test("tasks table fits its tablet container without horizontal scrolling", async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== "tablet-chromium");
  await page.goto("/function/tasks");
  const table = page.locator('[data-slot="table-container"]').first();
  await expect(table).toBeVisible();
  await expect
    .poll(() => table.evaluate((element) => element.scrollWidth <= element.clientWidth + 1))
    .toBe(true);
});
```

- [ ] **Step 2: Run the tablet test and verify RED**

Run: `cd web && npx playwright test --project=tablet-chromium --grep 'tasks table fits'`

Expected: FAIL because all task columns still force horizontal scrolling.

- [ ] **Step 3: Add a reusable tablet breakpoint and auto-collapse the sidebar**

Refactor `web/src/hooks/use-mobile.ts` to share a private `useBreakpoint` helper and export:

```ts
const MOBILE_BREAKPOINT = 768;
const TABLET_BREAKPOINT = 1280;

function useBreakpoint(breakpoint: number) {
  const [below, setBelow] = React.useState<boolean | undefined>(undefined);
  React.useEffect(() => {
    const query = window.matchMedia(`(max-width: ${breakpoint - 1}px)`);
    const update = () => setBelow(window.innerWidth < breakpoint);
    query.addEventListener("change", update);
    update();
    return () => query.removeEventListener("change", update);
  }, [breakpoint]);
  return below;
}

export function useIsMobile() {
  return !!useBreakpoint(MOBILE_BREAKPOINT);
}

export function useIsTablet() {
  return useBreakpoint(TABLET_BREAKPOINT);
}
```

Import `useIsTablet` in `sidebar.tsx`, call it beside `useIsMobile`, and add this transition-aware effect after `setOpen`:

```tsx
const previousTablet = React.useRef<boolean | undefined>(undefined);
React.useEffect(() => {
  if (isTablet === undefined || isMobile) {
    previousTablet.current = isTablet;
    return;
  }
  const firstMeasurement = previousTablet.current === undefined;
  if (firstMeasurement) {
    if (isTablet) setOpen(false);
  } else if (previousTablet.current !== isTablet) {
    setOpen(!isTablet);
  }
  previousTablet.current = isTablet;
}, [isTablet, isMobile, setOpen]);
```

- [ ] **Step 4: Apply container-priority classes to the task table**

Wrap the current task `Table` in `<div className="@container">`. Apply matching header and body classes:

| Column | Header and body class |
|---|---|
| Description | `hidden @5xl:table-cell` |
| Goal progress | `hidden @3xl:table-cell` |
| Running workers | `hidden @4xl:table-cell` |
| Created at | `hidden @6xl:table-cell` |
| Duration | `hidden @6xl:table-cell` |
| Token | `hidden @7xl:table-cell` |

Remove `sticky right-0`, the background override, z-index, and shadow from both Actions cells, leaving `className="text-right"`. Keep selection, ID, Name, Goal, Status, Findings, and Actions visible at every container width.

- [ ] **Step 5: Verify GREEN and all browser projects**

Run:

```bash
cd web
npx playwright test --project=tablet-chromium --grep 'tasks table fits'
npm run check
npm run test:e2e
```

Expected: tablet test PASS; desktop, tablet, and mobile projects PASS.

- [ ] **Step 6: Commit**

```bash
git add web/playwright.config.ts web/e2e/smoke.spec.ts web/src/hooks/use-mobile.ts web/src/components/ui/sidebar.tsx 'web/src/app/(main)/function/tasks/page.tsx'
git commit -m "fix(web): fit task controls at tablet widths"
```

### Task 7: Full verification and review

**Files:**
- Verify all changed files; no new production behavior.

- [ ] **Step 1: Run formatting and static checks**

```bash
gofmt -w cmd/artex/distribution_test.go server/server_mgmt.go server/skill_upload_test.go agent/toolcatalog.go agent/toolalias_test.go agent/assembly.go
git diff --check
POSTGRES_PASSWORD=test docker compose config >/dev/null
if command -v actionlint >/dev/null 2>&1; then actionlint; fi
```

Expected: all commands exit 0.

- [ ] **Step 2: Run the complete backend suites**

With the repository test PostgreSQL available through `ARTEX_PG_DSN`, run:

```bash
go test -count=1 -p 1 ./...
go test -race -count=1 -p 1 ./...
```

Expected: PASS with no race reports.

- [ ] **Step 3: Run the complete frontend suites**

```bash
cd web
npm test
npm run check
npm run build:static
npm run test:e2e
```

Expected: PASS for Node tests, Biome, static export, and all three Playwright projects.

- [ ] **Step 4: Inspect the final distribution and change scope**

```bash
cd ..
rg -n 'autumn27/artex|DOCKERHUB_' .github docker-compose.yml README.md
git status --short
git log --oneline --decorate -8
```

Expected: `rg` returns no matches, status is clean, and history shows one focused commit per task.

- [ ] **Step 5: Request spec and quality review before handoff**

Use `superpowers:requesting-code-review`, resolve any validated finding with a new RED/GREEN cycle, then rerun the affected targeted test and the complete gate it belongs to.
