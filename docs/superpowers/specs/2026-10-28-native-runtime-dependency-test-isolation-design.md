# Native Runtime, Dependency, and Test Isolation Hardening Design

**Status:** Approved for specification

## Goal

Close the three residual risks on the runtime-safety branch:

1. execute the changed portable runtime paths on native Windows and macOS runners;
2. remove npm advisories using non-breaking dependency updates; and
3. make `TestCompanyScopeAttribution` safe to repeat against a reused PostgreSQL database.

The work must preserve the current runtime behavior, database data, release targets, and English-only UI/default-data contract.

## Scope

### Native runtime verification

Add a permanent native job to `.github/workflows/ci.yml` with a matrix containing `windows-2025` and `macos-15`. Reuse the workflow's pinned `actions/checkout` and `actions/setup-go` revisions.

Each runner will execute the paths affected by this branch without requiring a platform-specific PostgreSQL installation:

- all `cmd/artex` tests, including real filesystem descriptor behavior and address classification;
- the pure in-memory global run-gate tests in `server`;
- the planner intent-budget and admission tests in `agent`; and
- a native `go build ./cmd/artex` smoke build.

Linux continues to own the complete PostgreSQL-backed normal and race suites. Cross-compilation remains in the release workflow, while the new matrix proves that selected test binaries actually execute on both release operating systems.

### Dependency advisories

Update npm packages only within the semver ranges already allowed by `web/package.json`. The expected direct updates include Next.js, PostCSS, shadcn, and Zod; transitive packages will follow from the regenerated lockfile.

Do not run `npm audit fix --force`, accept a major-version downgrade, add an override, or suppress an advisory. The acceptance target is a clean `npm audit --json` report with zero advisories. If a remaining advisory requires a breaking update, stop and reassess instead of forcing it.

After the lockfile update, run the existing 64 unit tests, Biome check, static export/typecheck, and desktop/mobile Playwright smoke tests. This protects the static frontend contract even though several vulnerable packages are build-time dependencies rather than part of the Go-served runtime.

### Reusable-database fixture isolation

The existing `TestCompanyScopeAttribution` uses a fixed documentation CIDR and deletes only newly inserted assets. It leaves the created company and cascading scope rows behind, so a reused database can attribute the next run's fixed IP to an older company.

Keep the production company-attribution code unchanged. Make the fixture unique for each run and register cleanup for the company; deleting the company removes its scope rows through the existing foreign key. Preserve the asset cleanup and close behavior.

The regression proof is:

1. before the fix, repeat the focused test on the same fresh database and observe leaked `ScopeCo`/scope rows or incorrect attribution;
2. after the fix, run the focused test twice against the same database; and
3. query the database to confirm that no company, company-scope, or test asset rows remain.

## Data and Security Properties

- No production database migration or runtime write path changes.
- No existing user company, scope, asset, or setting is deleted.
- Test cleanup targets only the company ID created by that test.
- Dependency remediation is lockfile-driven and keeps declared compatibility ranges.
- GitHub Actions retain read-only default permissions and full-SHA action pins.
- Native jobs do not receive deployment secrets and do not perform releases.

## Verification and Delivery

Local verification must include focused RED/GREEN evidence, full Go tests on a fresh PostgreSQL 17 database, Docker Go 1.26 race tests, frontend unit/check/static build, Playwright desktop/mobile, `npm audit`, `actionlint`, the full-SHA scanner, `git diff --check`, and commit identity checks.

Push `feature/runtime-safety-foundation`, open a pull request against `main`, and require successful `windows-2025` and `macos-15` jobs as the native-runtime evidence. Keep the native CI job in the final branch so later portability regressions cannot silently return.

Every new commit must use author and committer `hypnguyen1209 <hypnguyen1209@gmail.com>`. Do not deploy or merge as part of this hardening pass.
