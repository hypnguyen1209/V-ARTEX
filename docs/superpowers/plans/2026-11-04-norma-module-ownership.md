# Norma Module Ownership Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish the pinned Norma v0.4.3 source under `github.com/hypnguyen1209/norma` and migrate V-ARTEX away from the Autumn-27 module path without upgrading Norma behavior.

**Architecture:** Create a dedicated branch and tag in the `hypnguyen1209/Norma` fork from upstream tag `v0.4.3`, changing only the module path, self-imports, and README module reference. Because upstream added Apache-2.0 immediately after `v0.4.3`, copy its exact LICENSE from commit `52f17dd` so the fork preserves attribution without changing runtime code. Then update every exact Norma module reference in V-ARTEX, let Go regenerate checksums, and verify normal, race, and native cross-build paths.

**Tech Stack:** Go 1.26, Git/GitHub CLI, Go modules, Git worktrees.

---

### Task 1: Publish the owned Norma module

**Files:**
- Modify in `hypnguyen1209/Norma`: `go.mod`
- Modify in `hypnguyen1209/Norma`: all tracked files containing `github.com/Autumn-27/norma`
- Create from upstream commit `52f17dd`: `LICENSE`

- [ ] **Step 1: Clone the fork and branch from the exact pinned source**

```bash
gh repo clone hypnguyen1209/Norma "$NORMA_DIR"
git -C "$NORMA_DIR" fetch upstream refs/tags/v0.4.3
git -C "$NORMA_DIR" switch -c v-artex/module-v0.4.3 FETCH_HEAD
test "$(git -C "$NORMA_DIR" rev-parse HEAD)" = 320ae32989da97dd91469bb89164cbfe87d83d5d
```

- [ ] **Step 2: Verify RED ownership assertions**

```bash
test "$(sed -n 's/^module //p' "$NORMA_DIR/go.mod")" = github.com/hypnguyen1209/norma
! git -C "$NORMA_DIR" grep -F github.com/Autumn-27/norma
test -f "$NORMA_DIR/LICENSE"
```

Expected: all assertions fail because v0.4.3 still declares and imports the upstream module and predates the license-file commit.

- [ ] **Step 3: Rename the module and every self-import**

```bash
cd "$NORMA_DIR"
git checkout 52f17dd52d95b087265e5d5b306fe85c1ea05599 -- LICENSE
git grep -IlF github.com/Autumn-27/norma | \
  xargs sed -i 's#github.com/Autumn-27/norma#github.com/hypnguyen1209/norma#g'
go mod tidy
```

Keep the copied `LICENSE`, including `Copyright 2026 Autumn-27`, byte-for-byte identical to upstream.

- [ ] **Step 4: Verify the fork**

```bash
test "$(go list -m)" = github.com/hypnguyen1209/norma
! git grep -F github.com/Autumn-27/norma
test "$(git hash-object LICENSE)" = "$(git show 52f17dd52d95b087265e5d5b306fe85c1ea05599:LICENSE | git hash-object --stdin)"
go mod verify
set +e
go test -count=1 ./... > /tmp/norma-migrated-test.log 2>&1
migrated_code=$?
set -e
test "$migrated_code" -ne 0
sed -n 's/^[[:space:]]*--- FAIL: \([^ ]*\).*/\1/p' /tmp/norma-v043-baseline-test.log | sort -u > /tmp/norma-baseline-failures
sed -n 's/^[[:space:]]*--- FAIL: \([^ ]*\).*/\1/p' /tmp/norma-migrated-test.log | sort -u > /tmp/norma-migrated-failures
diff -u /tmp/norma-baseline-failures /tmp/norma-migrated-failures
mapfile -t passing_packages < <(go list ./... | grep -Ev '/(llm|noa|noaadapter)$')
go test -count=1 "${passing_packages[@]}"
go test -race -count=1 "${passing_packages[@]}"
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./...
git diff --check
```

Expected: ownership, checksum, all unaffected-package normal/race tests, and both cross-builds pass. The complete suite must contain exactly the same eight pre-existing failures as the clean upstream `v0.4.3` checkout: one `llm` retry-test panic, two `noa` pacing tests, and five `noaadapter` tests. No additional failure is accepted.

- [ ] **Step 5: Commit, push the branch, and publish the module tag**

```bash
git add LICENSE README.md go.mod '**/*.go'
git commit -m "chore: publish V-ARTEX Norma module"
git push -u origin v-artex/module-v0.4.3
git tag -a v0.4.3-v-artex.1 -m "Norma v0.4.3 for V-ARTEX"
git push origin v0.4.3-v-artex.1
```

Verify the tag resolves to the committed module:

```bash
git ls-remote origin refs/tags/v0.4.3-v-artex.1
GOPROXY=direct go list -m -json github.com/hypnguyen1209/norma@v0.4.3-v-artex.1
```

### Task 2: Migrate V-ARTEX to the owned Norma module

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: every tracked file containing `github.com/Autumn-27/norma`
- Modify/Test: `cmd/artex/distribution_test.go`

- [ ] **Step 1: Add the failing ownership regression**

Add `TestDistributionUsesOwnedNormaModule` to `cmd/artex/distribution_test.go`. It must read `go.mod` and `README.md`, reject `github.com/Autumn-27/norma`, and require `github.com/hypnguyen1209/norma`.

- [ ] **Step 2: Run the test and verify RED**

```bash
go test ./cmd/artex -run TestDistributionUsesOwnedNormaModule -count=1
```

Expected: FAIL because both files still reference the upstream module path.

- [ ] **Step 3: Replace module references and regenerate checksums**

```bash
git grep -IlF github.com/Autumn-27/norma -- . \
  ':!go.sum' \
  ':!cmd/artex/distribution_test.go' \
  ':!docs/superpowers/plans/2026-11-04-norma-module-ownership.md' | \
  xargs sed -i 's#github.com/Autumn-27/norma#github.com/hypnguyen1209/norma#g'
go mod edit -require=github.com/hypnguyen1209/norma@v0.4.3-v-artex.1
GOPROXY=direct go mod tidy
```

Do not edit `go.sum` manually.

- [ ] **Step 4: Verify GREEN and repository ownership**

```bash
go test ./cmd/artex -run TestDistributionUsesOwnedNormaModule -count=1
! git grep -F github.com/Autumn-27/norma -- . \
  ':!cmd/artex/distribution_test.go' \
  ':!docs/superpowers/plans/2026-11-04-norma-module-ownership.md'
go list -m github.com/hypnguyen1209/norma
go mod verify
git diff --check
```

Expected: the test passes, the old module path is absent, and the owned module resolves at `v0.4.3-v-artex.1`.

- [ ] **Step 5: Run complete backend and native verification**

```bash
ARTEX_PG_DSN='postgres://postgres:postgres@127.0.0.1:55432/artex_test?sslmode=disable' go test -count=1 -p 1 ./...
ARTEX_PG_DSN='postgres://postgres:postgres@127.0.0.1:55432/artex_test?sslmode=disable' go test -race -count=1 -p 1 ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./...
```

Expected: both suites and both cross-builds pass.

- [ ] **Step 6: Commit the V-ARTEX migration**

```bash
git add go.mod go.sum README.md cmd/artex/distribution_test.go agent db docs guard llmpool llmrec mcphttp server sidequestion traffic
git commit -m "chore: use the owned Norma module"
```

### Task 3: Final review and handoff

**Files:**
- Verify all changed files; no new production behavior.

- [ ] **Step 1: Inspect both immutable diffs**

Confirm the Norma commit is based on upstream `v0.4.3`, copies the upstream Apache-2.0 license exactly, changes no runtime logic beyond self-import paths, and the V-ARTEX diff contains only path/checksum/test/documentation updates.

- [ ] **Step 2: Re-run ownership assertions and clean-state checks**

```bash
! git grep -F github.com/Autumn-27/norma -- . \
  ':!cmd/artex/distribution_test.go' \
  ':!docs/superpowers/plans/2026-11-04-norma-module-ownership.md'
git status --short
git log -1 --format='%an <%ae>|%cn <%ce>|%s'
```

Expected: no upstream module path, a clean worktree, and `hypnguyen1209 <hypnguyen1209@gmail.com>` as author and committer.

- [ ] **Step 3: Use `superpowers:finishing-a-development-branch`**

Offer merge/push/keep/discard choices for the V-ARTEX feature branch. The Norma branch and tag are already pushed because V-ARTEX must resolve them as a remote module.
