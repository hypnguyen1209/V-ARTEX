# GraphOverview Stale Test Fix Design

## Problem

`TestGraphOverviewExpandsAssociatedCompanyScope` expects `coverage.scope`,
`company_scope`, and `company_keywords` in `graph_overview`. Commit `06a43f3`
intentionally removed those fields to keep the planner overview compact, but the
test was not updated. The test therefore fails consistently even though the
current runtime behavior matches the intended GraphOverview contract.

## Decision

Update the stale test instead of restoring the removed runtime fields. The fix
must not change production behavior, database schemas, persisted data, or the
English-only translation and migration work.

## Changes

- Rename the test to describe its remaining behavior: associated company assets
  stay available in task context.
- Remove assertions for the intentionally removed `coverage.scope`,
  `company_scope`, and `company_keywords` fields.
- Keep the existing assertions that verify:
  - company assets are linked to the task with provenance;
  - GraphOverview reports the associated host count;
  - untested, task-scoped, and company-scoped asset queries expose the asset;
  - planner intents retain the asset for workers;
  - coverage-disabled tasks retain company asset context without metrics.
- Assert that GraphOverview does not restore the removed `scope` payload, so a
  future change cannot silently reintroduce the prompt-size regression.

## Verification

The existing failing test is the RED reproduction. After the test correction:

1. Run the renamed targeted test against a disposable PostgreSQL database.
2. Run the complete `agent` package tests against that database.
3. Run the full Go repository suite serially against a fresh disposable
   PostgreSQL database and require zero failures, allowing only the existing
   opt-in live-test skip.
4. Re-run the English-only Han scan and confirm the worktree is clean after the
   implementation commit.

## Scope

No runtime feature, compatibility layer, dependency, or unrelated refactor is
included.
