# English Translation Integration Verification Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove the complete repository is English-only, behaviorally intact, buildable, and visually refreshed after the subsystem plans are complete.

**Architecture:** Treat the repository-wide Han scan as a hard acceptance gate, then independently verify filenames, Go behavior, frontend checks/build, scripts, database migration behavior, image inventory, and contract-preserving diffs.

**Tech Stack:** Git, Go, npm, Next.js, Biome, Bash, Docker Compose, image inspection.

---

### Task 1: Audit the Translation Surface

**Files:**
- Verify: every Git-tracked file.

- [ ] **Step 1: Require zero Han in tracked text**

Run:

```bash
! git grep -I -P -n '[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}]'
```

Expected: no output and exit 0 after inversion.

- [ ] **Step 2: Require zero Han in tracked filenames**

Run:

```bash
python3 - <<'PY'
import re
import subprocess
import sys

filenames = subprocess.check_output(["git", "ls-files", "-z"]).decode("utf-8").split("\0")
matches = [name for name in filenames if re.search(r"[\u3400-\u4DBF\u4E00-\u9FFF\uF900-\uFAFF]", name)]
for name in matches:
    print(name)
sys.exit(bool(matches))
PY
```

Expected: no output and exit 0.

- [ ] **Step 3: Inspect escaped legacy text usage**

Run:

```bash
rg -n '\\u[0-9a-fA-F]{4}' db/english_migration.go db/english_migration_test.go
```

Expected: only documented exact-match fixtures for the upgrade migration. No application display text should be encoded merely to evade the Han scan.

### Task 2: Run Full Automated Verification

**Files:**
- Test: all Go packages.
- Test: all frontend source.
- Test: operational scripts and configuration.

- [ ] **Step 1: Verify Go**

Run:

```bash
git ls-files -z '*.go' | xargs -0 gofmt -w
go test ./... -count=1
```

Expected: all packages PASS; only existing environment-gated database tests may SKIP explicitly.

- [ ] **Step 2: Verify frontend**

Run:

```bash
cd web
npm ci
npm run check
NEXT_PUBLIC_MOCK=1 npm run build
```

Expected: dependency install, Biome, type checking, and the production build all exit 0.

- [ ] **Step 3: Verify scripts and configuration**

Run from repository root:

```bash
bash -n build.sh dev.sh install.sh reset-password.sh start.sh update.sh
python3 -m py_compile skills/api-recon/scripts/harvest_static.py \
  skills/api-recon/scripts/spider_mpa.py
node --check skills/api-recon/scripts/preload.js
node --check skills/api-recon/scripts/runtime_harvest.js
python3 -m json.tool config.example.json >/dev/null
python3 -m json.tool sidequestion/validation-2026-09-10.json >/dev/null
```

Expected: every parser exits 0.

### Task 3: Verify Upgrade Semantics

**Files:**
- Test: `db/english_migration_test.go`
- Verify: `db/english_migration.go`, `db/db.go`

- [ ] **Step 1: Run migration tests with PostgreSQL**

Configure a disposable PostgreSQL DSN through the existing test configuration and run:

```bash
go test ./db -run 'TestMigrateBuiltinEnglishText|TestOpenSeed' -count=1 -v
```

Expected: PASS without skips in this dedicated verification environment.

- [ ] **Step 2: Confirm no user tables are migration targets**

Review the SQL and query loop in `db/english_migration.go`. It may target built-in agents, prompt metadata/default bodies, built-in interception rules, built-in asset rules, and the exact saved judge default. It must not issue updates against tasks, conversations, findings, evidence, reports, notification history, or arbitrary user content.

### Task 4: Verify Images and Documentation

**Files:**
- Verify: all files under `screenshots/`.
- Verify: `README.md`.

- [ ] **Step 1: Confirm the image set**

Run:

```bash
test "$(find screenshots -maxdepth 1 -type f -name '*.png' | wc -l)" -eq 14
file screenshots/*.png
```

Expected: exactly 14 valid PNGs; 13 are application captures and `wx.png` is the translated banner.

- [ ] **Step 2: Inspect at original resolution**

Review each PNG for remaining Chinese display text, clipping, stale loading states, accidental dialogs, credentials, and inconsistent layout. Confirm `wx.png` says `Search on WeChat`, still says `SecSentry`, and retains a decodable QR code.

- [ ] **Step 3: Verify README references**

Run:

```bash
rg -o 'screenshots/[A-Za-z0-9_.-]+\.png' README.md | while read -r image; do test -f "$image" || exit 1; done
```

Expected: every image reference resolves.

### Task 5: Review Contracts and Produce Final Evidence

**Files:**
- Review: the full change range from the design commit through HEAD.

- [ ] **Step 1: Check patch hygiene**

Run:

```bash
git diff --check 2a9c4ac..HEAD
git status --short
git diff --stat 2a9c4ac..HEAD
```

Expected: no whitespace errors; only intentional translation, migration, documentation, and image changes are present.

- [ ] **Step 2: Review machine-facing contract tokens**

Inspect diffs involving routes, JSON tags/keys, SQL identifiers, enum values, tool names, environment variables, shell flags, protocol tokens, placeholders, and regex patterns. Revert accidental translations of machine-facing values while keeping their human descriptions English.

- [ ] **Step 3: Run the final acceptance command set fresh**

Run all of the following in the same verification pass:

```bash
! git grep -I -P -n '[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}]'
go test ./... -count=1
(cd web && npm run check && NEXT_PUBLIC_MOCK=1 npm run build)
git diff --check 2a9c4ac..HEAD
```

Expected: zero Han output, all tests/checks/builds exit 0, and no whitespace errors.

- [ ] **Step 4: Commit any verification-only corrections**

If verification exposed residual translation or formatting defects, fix only those defects and commit:

```bash
git add -A
git commit -m "fix: complete English translation verification"
```

Do not create an empty commit when no corrections were needed.
