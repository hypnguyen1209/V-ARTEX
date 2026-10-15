# GraphOverview Stale Test Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Correct the stale GraphOverview test so the full Go suite matches the intentionally compact runtime contract.

**Architecture:** Keep production behavior unchanged. Update the existing PostgreSQL integration test to verify associated-company asset context while explicitly protecting the removal of the old `coverage.scope` payload.

**Tech Stack:** Go, `testing`, PostgreSQL 16, Docker, Git

---

### Task 1: Align the associated-company GraphOverview test with the current contract

```json:metadata
{"modelTier": "mechanical"}
```

**Files:**
- Modify/Test: `agent/blackboard_inheritance_test.go:28-175`

- [ ] **Step 1: Reproduce the stale assertion before editing**

Start a disposable PostgreSQL instance and run the existing test:

```bash
docker run -d --rm --name artex-graph-test \
  -e POSTGRES_USER=artex \
  -e POSTGRES_PASSWORD=artex \
  -e POSTGRES_DB=artex_test \
  -p 127.0.0.1:55438:5432 postgres:16-alpine
until docker exec artex-graph-test pg_isready -U artex -d artex_test; do sleep 0.25; done
ARTEX_PG_DSN='postgres://artex:artex@127.0.0.1:55438/artex_test?sslmode=disable' \
  go test ./agent -run '^TestGraphOverviewExpandsAssociatedCompanyScope$' -count=1 -v
```

Expected: FAIL at `task scope missing: <nil>`. This is the RED evidence that the test still expects the intentionally removed `coverage.scope` field.

- [ ] **Step 2: Make the smallest test-only correction**

Rename the test:

```go
func TestGraphOverviewKeepsAssociatedCompanyAssetsInTaskContext(t *testing.T) {
```

Replace the obsolete block that reads `coverage["scope"]`, expands `company_scope`, and checks `company_keywords` with this compact-contract assertion:

```go
if _, exists := coverage["scope"]; exists {
	t.Fatalf("graph overview unexpectedly restored removed scope payload: %#v", coverage["scope"])
}
```

Keep every other assertion unchanged, including company provenance, host count, untested/task/company queries, worker intent targeting, and coverage-disabled behavior. Do not modify production code.

- [ ] **Step 3: Format and verify the corrected targeted test**

```bash
gofmt -w agent/blackboard_inheritance_test.go
ARTEX_PG_DSN='postgres://artex:artex@127.0.0.1:55438/artex_test?sslmode=disable' \
  go test ./agent -run '^TestGraphOverviewKeepsAssociatedCompanyAssetsInTaskContext$' -count=3 -v
```

Expected: PASS three times with no skips.

- [ ] **Step 4: Run the complete agent package**

```bash
ARTEX_PG_DSN='postgres://artex:artex@127.0.0.1:55438/artex_test?sslmode=disable' \
  go test -p 1 ./agent -count=1
```

Expected: PASS with no failures.

- [ ] **Step 5: Run the complete Go repository suite serially**

```bash
ARTEX_PG_DSN='postgres://artex:artex@127.0.0.1:55438/artex_test?sslmode=disable' \
  go test -p 1 ./... -count=1
```

Expected: PASS across all packages. The opt-in `TestLiveContextReview` may remain skipped; no test may fail.

- [ ] **Step 6: Recheck English-only and diff invariants**

```bash
git diff --check
git grep -a -I -nP '[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}]' -- .
git status --short
```

Expected: no whitespace errors, no Han/CJK matches, and only the intended test file is changed before the implementation commit.

- [ ] **Step 7: Commit the correction and remove the disposable database**

```bash
git add agent/blackboard_inheritance_test.go
git commit -m "test(agent): align GraphOverview company context contract"
docker stop artex-graph-test
```

Expected: one implementation commit; the PostgreSQL container is removed.
