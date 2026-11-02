# Targeted artex-global Improvements Design

## Goal

Port the six proven reliability improvements identified in `jiwoochris/artex-global` without importing its multilingual stack, test framework, or unrelated detection content.

## Scope

The change covers:

1. Publish and consume V-ARTEX's own container image from `ghcr.io/hypnguyen1209/v-artex`.
2. Keep skill API paths slash-separated on every operating system.
3. Accept common lowercase and snake-case aliases for CamelCase built-in tools without exposing duplicate schemas.
4. Poll the active transcript only while its SSE connection is unavailable.
5. Distinguish report loading, failure, empty success, and ready states, with retry support.
6. Prevent the task table from crowding or overlapping at tablet widths and test that viewport in Playwright.

The change does not add i18n, Vitest, a detection pack, new runtime dependencies, or broad UI refactoring.

## Architecture

### Container ownership

The release workflow will authenticate to GitHub Container Registry with the repository-scoped `GITHUB_TOKEN`, request only the `packages: write` permission needed by the Docker job, and publish the existing multi-architecture image as `ghcr.io/hypnguyen1209/v-artex`. Compose and the Docker installation/update documentation will consume that same image. A static regression test will keep the workflow, Compose file, and documentation from drifting back to an upstream namespace.

### Portable skill paths

Skill paths are API identifiers, so they remain slash-separated independent of the host OS. Validation will use `path.Clean`; directory walking will convert `filepath.Rel` output with `filepath.ToSlash`. Filesystem joins remain responsible for crossing from the API representation into OS-native paths. Tests will cover nested files and directories, and Windows CI will run the relevant server tests.

### Tool-name drift tolerance

Agent assembly will add non-conflicting lowercase, snake-case, and uppercase aliases only for tools whose canonical names contain uppercase characters. Each alias delegates all behavior to the canonical tool and overrides only `Name()`. Alias names are added to the deferred list so provider-facing schemas continue to show only canonical tools. Tests will cover name conversion, delegation, collision avoidance, and assembly behavior.

### Transcript resilience

The session view will retain SSE as the primary transport. While `sseLive` is false, one effect will fetch the active session's latest activity page every two seconds, merge entries by sequence, and avoid state writes when no newer sequence exists. The timer stops on unmount, task change, or SSE recovery. Existing request and merge helpers will be reused.

### Report state

The report tab will use a discriminated `loading | error | ready` state. Changing tasks remounts the task-specific report component so stale content and copy controls cannot survive the transition. A failed request shows an accessible error and Retry action; a successful empty response still shows “No report yet.”

### Tablet layout

The task table will respond to its content width through a container query and progressively hide secondary columns while retaining selection, identity, goal, status, findings, and actions. The sidebar will auto-collapse only in the 768–1279 px tablet range and preserve desktop/mobile behavior. Playwright will add one 1024×768 Chromium project and continue asserting that the tasks page has no document-level horizontal overflow.

## Error Handling and Safety

- A failed image publication cannot affect GitHub Release artifacts because jobs remain independent after verified builds.
- Tool aliases never overwrite a canonical or previously registered name.
- Transcript fallback failures are best-effort and retried on the next tick; they do not disable SSE reconnection.
- Report failures are visible and retryable rather than being represented as valid empty data.
- Responsive changes hide only redundant table summaries; task details remain available from the retained row action.

## Verification

Implementation follows test-first cycles. Targeted tests will prove each behavior before the full gates run:

- Go unit tests for skill paths and tool aliases, including the Windows CI slice.
- Frontend behavioral tests for report state and SSE fallback source contracts using the repository's existing Node test style.
- Playwright desktop, tablet, and mobile smoke coverage.
- `go test ./...`, `go test -race ./...`, `npm test`, `npm run check`, `npm run build:static`, and `npm run test:e2e`.
- Static inspection of the release/Compose image name and a clean worktree before handoff.

## Success Criteria

- Docker deployments and releases reference only the V-ARTEX-owned GHCR image.
- Nested skill files round-trip on Windows without backslashes entering the API.
- Drifted calls such as `bash` and `web_fetch` reach their canonical handlers without duplicate provider schemas.
- The active transcript advances without reload when SSE is unavailable.
- Report errors are distinguishable from empty reports and cannot expose the prior task's copy action.
- The task page passes overflow checks at desktop, 1024×768 tablet, and Pixel 7 viewports.
