# Final Review Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the remaining CI supply-chain and `artex doctor` false-success cases found by final review while preserving the command's read-only and compatibility contracts.

**Architecture:** Pin the reusable CI workflow to the already-reviewed release action SHAs. Keep doctor checks in `cmd/artex/doctor.go`, strengthening each existing boundary with explicit error classification, fixed read-only schema probes, and expected-kind/access filesystem probes. Reuse the current injectable runtime and PostgreSQL immutability harness; do not add packages or persistence.

**Tech Stack:** GitHub Actions, actionlint, Go standard library, `database/sql`, pgx PostgreSQL driver, existing doctor test harness.

**Design:** `docs/superpowers/specs/2026-10-22-final-review-hardening-design.md`

---

## File Map

- Modify `.github/workflows/ci.yml`: pin all external actions by full commit SHA.
- Modify `cmd/artex/doctor.go`: classify bind failures, validate path kinds/access, and query required schema columns read-only.
- Modify `cmd/artex/doctor_test.go`: add focused regression tests and update runtime fakes for the stronger contracts.

### Task 1: Pin reusable CI actions

```json:metadata
{"modelTier": "mechanical"}
```

**Files:**
- Modify: `.github/workflows/ci.yml`

- [ ] **Step 1: Reproduce the mutable-action failure**

Run:

```bash
if rg -n 'uses:\s+[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+@v[0-9]' .github/workflows/ci.yml; then
  exit 1
fi
```

Expected: exit `1`, with all current `actions/checkout@v7`,
`actions/setup-go@v7`, `actions/setup-node@v7`, and
`actions/upload-artifact@v7` uses printed.

- [ ] **Step 2: Replace only the mutable tags**

Use the same reviewed revisions already present in `release.yml` at every
matching call site:

```yaml
- uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
- uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
- uses: actions/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1 # v7.1.0
- uses: actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 # v7.0.2
```

Do not change job commands, versions, permissions, timeouts, cache settings, or
the local reusable-workflow call.

- [ ] **Step 3: Verify immutable references and workflow syntax**

Run:

```bash
if rg -n 'uses:\s+[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+@v[0-9]' .github/workflows/ci.yml; then
  exit 1
fi
docker run --rm \
  -v "$PWD:/repo:ro" -w /repo rhysd/actionlint:latest \
  .github/workflows/ci.yml .github/workflows/release.yml
git diff --check
```

Expected: all commands exit `0`; actionlint prints no diagnostics.

- [ ] **Step 4: Commit the CI pins**

```bash
git add .github/workflows/ci.yml
GIT_AUTHOR_NAME='hypnguyen1209' GIT_AUTHOR_EMAIL='hypnguyen1209@gmail.com' \
GIT_COMMITTER_NAME='hypnguyen1209' GIT_COMMITTER_EMAIL='hypnguyen1209@gmail.com' \
git commit -m 'ci: pin verification actions'
```

### Task 2: Classify doctor address failures accurately

```json:metadata
{"modelTier": "mechanical"}
```

**Files:**
- Modify: `cmd/artex/doctor_test.go`
- Modify: `cmd/artex/doctor.go`

- [ ] **Step 1: Add failing address regression tests**

Keep `TestDoctorOccupiedPortIsWarning` unchanged. Add:

```go
func TestDoctorInvalidAddressIsFailure(t *testing.T) {
	result := checkAddress("network.http", "127.0.0.1:99999", net.Listen)
	if result.Status != doctorFail {
		t.Fatalf("invalid address = %+v, want failure", result)
	}
}

func TestDoctorUnavailableAddressIsFailureWithoutRawError(t *testing.T) {
	const raw = "fixture bind detail"
	result := checkAddress("network.http", "192.0.2.1:8787", func(string, string) (net.Listener, error) {
		return nil, errors.New(raw)
	})
	if result.Status != doctorFail {
		t.Fatalf("unavailable address = %+v, want failure", result)
	}
	if strings.Contains(result.Detail+result.Remediation, raw) {
		t.Fatalf("raw listen error leaked: %+v", result)
	}
}
```

- [ ] **Step 2: Run the tests and verify RED**

```bash
go test ./cmd/artex -run 'TestDoctor(OccupiedPort|InvalidAddress|UnavailableAddress)' -count=1
```

Expected: the occupied-port test passes; both new tests fail because every
listen error is currently `warn`.

- [ ] **Step 3: Implement the minimal classification**

Add `syscall` to the imports. Replace only the listen-error branch in
`checkAddress`:

```go
		listener, err := listen("tcp", address)
		if err != nil {
			if errors.Is(err, syscall.EADDRINUSE) {
				return doctorResult{
					Status: doctorWarn,
					Detail: address + " is already in use",
					Remediation: "It may be a running V-ARTEX instance; identify the listener or choose another port",
				}
			}
			return doctorResult{
				Status: doctorFail,
				Detail: address + " cannot be used",
				Remediation: "Correct the address and port, permissions, or local interface configuration",
			}
		}
```

Do not return or interpolate `err` into public output.

- [ ] **Step 4: Verify GREEN and the command contract**

```bash
go test ./cmd/artex -run 'TestDoctor(OccupiedPort|InvalidAddress|UnavailableAddress)|TestRunDoctorExitCodes|TestRunDoctorJSONHasStableShapeAndNoANSI' -count=1
git diff --check
```

Expected: PASS.

- [ ] **Step 5: Commit address diagnosis**

```bash
git add cmd/artex/doctor.go cmd/artex/doctor_test.go
GIT_AUTHOR_NAME='hypnguyen1209' GIT_AUTHOR_EMAIL='hypnguyen1209@gmail.com' \
GIT_COMMITTER_NAME='hypnguyen1209' GIT_COMMITTER_EMAIL='hypnguyen1209@gmail.com' \
git commit -m 'fix(cli): classify doctor bind failures'
```

### Task 3: Prove filesystem kind and read access

```json:metadata
{"modelTier": "standard"}
```

**Files:**
- Modify: `cmd/artex/doctor_test.go`
- Modify: `cmd/artex/doctor.go`

- [ ] **Step 1: Add failing path regression tests**

Add an expected-kind type and tests that describe the desired call contract:

```go
func TestDoctorPathRejectsWrongKind(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	openPath := func(path string) (*os.File, error) { return os.Open(path) }
	if result := checkPath("path.data", file, true, doctorPathDirectory, os.Stat, openPath); result.Status != doctorFail {
		t.Fatalf("file as directory = %+v, want failure", result)
	}
	if result := checkPath("path.jwt_key", dir, false, doctorPathRegularFile, os.Stat, openPath); result.Status != doctorFail {
		t.Fatalf("directory as key = %+v, want failure", result)
	}
}

func TestDoctorPathRejectsUnreadableObject(t *testing.T) {
	dir := t.TempDir()
	const raw = "fixture permission detail"
	result := checkPath("path.data", dir, true, doctorPathDirectory, os.Stat,
		func(string) (*os.File, error) { return nil, errors.New(raw) })
	if result.Status != doctorFail {
		t.Fatalf("unreadable directory = %+v, want failure", result)
	}
	if strings.Contains(result.Detail+result.Remediation, raw) {
		t.Fatalf("raw filesystem error leaked: %+v", result)
	}
}
```

- [ ] **Step 2: Run the tests and verify RED**

```bash
go test ./cmd/artex -run 'TestDoctorPathRejects' -count=1
```

Expected: build failure because the expected-kind/open contract does not exist.

- [ ] **Step 3: Add the smallest runtime boundary and implementation**

Add to `doctorRuntime`:

```go
	openPath func(string) (*os.File, error)
```

Set `openPath: os.Open` in `defaultDoctorDeps`. Add:

```go
type doctorPathKind uint8

const (
	doctorPathDirectory doctorPathKind = iota
	doctorPathRegularFile
)

func checkPath(name, path string, required bool, kind doctorPathKind,
	stat func(string) (os.FileInfo, error), openPath func(string) (*os.File, error),
) doctorResult {
	return timedDoctorResult(name, func() doctorResult {
		info, err := stat(path)
		if err != nil {
			status := doctorWarn
			if required {
				status = doctorFail
			}
			return doctorResult{Status: status, Detail: "Path unavailable: " + path, Remediation: "Check that the path exists and is accessible"}
		}
		if kind == doctorPathDirectory && !info.IsDir() {
			return doctorResult{Status: doctorFail, Detail: "Expected a directory: " + path, Remediation: "Replace the path with a readable directory"}
		}
		if kind == doctorPathRegularFile && !info.Mode().IsRegular() {
			return doctorResult{Status: doctorFail, Detail: "Expected a regular file: " + path, Remediation: "Replace the path with a readable regular file"}
		}
		if openPath == nil {
			openPath = os.Open
		}
		handle, err := openPath(path)
		if err != nil {
			return doctorResult{Status: doctorFail, Detail: "Path is not readable: " + path, Remediation: "Check path ownership and read permissions"}
		}
		defer handle.Close()
		if kind == doctorPathDirectory {
			if _, err := handle.Readdirnames(1); err != nil && !errors.Is(err, io.EOF) {
				return doctorResult{Status: doctorFail, Detail: "Directory is not readable: " + path, Remediation: "Check directory ownership and read permissions"}
			}
		}
		return doctorResult{Status: doctorOK, Detail: fmt.Sprintf("%s (mode %s, size %d bytes)", path, info.Mode(), info.Size())}
	})
}
```

Pass kinds at the four call sites:

```go
checkPath("path.data", opts.DataDir, true, doctorPathDirectory, runtime.stat, runtime.openPath)
checkPath("path.workspace", filepath.Join(opts.DataDir, "tasks"), false, doctorPathDirectory, runtime.stat, runtime.openPath)
checkPath("path.skills", doctorSkillDir(), true, doctorPathDirectory, runtime.stat, runtime.openPath)
checkPath("path.jwt_key", filepath.Join(jwtKeyDir(), "jwt.key"), false, doctorPathRegularFile, runtime.stat, runtime.openPath)
```

Add `io` only if it is not already imported. Do not read regular-file contents
and do not create a probe file.

- [ ] **Step 4: Verify GREEN and immutability**

```bash
go test ./cmd/artex -run 'TestDoctorPathRejects|TestDoctorFilesystemSnapshotDetectsPersistentChanges|TestDoctorDoesNotPersistDatabaseOrFilesystemState' -count=1
path_test_db="artex_hardening_paths_$(date +%s)_$$"
cleanup_path_test_db() {
  docker exec v-artex-baseline-pg-20261021 dropdb -U artex --if-exists --force "$path_test_db"
}
trap cleanup_path_test_db EXIT
docker exec v-artex-baseline-pg-20261021 createdb -U artex -T artex_test "$path_test_db"
path_test_dsn="postgres://artex:artex_test_password@127.0.0.1:55439/${path_test_db}?sslmode=disable"
ARTEX_PG_DSN="$path_test_dsn" go test ./cmd/artex \
  -run 'TestDoctorPathRejects|TestDoctorFilesystemSnapshotDetectsPersistentChanges|TestDoctorDoesNotPersistDatabaseOrFilesystemState' \
  -count=1
ARTEX_PG_DSN="$path_test_dsn" go test ./cmd/artex -count=1
git diff --check
cleanup_path_test_db
trap - EXIT
```

Expected: PASS. The first command exercises the unit cases immediately; the
fresh PostgreSQL clone ensures the read-only integration test cannot silently
skip. The cleanup trap removes the database even when a command fails.

- [ ] **Step 5: Commit path validation**

```bash
git add cmd/artex/doctor.go cmd/artex/doctor_test.go
GIT_AUTHOR_NAME='hypnguyen1209' GIT_AUTHOR_EMAIL='hypnguyen1209@gmail.com' \
GIT_COMMITTER_NAME='hypnguyen1209' GIT_COMMITTER_EMAIL='hypnguyen1209@gmail.com' \
git commit -m 'fix(cli): validate doctor path access'
```

### Task 4: Prove required PostgreSQL columns and read access

```json:metadata
{"modelTier": "standard"}
```

**Files:**
- Modify: `cmd/artex/doctor_test.go`
- Modify: `cmd/artex/doctor.go`

- [ ] **Step 1: Add failing schema-query tests**

Add a test around the existing `doctorQueryConnector`:

```go
func TestDoctorSchemaFailsWhenRequiredTableQueryFails(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
	}{
		{name: "missing required column", raw: "fixture undefined column"},
		{name: "missing select permission", raw: "fixture permission denied"},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := sql.OpenDB(doctorQueryConnector{query: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
				if strings.Contains(query, "FROM tasks LIMIT 0") {
					return nil, errors.New(test.raw)
				}
				if strings.Contains(query, "SHOW server_version") {
					return &doctorStaticRows{columns: []string{"server_version"}, values: []driver.Value{"17"}}, nil
				}
				return &doctorStaticRows{columns: []string{"probe"}}, nil
			}})
			defer database.Close()

			result := checkDoctorSchema(context.Background(), &doctorSession{sql: database})
			if result.Status != doctorFail || !strings.Contains(result.Detail, "tasks") {
				t.Fatalf("schema result = %+v, want tasks failure", result)
			}
			if strings.Contains(result.Detail+result.Remediation, test.raw) {
				t.Fatalf("raw database error leaked: %+v", result)
			}
		})
	}
}
```

Update `TestDefaultDoctorSkipsLiveLLMWhenProfileValidationFails` so its fake
returns an empty `doctorStaticRows` for each `LIMIT 0` schema probe instead of
matching `to_regclass`.

- [ ] **Step 2: Run the focused test and verify RED**

```bash
go test ./cmd/artex -run 'TestDoctorSchemaFailsWhenRequiredTableQueryFails' -count=1
```

Expected: FAIL because current code calls `to_regclass` and never executes the
injected `tasks` query failure.

- [ ] **Step 3: Replace relation lookup with fixed read-only queries**

Add this code-owned probe list next to `checkDoctorSchema`:

```go
var doctorSchemaProbes = []struct {
	table string
	query string
}{
	{"settings", `SELECT key,value,updated_at FROM settings LIMIT 0`},
	{"tasks", `SELECT id,name,category_id,description,goal,exploration_id,status,paused,queued,queued_at,queue_mode,llm_profile_id,active_llm_profile_id,llm_chain_revision,company_id,parent_ref,timeout_seconds,plan_heartbeat_seconds,coverage_enabled,pinned_at,first_run_at,deadline_at,archived_at,deleted_at,completed_at,created_at,updated_at FROM tasks LIMIT 0`},
	{"explorations", `SELECT id,description,goal,status,round_no,created_at,updated_at FROM explorations LIMIT 0`},
	{"exploration_nodes", `SELECT id,exploration_id,kind,payload,priority,state,origin,owner,blocked_reason,delete_reason,content_version,cold_since_round,created_at,updated_at,completed_at FROM exploration_nodes LIMIT 0`},
	{"llm_profiles", `SELECT id,name,format,base_url,proxy,model,api_key,api_key_hint,rate_per_second,rate_per_minute,context_window_k,reasoning_effort,thinking_type,is_default,priority,pool_exclude,streaming,max_tokens,max_tokens_field,session_header_key,retry_connect_attempts,retry_connect_interval_ms,retry_empty_attempts,retry_empty_interval_ms,retry_stream_attempts,retry_stream_interval_ms,created_at,updated_at FROM llm_profiles LIMIT 0`},
	{"agents", `SELECT id,key,name,description,role,builtin,enabled,llm_profile_id,current_prompt_id,max_turns,run_seconds,web_search,interactive_shell,wrapup_prompt,wrapup_max_turns,task_timeout_wrapup_prompt,task_timeout_wrapup_max_turns,trigger_run_mode,trigger_merge_mode,trigger_max_parallel,created_at,updated_at FROM agents LIMIT 0`},
}
```

Replace the `to_regclass` loop with:

```go
		for _, probe := range doctorSchemaProbes {
			rows, err := session.sql.QueryContext(probeCtx, probe.query)
			if err != nil {
				return doctorResult{Status: doctorFail, Detail: "Required table is missing, incompatible, or unreadable: " + probe.table, Remediation: "Run the normal V-ARTEX startup migration with an authorized database account and grant SELECT access"}
			}
			if err := rows.Close(); err != nil {
				return doctorResult{Status: doctorFail, Detail: "Required table is unreadable: " + probe.table, Remediation: "Check PostgreSQL read permissions and connectivity"}
			}
		}
```

Keep the existing shared three-second context and `SHOW server_version`. Do not
include raw SQL errors in results and do not call `db.Open`.

- [ ] **Step 4: Verify GREEN against unit and real PostgreSQL fixtures**

Create a fresh isolated test database and run the focused and package tests in
the same shell so the cleanup trap always drops it:

```bash
doctor_test_db="artex_hardening_doctor_$(date +%s)_$$"
cleanup_doctor_test_db() {
  docker exec v-artex-baseline-pg-20261021 dropdb -U artex --if-exists --force "$doctor_test_db"
}
trap cleanup_doctor_test_db EXIT
docker exec v-artex-baseline-pg-20261021 createdb -U artex -T artex_test "$doctor_test_db"
doctor_test_dsn="postgres://artex:artex_test_password@127.0.0.1:55439/${doctor_test_db}?sslmode=disable"
ARTEX_PG_DSN="$doctor_test_dsn" go test ./cmd/artex \
  -run 'TestDoctorSchema|TestDefaultDoctorSkipsLiveLLMWhenProfileValidationFails|TestDoctorDoesNotPersistDatabaseOrFilesystemState' \
  -count=1
ARTEX_PG_DSN="$doctor_test_dsn" go test ./cmd/artex -count=1
git diff --check
cleanup_doctor_test_db
trap - EXIT
```

Expected: PASS. `TestDoctorDoesNotPersistDatabaseOrFilesystemState` must continue proving that
tables, columns, indexes, constraints, sequences, rows, functions, triggers,
and filesystem inputs are byte-for-byte unchanged. Drop the fresh database.

- [ ] **Step 5: Commit schema validation**

```bash
git add cmd/artex/doctor.go cmd/artex/doctor_test.go
GIT_AUTHOR_NAME='hypnguyen1209' GIT_AUTHOR_EMAIL='hypnguyen1209@gmail.com' \
GIT_COMMITTER_NAME='hypnguyen1209' GIT_COMMITTER_EMAIL='hypnguyen1209@gmail.com' \
git commit -m 'fix(cli): verify doctor schema access'
```

### Task 5: Run final acceptance and integration review

```json:metadata
{"modelTier": "standard"}
```

**Files:**
- Verify only; modify only the owning file if a gate exposes a defect, using a new RED-GREEN cycle.

- [ ] **Step 1: Backend and race evidence on separate fresh databases**

```bash
normal_db="artex_hardening_normal_$(date +%s)_$$"
race_db="artex_hardening_race_$(date +%s)_$$"
cleanup_backend_dbs() {
  docker exec v-artex-baseline-pg-20261021 dropdb -U artex --if-exists --force "$normal_db"
  docker exec v-artex-baseline-pg-20261021 dropdb -U artex --if-exists --force "$race_db"
}
trap cleanup_backend_dbs EXIT
docker exec v-artex-baseline-pg-20261021 createdb -U artex -T artex_test "$normal_db"
docker exec v-artex-baseline-pg-20261021 createdb -U artex -T artex_test "$race_db"
normal_dsn="postgres://artex:artex_test_password@127.0.0.1:55439/${normal_db}?sslmode=disable"
race_dsn="postgres://artex:artex_test_password@127.0.0.1:55439/${race_db}?sslmode=disable"
ARTEX_PG_DSN="$normal_dsn" go test -count=1 -p 1 ./...
docker run --rm --network host \
  -e ARTEX_PG_DSN="$race_dsn" \
  -v "$PWD:/src" -w /src \
  -v v-artex-go-mod-cache:/go/pkg/mod \
  -v v-artex-go-build-cache:/root/.cache/go-build \
  golang:1.26-bookworm go test -race -count=1 -p 1 ./...
cleanup_backend_dbs
trap - EXIT
```

Expected: both PASS. Drop and confirm absence of both databases.

- [ ] **Step 2: Doctor JSON evidence**

With a third fresh database cloned from the migrated test baseline, capture the
documented exit code separately from JSON parsing and always clean up:

```bash
doctor_db="artex_hardening_smoke_$(date +%s)_$$"
cleanup_doctor_db() {
  docker exec v-artex-baseline-pg-20261021 dropdb -U artex --if-exists --force "$doctor_db"
}
trap cleanup_doctor_db EXIT
docker exec v-artex-baseline-pg-20261021 createdb -U artex -T artex_test "$doctor_db"
doctor_dsn="postgres://artex:artex_test_password@127.0.0.1:55439/${doctor_db}?sslmode=disable"
doctor_code=0
ARTEX_PG_DSN="$doctor_dsn" \
  go run ./cmd/artex doctor --json --addr 127.0.0.1:0 --proxy '' \
  > /tmp/v-artex-doctor-final.json || doctor_code=$?
test "$doctor_code" -eq 0 || test "$doctor_code" -eq 1
python3 -m json.tool /tmp/v-artex-doctor-final.json >/dev/null
DOCTOR_DSN="$doctor_dsn" python3 - <<'PY'
import json
import os
from pathlib import Path

raw = Path("/tmp/v-artex-doctor-final.json").read_bytes()
report = json.loads(raw)
checks = {check["name"]: check["status"] for check in report["checks"]}
for name in ("postgres.connectivity", "postgres.schema", "llm.profiles"):
    assert checks[name] == "ok", (name, checks[name])
assert b"\x1b[" not in raw
assert b"artex_test_password" not in raw
assert os.environ["DOCTOR_DSN"].encode() not in raw
PY
cleanup_doctor_db
trap - EXIT
```

Accept only exit `0` or documented exit `1` caused by intentionally absent
required local paths. Require `postgres.connectivity`, `postgres.schema`, and
`llm.profiles` to be `ok`; scan output for ANSI sequences, the DSN, and its
password. Drop the database.

- [ ] **Step 3: Frontend and browser evidence**

```bash
cd web
npm ci
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

Expected: unit tests, full check, 31-page static build, and desktop/mobile E2E
all PASS. Remove only generated `.next`, `test-results`, and
`playwright-report`; verify no root-owned artifacts or listener on port 4173.

- [ ] **Step 4: Workflow, metadata, and cleanliness evidence**

```bash
docker run --rm -v "$PWD:/repo:ro" -w /repo rhysd/actionlint:latest \
  .github/workflows/ci.yml .github/workflows/release.yml
if rg -n 'uses:\s+[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+@v[0-9]' .github/workflows/ci.yml; then
  exit 1
fi
git diff --check
git status --short
git log 83ad930..HEAD --format='%H%x09%an <%ae>%x09%cn <%ce>'
```

Expected: workflow checks pass; status is clean; every commit in the range has
author and committer `hypnguyen1209 <hypnguyen1209@gmail.com>`.

- [ ] **Step 5: Request final whole-range review**

Review `83ad930..HEAD` for correctness, deadlocks/races, doctor read-only and
false-success behavior, workflow supply-chain integrity, migration safety,
frontend regressions, and test quality. Fix every Critical/Important finding
through a new TDD cycle and rerun affected gates before integration.
