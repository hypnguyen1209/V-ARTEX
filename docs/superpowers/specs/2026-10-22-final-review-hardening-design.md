# Final Review Hardening Design

**Date:** 2026-10-22

**Status:** Approved for implementation planning

## Context

The runtime-safety and delivery work passes its full backend, race, doctor,
frontend, browser, and workflow acceptance matrix. A final independent review
identified four remaining false-confidence risks:

- the reusable CI workflow uses mutable action tags even though the release
  workflow pins the same actions by commit;
- `artex doctor` reports every listen failure as a harmless occupied-port
  warning;
- the schema check proves only that six relation names resolve; and
- filesystem checks accept the wrong file type and do not prove access.

These are narrow verification defects. The existing planner, run-gate,
shutdown, migration, UI, and release dependency designs remain unchanged.

## Goals

- Make the reusable verification workflow as immutable as the publishing
  workflow.
- Make doctor fail when the configured server or proxy address cannot be used,
  while preserving a warning for a genuinely occupied address.
- Make doctor prove that required runtime columns exist and are selectable,
  without running migrations or changing the database.
- Make doctor validate the expected filesystem object type and read access
  without creating or modifying anything.
- Preserve the existing JSON schema, check names, exit-code contract, secret
  redaction, time bounds, and read-only guarantees.

## Non-goals

- A generalized database migration/version framework.
- Exercising every runtime query or validating indexes, constraints, and
  PostgreSQL performance.
- Probing write permissions, creating temporary files, or repairing paths.
- Changing which paths are required or optional.
- Refactoring the doctor command into additional packages.

## CI Action Integrity

Every external `uses:` entry in `.github/workflows/ci.yml` will use the same
reviewed full commit SHA and version comment already used by
`.github/workflows/release.yml`:

- `actions/checkout` v7.0.1;
- `actions/setup-go` v7.0.0;
- `actions/setup-node` v7.1.0; and
- `actions/upload-artifact` v7.0.2.

All repeated uses will be pinned individually. Workflow permissions, job
structure, cache configuration, commands, and versions remain unchanged.
`actionlint` and a repository scan that rejects tag-based external actions will
guard the result.

## Address Diagnosis

Doctor continues to use a transient `net.Listen("tcp", address)` probe and
closes a successful listener immediately. A failed probe is classified by its
actual cause:

- `EADDRINUSE` is `warn`, because an already-running V-ARTEX instance may own
  the configured address;
- malformed addresses, invalid ports, non-local addresses, permission errors,
  and every other listen failure are `fail` because normal startup would fail
  on the same input.

The output remains sanitized: it may name the supplied address but never emits
the raw platform error. Unit tests cover an occupied address, an invalid port,
an unavailable non-local address through an injected error, and success.

## Schema Compatibility and Read Access

`postgres.schema` will replace relation-name-only checks with a fixed,
code-owned contract for the existing required tables. For each table it runs a
bounded query of this form:

```sql
SELECT <required runtime columns> FROM <required table> LIMIT 0
```

The table and column identifiers are hard-coded constants, never user input.
`LIMIT 0` returns no application rows and performs no write, but PostgreSQL
still resolves the table and columns and checks `SELECT` permission. The column
sets cover the fields used by startup/task loading for `settings`, `tasks`,
`explorations`, `exploration_nodes`, `llm_profiles`, and `agents`. This catches
missing tables, partially migrated schemas, and unreadable tables.

The first failed table produces `fail`, identifies only the table, and retains
the existing migration/permission remediation without embedding the raw
database error. The server-version detail is appended only after all probes
pass. Existing profile-shape validation remains a separate check.

Tests will prove:

- a current migrated schema passes;
- a required-column resolution failure fails `postgres.schema`;
- a `SELECT` permission failure fails `postgres.schema`; and
- the probes leave schema, rows, sequences, functions, and triggers unchanged.

## Filesystem Path Validation

Each path check receives an expected kind:

- `path.data`, `path.workspace`, and `path.skills` expect directories;
- `path.jwt_key` expects a regular file.

Missing-path severity remains compatible: required paths fail and optional
paths warn. If a path exists but has the wrong type, the result fails even when
the path is optional, because startup cannot use that object. Symlinks retain
the existing `os.Stat` behavior and are validated by the type of their target.

After type validation, doctor performs a non-mutating access probe:

- directories must be openable and readable as directories; and
- regular files must be openable for reading, without consuming or printing
  their contents.

An access failure is `fail`. Successful detail retains mode and size. Ownership
is not used as a pass/fail signal because Go does not expose it portably through
`os.FileInfo`; access is proven directly under the effective account instead.
The injected runtime boundary will keep permission-error tests deterministic
without relying on the test process UID.

## Error Handling and Compatibility

- No raw listen, SQL, or filesystem error is copied into public output.
- All database probes share the existing three-second schema-check deadline.
- Doctor still opens PostgreSQL directly and never calls `db.Open`, so it
  cannot run migrations.
- Existing result names, result JSON fields, and exit codes do not change.
- Warnings still permit exit `0`; newly accurate address, schema, type, and
  access failures produce exit `1` through the existing aggregation logic.
- Normal server startup behavior is untouched.

## Verification

Implementation follows RED-GREEN-REFACTOR:

1. Reproduce mutable-action detection, then pin the four action families and
   run `actionlint` on both workflows.
2. Add focused address tests that fail under the current all-warning behavior,
   implement classification, and rerun the command tests.
3. Add schema resolution/permission tests that fail under `to_regclass`,
   implement `LIMIT 0` probes, and rerun PostgreSQL read-only snapshots.
4. Add wrong-kind/access tests that fail under `os.Stat`-only checks, implement
   access probes, and rerun filesystem immutability tests.
5. Run the full Go suite and race detector, doctor JSON smoke test, frontend
   unit/check/static build, desktop/mobile Playwright, workflow lint, metadata
   audit, and repository cleanliness checks.

## Acceptance Criteria

- No external action in `ci.yml` is referenced by a mutable tag.
- An occupied address remains `warn`; every other bind failure is `fail`.
- A missing required column or missing table `SELECT` privilege makes
  `postgres.schema` fail without exposing the underlying error.
- A directory where the JWT key should be, a file where a required directory
  should be, or an inaccessible expected object makes its path check fail.
- Doctor remains read-only, bounded, JSON-compatible, and secret-safe.
- All existing acceptance gates pass on the final commit.
