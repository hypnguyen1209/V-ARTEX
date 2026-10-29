# Native Runtime, Dependency, and Test Isolation Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate the remaining native-runtime, npm-advisory, and reusable-database test-isolation risks before integrating the runtime-safety branch.

**Architecture:** Keep production behavior unchanged. Harden one existing PostgreSQL fixture, refresh the npm lockfile within declared semver ranges, and add a permanent focused Windows/macOS job to the existing CI workflow. Linux remains responsible for the complete PostgreSQL and race suites; GitHub-hosted native runners execute the portability-sensitive tests and build the binary.

**Tech Stack:** Go 1.26+, PostgreSQL 17, Node.js 22, npm, Next.js, Playwright, GitHub Actions, actionlint, Docker, GitHub CLI.

---

## Execution Rules

- Work only in `/home/lab/V-ARTEX/.worktrees/runtime-safety-foundation` on `feature/runtime-safety-foundation`.
- Do not deploy or merge.
- Use author and committer `hypnguyen1209 <hypnguyen1209@gmail.com>` on every commit.
- Do not rewrite existing commits; add new commits.
- Do not use `npm audit fix --force`, dependency overrides, advisory suppression, or npm's breaking `shadcn@1.0.0` downgrade.
- Use a fresh PostgreSQL database for each full normal/race acceptance run and remove every temporary database/container.
- After each implementation task, run a fresh spec review and a fresh code-quality review before continuing.

### Task 1: Make the company-scope fixture reusable

**Files:**
- Modify: `db/company_scope_test.go:316-406`
- Test: `db/company_scope_test.go:316-406`

- [ ] **Step 1: Start an isolated PostgreSQL 17 fixture**

Run from the repository root:

```bash
export ARTEX_FIXTURE_PG="v-artex-fixture-pg-$(date +%Y%m%d%H%M%S)-$$"
docker run -d --name "$ARTEX_FIXTURE_PG" \
  -e POSTGRES_USER=artex \
  -e POSTGRES_PASSWORD=artex_test_password \
  -e POSTGRES_DB=artex_fixture \
  -p 127.0.0.1:55439:5432 \
  --health-cmd "pg_isready -U artex -d artex_fixture" \
  --health-interval 1s --health-timeout 3s --health-retries 30 \
  postgres:17
until [ "$(docker inspect --format '{{.State.Health.Status}}' "$ARTEX_FIXTURE_PG")" = healthy ]; do sleep 1; done
export ARTEX_PG_DSN='postgres://artex:artex_test_password@127.0.0.1:55439/artex_fixture?sslmode=disable'
```

Expected: the container becomes `healthy`; no production V-ARTEX container is touched.

- [ ] **Step 2: Capture the RED contamination evidence**

Run the existing test once, then inspect rows it should have removed:

```bash
go test ./db -run '^TestCompanyScopeAttribution$' -count=1
docker exec -e PGPASSWORD=artex_test_password "$ARTEX_FIXTURE_PG" \
  psql -U artex -d artex_fixture -Atc \
  "SELECT count(*) FROM companies WHERE name LIKE 'ScopeCo %'"
```

Expected before the fix: the test itself may pass, but the query prints a non-zero count. A second focused run can fail because the fixed `198.51.100.0/24` scope attributes the fixed IP to an older company.

- [ ] **Step 3: Implement the smallest fixture-only fix**

In `TestCompanyScopeAttribution`, keep the existing asset cleanup and production APIs. Replace the fixture identity/setup with the following shape:

```go
var cid int64
t.Cleanup(func() {
	if _, err := d.Exec(`DELETE FROM assets WHERE id > $1`, startMax); err != nil {
		t.Errorf("clean up company-scope assets: %v", err)
	}
	if cid != 0 {
		if _, err := d.Exec(`DELETE FROM companies WHERE id = $1`, cid); err != nil {
			t.Errorf("clean up company-scope company: %v", err)
		}
	}
	d.Close()
})

stamp := time.Now().UnixNano()
cid, _, err = cs.UpsertCompany(fmt.Sprintf("ScopeCo %d", stamp), "")
if err != nil {
	t.Fatalf("UpsertCompany: %v", err)
}

high := uint64(cid) >> 16 & 0xffff
low := uint64(cid) & 0xffff
root := fmt.Sprintf("scopetest-%d.invalid", cid)
sub := "api." + root
network := fmt.Sprintf("2001:db8:%x:%x::/64", high, low)
ipIn := fmt.Sprintf("2001:db8:%x:%x::9", high, low)
ipOut := fmt.Sprintf("2001:db8:%x:%x::9", high, low^0x8000)
outDomain := fmt.Sprintf("other-%d.invalid", cid)
```

Pass `network` instead of the fixed CIDR to `cs.AddScope`. Do not change production company, scope, or asset code.

- [ ] **Step 4: Prove GREEN on a reused database**

Run:

```bash
go test ./db -run '^TestCompanyScopeAttribution$' -count=2
docker exec -e PGPASSWORD=artex_test_password "$ARTEX_FIXTURE_PG" \
  psql -U artex -d artex_fixture -Atc \
  "SELECT (SELECT count(*) FROM companies WHERE name LIKE 'ScopeCo %') || ':' ||
          (SELECT count(*) FROM company_scope cs JOIN companies c ON c.id=cs.company_id WHERE c.name LIKE 'ScopeCo %') || ':' ||
          (SELECT count(*) FROM assets WHERE domain LIKE 'scopetest-%' OR domain LIKE '%.scopetest-%')"
go test ./db -count=1
git diff --check
```

Expected: both focused repetitions pass; the query prints `0:0:0`; the complete `db` package passes.

- [ ] **Step 5: Stop and remove the isolated fixture**

Run:

```bash
docker stop "$ARTEX_FIXTURE_PG"
docker container rm "$ARTEX_FIXTURE_PG"
unset ARTEX_FIXTURE_PG ARTEX_PG_DSN
```

Expected: `docker ps -a --filter name=v-artex-fixture-pg --format '{{.Names}}'` prints nothing for the exact test container.

- [ ] **Step 6: Commit the fixture fix**

```bash
git add db/company_scope_test.go
GIT_AUTHOR_NAME=hypnguyen1209 \
GIT_AUTHOR_EMAIL=hypnguyen1209@gmail.com \
GIT_COMMITTER_NAME=hypnguyen1209 \
GIT_COMMITTER_EMAIL=hypnguyen1209@gmail.com \
git commit -m "test(db): isolate company scope attribution"
```

### Task 2: Remove npm advisories without breaking versions

**Files:**
- Modify: `web/package.json`
- Modify: `web/package-lock.json`
- Modify: `web/src/app/globals.css`
- Create: `web/src/styles/shadcn-tailwind.css`
- Create: `web/public/shadcn-LICENSE.txt`

- [ ] **Step 1: Record the RED audit baseline**

Run:

```bash
cd web
set +e
npm audit --json > /tmp/v-artex-audit-before.json
audit_code=$?
set -e
node -e 'const r=require("/tmp/v-artex-audit-before.json"); console.log(r.metadata.vulnerabilities)'
test "$audit_code" -ne 0
cd ..
```

Expected at planning time: a non-zero exit and 22 total advisories, including critical advisories in Next.js and the shadcn CLI tree. The exact feed count may change; preserve the JSON evidence and require a non-zero baseline rather than hard-coding 22.

- [ ] **Step 2: Refresh only versions allowed by current semver ranges**

Run:

```bash
cd web
npm audit fix --package-lock-only
git diff --exit-code -- package.json
npm ci
cd ..
```

Expected: `web/package-lock.json` changes and the audit falls from 22 findings to the three low findings in `shadcn`, `@shadcn/registry`, and `deepmerge`. If npm proposes a major downgrade, an override, or requires `--force`, do not apply it.

- [ ] **Step 3: Replace the vulnerable CLI with its required static CSS**

The application imports `shadcn/tailwind.css` but never executes or imports the `shadcn` CLI JavaScript. Make these exact changes:

1. Remove `"shadcn": "^4.12.0"` from `web/package.json`; retain `"@shadcn/react": "^0.1.0"`.
2. Copy the exact contents of `node_modules/shadcn/dist/tailwind.css` from installed `shadcn@4.23.0` to `web/src/styles/shadcn-tailwind.css` using `apply_patch`.
3. Prepend this license/source header to the vendored file:

```css
/*
 * Vendored from shadcn@4.23.0/dist/tailwind.css.
 * MIT License — Copyright (c) 2023 shadcn.
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 * The above copyright notice and this permission notice shall be included in
 * all copies or substantial portions of the Software.
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
 * OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
 * SOFTWARE.
 */
```

4. Add `web/public/shadcn-LICENSE.txt` containing the complete plain-text MIT License from `node_modules/shadcn/LICENSE.md`. This file is required because PostCSS strips the source comment from compiled CSS.
5. Change `@import "shadcn/tailwind.css";` in `web/src/app/globals.css` to `@import "../styles/shadcn-tailwind.css";`.
6. Run `npm install --package-lock-only` to remove the CLI dependency tree from the lockfile, followed by `npm ci`.

Verify the vendored body before the license header is byte-for-byte identical to the installed 4.23.0 stylesheet, and verify no source or script imports the CLI:

```bash
rg -n --glob '!node_modules/**' 'from ["'"']shadcn["'"']|require\(["'"']shadcn["'"']\)|npm exec shadcn|npx shadcn' .
```

Expected: no executable CLI references. `components.json` and descriptive comments may still mention shadcn.

- [ ] **Step 4: Require a clean audit**

Run:

```bash
cd web
npm audit --json > /tmp/v-artex-audit-after.json
node -e 'const r=require("/tmp/v-artex-audit-after.json"); if (r.metadata.vulnerabilities.total !== 0) { console.error(r.metadata.vulnerabilities); process.exit(1) }'
cd ..
```

Expected: npm exits zero and the total is `0`.

- [ ] **Step 5: Verify the complete frontend contract**

Run:

```bash
cd web
npm test
npm run check
NEXT_PUBLIC_MOCK=1 npm run build:static
docker run --rm --init --ipc=host --network host \
  --user "$(id -u):$(id -g)" \
  -e HOME=/tmp/pw-home -e XDG_CACHE_HOME=/tmp/pw-cache \
  -v "$PWD:/work" -w /work \
  mcr.microsoft.com/playwright:v1.64.1-noble \
  bash -lc 'mkdir -p "$HOME" "$XDG_CACHE_HOME" && npm run test:e2e'
cd ..
```

Expected: 64 unit tests pass, Biome exits zero, 31 static pages build, and both desktop/mobile Playwright tests pass.

After the static build, verify the distributed notice:

```bash
cmp web/public/shadcn-LICENSE.txt web/out/shadcn-LICENSE.txt
rg -n 'Copyright \(c\) 2023 shadcn|Permission is hereby granted|THE SOFTWARE IS PROVIDED "AS IS"' web/out/shadcn-LICENSE.txt
```

Expected: the exported notice is byte-for-byte identical and contains all three license clauses.

- [ ] **Step 6: Clean generated frontend artifacts**

Run from the repository root:

```bash
for generated in web/.next web/out web/test-results web/playwright-report; do
  if [ -e "$generated" ]; then gio trash "$generated"; fi
done
git status --short
```

Expected: only `web/package.json`, `web/package-lock.json`, `web/src/app/globals.css`, `web/src/styles/shadcn-tailwind.css`, and `web/public/shadcn-LICENSE.txt` remain changed for this task.

- [ ] **Step 7: Commit the dependency remediation**

```bash
git add web/package.json web/package-lock.json web/src/app/globals.css web/src/styles/shadcn-tailwind.css web/public/shadcn-LICENSE.txt
GIT_AUTHOR_NAME=hypnguyen1209 \
GIT_AUTHOR_EMAIL=hypnguyen1209@gmail.com \
GIT_COMMITTER_NAME=hypnguyen1209 \
GIT_COMMITTER_EMAIL=hypnguyen1209@gmail.com \
git commit -m "fix(web): update vulnerable dependencies"
```

### Task 3: Add permanent native Windows and macOS verification

**Files:**
- Modify: `.github/workflows/ci.yml`

- [ ] **Step 1: Prove the native matrix is absent**

Run:

```bash
node <<'NODE'
const fs = require('fs');
const workflow = fs.readFileSync('.github/workflows/ci.yml', 'utf8');
if (!workflow.includes('windows-2025') || !workflow.includes('macos-15')) {
  throw new Error('RED: native Windows/macOS CI coverage is absent');
}
NODE
```

Expected before the change: the assertion fails with `RED: native Windows/macOS CI coverage is absent`.

- [ ] **Step 2: Add the focused native job**

Add this job to `.github/workflows/ci.yml`, reusing the already pinned action revisions:

```yaml
  native:
    name: native (${{ matrix.os }})
    runs-on: ${{ matrix.os }}
    timeout-minutes: 20
    strategy:
      fail-fast: false
      matrix:
        os: [windows-2025, macos-15]
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version-file: go.mod
          cache: true
      - name: Test native doctor paths
        run: go test -count=1 ./cmd/artex
      - name: Test native run gate
        run: go test -count=1 ./server -run '^(TestAgentRunGate|TestAgentRunRelease)'
      - name: Test native intent admission
        run: go test -count=1 ./agent -run '^(TestIntentBudget|TestAddIntent|TestPlannerInstallsFreshIntentBudget)'
      - name: Build native binary
        run: go build ./cmd/artex
```

Do not add PostgreSQL installation logic or duplicate the complete Linux suite.

- [ ] **Step 3: Verify workflow syntax and immutable actions**

Run:

```bash
docker run --rm -v "$PWD:/repo" -w /repo rhysd/actionlint:1.7.7
node <<'NODE'
const fs = require('fs');
for (const file of fs.readdirSync('.github/workflows').filter((name) => /\.ya?ml$/.test(name))) {
  fs.readFileSync(`.github/workflows/${file}`, 'utf8').split(/\r?\n/).forEach((line, index) => {
    const match = line.match(/^\s*-?\s*uses:\s*([^\s#]+)/);
    if (!match || match[1].startsWith('./') || match[1].startsWith('docker://')) return;
    const revision = match[1].slice(match[1].lastIndexOf('@') + 1);
    if (!/^[0-9a-f]{40}$/.test(revision)) throw new Error(`${file}:${index + 1}: mutable action ${match[1]}`);
  });
}
NODE
git diff --check
```

Expected: actionlint and the SHA scanner exit zero.

- [ ] **Step 4: Run the focused commands locally**

```bash
go test -count=1 ./cmd/artex
go test -count=1 ./server -run '^(TestAgentRunGate|TestAgentRunRelease)'
go test -count=1 ./agent -run '^(TestIntentBudget|TestAddIntent|TestPlannerInstallsFreshIntentBudget)'
go build ./cmd/artex
gio trash artex 2>/dev/null || true
```

Expected: every test/build command exits zero; the generated local binary is removed.

- [ ] **Step 5: Commit the native job**

```bash
git add .github/workflows/ci.yml
GIT_AUTHOR_NAME=hypnguyen1209 \
GIT_AUTHOR_EMAIL=hypnguyen1209@gmail.com \
GIT_COMMITTER_NAME=hypnguyen1209 \
GIT_COMMITTER_EMAIL=hypnguyen1209@gmail.com \
git commit -m "ci: test native Windows and macOS paths"
```

### Task 4: Run complete local acceptance

**Files:**
- Verify only; no tracked changes expected

- [ ] **Step 1: Run full Go tests on a fresh PostgreSQL 17 database**

Start a uniquely named PostgreSQL 17 container on `127.0.0.1:55439`, wait for health, and set `ARTEX_PG_DSN` as in Task 1. Then run:

```bash
go test -count=1 -p 1 ./...
```

Expected: all 17 packages pass.

- [ ] **Step 2: Recreate the database and run the Go 1.26 race suite**

Drop and recreate only the temporary acceptance database, then run:

```bash
docker run --rm --network host \
  -e ARTEX_PG_DSN="$ARTEX_PG_DSN" \
  -v "$PWD:/src" -w /src \
  -v v-artex-go-mod-cache:/go/pkg/mod \
  -v v-artex-go-build-cache:/root/.cache/go-build \
  golang:1.26-bookworm \
  go test -race -count=1 -p 1 ./...
```

Expected: all 17 packages pass and no race report appears.

- [ ] **Step 3: Remove the temporary PostgreSQL container**

Stop and remove the exact uniquely named container. Confirm `docker ps -a --filter name=<exact-name> --format '{{.Names}}'` is empty.

- [ ] **Step 4: Re-run frontend, audit, workflow, and repository checks**

Run the Task 2 frontend/audit commands and Task 3 workflow checks again. Then run:

```bash
git diff --check 83ad93038eaa589634fbcc0ddd546e6aecd21513..HEAD
git status --short
git log 83ad93038eaa589634fbcc0ddd546e6aecd21513..HEAD \
  --format='%H%x09%an%x09%ae%x09%cn%x09%ce' \
  | awk -F '\t' '$2 != "hypnguyen1209" || $3 != "hypnguyen1209@gmail.com" || $4 != "hypnguyen1209" || $5 != "hypnguyen1209@gmail.com" { print; bad=1 } END { exit bad }'
```

Expected: audit total zero, all tests/builds pass, no generated artifacts remain, worktree is clean, and every commit has the required identity.

### Task 5: Push, open the PR, and collect native evidence

**Files:**
- No additional tracked changes expected

- [ ] **Step 1: Push the feature branch**

```bash
git push -u origin feature/runtime-safety-foundation
```

Expected: the remote branch points to the locally accepted HEAD. Do not force-push.

- [ ] **Step 2: Create the pull request**

```bash
gh pr create \
  --base main \
  --head feature/runtime-safety-foundation \
  --title "Harden runtime safety and native verification" \
  --body "$(cat <<'EOF'
## Summary
- add bounded planner intent admission and global agent-run concurrency controls
- add read-only doctor diagnostics, shutdown hardening, and English-only compatibility migrations
- add reproducible Linux/race/browser gates plus native Windows/macOS verification
- update vulnerable frontend dependencies and isolate reusable PostgreSQL fixtures

## Test plan
- Go full suite on fresh PostgreSQL 17
- Go 1.26 race suite on a separate fresh database
- 64 frontend unit tests, Biome, 31-page static build, desktop/mobile Playwright
- npm audit with zero advisories
- actionlint and immutable action SHA scan
- GitHub-hosted windows-2025 and macos-15 native tests/builds
EOF
)"
```

Expected: GitHub returns the new PR URL.

- [ ] **Step 3: Wait for all checks and inspect native logs**

```bash
gh pr checks --watch
gh run list --branch feature/runtime-safety-foundation --limit 5
```

Open the successful CI run with `gh run view <run-id> --log` and confirm both `native (windows-2025)` and `native (macos-15)` executed the tests and build rather than being skipped.

Expected: every required check succeeds. A native failure is a real blocker: use systematic debugging, add a failing regression test where practical, make the smallest fix, repeat local acceptance, push normally, and wait for the replacement run.

- [ ] **Step 4: Hand off without merge or deployment**

Report the PR URL, final HEAD, Windows/macOS job results, audit count, local/race/frontend results, and the exact remaining limitations. Keep the feature worktree and branch for PR feedback.
