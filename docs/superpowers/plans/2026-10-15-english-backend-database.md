# Backend and Database English Translation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Convert all backend, database, agent, prompt, notification, and Go test text to English while safely migrating unchanged built-in database defaults.

**Architecture:** Translate source contextually by package and keep all machine contracts unchanged. Add one idempotent startup migration that recognizes legacy built-in values by stable identity plus exact value or frozen SHA-256, updates only those values, and leaves customized or user-authored content untouched.

**Tech Stack:** Go 1.26+, PostgreSQL, embedded SQL, standard `testing`, `gofmt`, `go test`, Git PCRE search.

---

## File Map

- Create `db/english_migration.go`: exact-match migration for database-owned seed text plus a hash-guarded API that accepts translated prompt defaults without importing `agent`.
- Create `db/english_migration_test.go`: upgrade, customization-preservation, and idempotency tests.
- Create `server/english_migration.go`: assemble current English defaults from `agent` and `intercept` and pass them to `db` during manager startup.
- Modify every Han-matching tracked file under `agent/`, `db/`, `server/`, `notify/`, `intercept/`, `traffic/`, `selfupdate/`, `report/`, `sidequestion/`, `llmpool/`, `llmrec/`, `mcphttp/`, `evidence/`, `guard/`, `enrich/`, `cmd/`, and `config/`, as frozen by Task 1.
- Modify related tests in the same packages whenever they assert translated text.

### Task 1: Freeze the Backend Inventory and Baseline

**Files:**
- Read: all tracked files in the package list above.
- Test: all Go packages with `go test ./...`.

- [ ] **Step 1: Record the exact translation inventory**

Run:

```bash
git grep -I -P -l '[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}]' -- \
  agent db server notify intercept traffic selfupdate report sidequestion \
  llmpool llmrec mcphttp evidence guard enrich cmd config \
  > /tmp/artex-backend-han-files.txt
wc -l /tmp/artex-backend-han-files.txt
```

Expected: `233` files before implementation. If the count differs because the tree changed, inspect the list and use that exact list as the scope; do not silently exclude new files.

- [ ] **Step 2: Save the failing zero-Han baseline**

Run:

```bash
git grep -I -P -n '[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}]' -- \
  agent db server notify intercept traffic selfupdate report sidequestion \
  llmpool llmrec mcphttp evidence guard enrich cmd config \
  > /tmp/artex-backend-han-before.txt
test -s /tmp/artex-backend-han-before.txt
```

Expected: exit 0 because the pre-translation file is non-empty. This is the RED state for the text conversion.

- [ ] **Step 3: Run the backend test baseline**

Run: `go test ./...`

Expected: exit 0, allowing PostgreSQL-dependent tests to report skips when no test DSN is configured. If it fails, save the complete output before editing and prove later whether each failure predates the translation.

### Task 2: Add Failing Upgrade-Migration Tests

**Files:**
- Create: `db/english_migration_test.go`
- Test: `db/english_migration_test.go`

- [ ] **Step 1: Write the migration contract test**

Add tests that open `testDSN(t)`, begin a transaction, install controlled legacy fixtures, call `migrateBuiltinEnglishText(tx)`, and roll back. Use escaped Unicode literals such as:

```go
const legacyGovernmentNote = "[\u5185\u7f6e] \u653f\u5e9c\u7f51\u7ad9 (.gov)"

func TestMigrateBuiltinEnglishTextUpdatesOnlyLegacyDefaults(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	tx, err := d.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	var legacyID, customID int64
	err = tx.QueryRow(`INSERT INTO asset_intercept_rules(enabled,kind,pattern,note,builtin)
		VALUES(true,'fuzzy_domain','.gov',$1,true) RETURNING id`, legacyGovernmentNote).Scan(&legacyID)
	if err != nil {
		t.Fatal(err)
	}
	err = tx.QueryRow(`INSERT INTO asset_intercept_rules(enabled,kind,pattern,note,builtin)
		VALUES(true,'fuzzy_domain','.gov','Operator custom note',true) RETURNING id`,).Scan(&customID)
	if err != nil {
		t.Fatal(err)
	}

	if err := migrateBuiltinEnglishText(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := migrateBuiltinEnglishText(t.Context(), tx); err != nil {
		t.Fatalf("second migration: %v", err)
	}

	var gotLegacy, gotCustom string
	if err := tx.QueryRow(`SELECT note FROM asset_intercept_rules WHERE id=$1`, legacyID).Scan(&gotLegacy); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(`SELECT note FROM asset_intercept_rules WHERE id=$1`, customID).Scan(&gotCustom); err != nil {
		t.Fatal(err)
	}
	if gotLegacy != "[Built-in] Government website (.gov)" {
		t.Fatalf("legacy note = %q", gotLegacy)
	}
	if gotCustom != "Operator custom note" {
		t.Fatalf("custom note changed to %q", gotCustom)
	}
}
```

Add table-driven cases for built-in intercept rule names/messages, built-in agent labels and prompt-variable metadata, prompt notes, the retest/report agents, saved wrap-up defaults, and the LLM judge setting. Pass controlled hash-to-English replacement maps into the prompt migration API and assert that customized values remain byte-for-byte unchanged.

- [ ] **Step 2: Lock the legacy prompt fingerprints into the test**

Use these SHA-256 values from the approved pre-translation tree:

```text
builtin:auto       b2a71b82243607337b19f0642317fed88c144d536412d80eaaf90807f2905a1d
builtin:goals      72a7a0159443b41dde29dfba8a3de6a2c8a15b273935495a7b88561fa708aa68
builtin:mainagent  12bb69ee0886c4fb9beb936d882021a91104860f1da69a5e3eda3d9e345cd399
builtin:pentest    a54bd5713b1c121d0f2ff3a72e0304820b1368c4b4b8159d5d651895663ac652
builtin:planner    14e163f4a44da9fee20a214048a607adc3659b2025275569eb512532ca8c15d5
builtin:worker     80fbb1c610dd57e266c20b9145793339eee540b809f6db8f5336261b45879f08
assistant          a179c990bff1ab4e0a6f326945a1c44036ef4c8a54c7c5b635965f6cefdc9f0a
reporter           8ba98208f6c5f2ccb103c88ef13239c4264733400c0e3dfec96d3a3572d78d39
retester           d08326aa9fbd9a1367e716cf7ef46533e9096e9a56493f77d364efbf696f7fe0
wrapup:planner     0afba34ad25d8dfe87bac7596dcaeb59040049ce5c412bd62c04f5881ad5dcce
wrapup:mainagent   52333c82468d757ce3818c8c88379f9d1e7488d9bc388d116abc3423fe9529fa
wrapup:worker      2613bf7d5d9fdc2f549667355eb747d92f53de2be3756a57361732aed8fd7ebe
task-timeout:worker  bbefbdf2059d09703a0c6c665d4b4734f99c4df270974bb10c795d987b672343
task-timeout:planner d002b51c2a5c42f8197714f5b86b11539f85c3451a19e87472e8483ed0adfd76
judge              d6bf0f5fae9e98f551de5ac73a935a667f1c4eb18b30fd9da2ffb3e851ea2c15
```

The identical empty wrap-up used by `goals`, `auto`, and `pentest` has hash `9d0b47458ae54a85cad722d7078eacbe40a11e40e6e7aef774bd8610a61a08b1` and does not require a database rewrite.

- [ ] **Step 3: Run the new test and verify RED**

Run: `go test ./db -run TestMigrateBuiltinEnglishText -count=1 -v`

Expected: build failure because `migrateBuiltinEnglishText` is not defined.

- [ ] **Step 4: Commit the failing test**

```bash
git add db/english_migration_test.go
git commit -m "test(db): define English default migration behavior"
```

### Task 3: Implement the Safe Built-In Text Migration

**Files:**
- Create: `db/english_migration.go`
- Modify: `db/db.go`
- Modify: `db/config.go`
- Modify: `server/assembly.go`
- Modify: `server/manager.go`
- Create: `server/english_migration.go`
- Modify: `server/finding_retests.go`
- Modify: `agent/promptcatalog.go`
- Modify: `agent/retester.go`
- Modify: `agent/wrapup.go`
- Modify: `intercept/prompt.go`
- Test: `db/english_migration_test.go`

- [ ] **Step 1: Implement the migration boundary**

Define a narrow internal interface implemented by both `*sql.Conn` and `*sql.Tx`:

```go
type englishMigrationStore interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func migrateBuiltinEnglishText(ctx context.Context, store englishMigrationStore) error
```

Use stable keys and exact legacy values encoded with `\u` escapes for short database-owned labels.

For model-facing defaults owned by `agent` and `intercept`, add a public data-only API that does not import those packages:

```go
type LegacyEnglishDefaults struct {
	PromptBySHA256      map[string]string
	WrapupBySHA256      map[string]string
	TaskTimeoutBySHA256 map[string]string
	JudgeBySHA256       map[string]string
}

func (d *DB) MigrateLegacyEnglishDefaults(ctx context.Context, defaults LegacyEnglishDefaults) error
```

The method must take the existing schema advisory lock in a transaction, select only candidate built-in/current-default rows, hash stored text with `sha256.Sum256`, and replace it only when the hash exists in the supplied frozen map. The replacement strings come from the newly translated `agent` and `intercept` defaults passed by `server`, not a duplicate copy inside `db`.

Update only:

- Fixed agent names/descriptions and prompt-variable descriptions/examples identified by agent key and variable name.
- Built-in intercept rule names/messages identified by their unchanged pattern plus exact legacy name/message.
- Built-in asset-intercept notes identified by `builtin`, kind, pattern, and exact legacy note.
- Exact built-in prompt bodies and prompt notes, including assistant, reporter, and retester defaults, through the supplied prompt map.
- Saved per-agent wrap-up and task-timeout text only when its hash matches a supplied legacy hash.
- The saved LLM judge setting only when its hash matches the supplied legacy hash.

Never update tasks, conversations, findings, evidence, reports, notification history, or arbitrary user-authored prompt text.

- [ ] **Step 2: Wire both migration layers without an import cycle**

Call `migrateBuiltinEnglishText(context.Background(), conn)` in `db.Open` after `applySchemaWithRetry` and before `seedBuiltins`. Return errors as `migrate built-in English text: %w` so startup never silently leaves a partial translation.

In `agent/promptcatalog.go` and `agent/wrapup.go`, expose hash-to-current-English maps keyed by the frozen hashes above. In `server/english_migration.go`, combine those maps with the translated `intercept.DefaultJudgePrompt`. Call the helper from `NewManager` immediately after `db.Open`; on failure, close the database and return the wrapped error from `NewManager`. This runs before `server.New` calls `seedPrompts` and avoids changing the existing `server.New` signature. `db` must not import `agent` or `intercept`.

- [ ] **Step 3: Translate all fresh-install defaults**

Translate the code and SQL defaults in the listed files. Keep agent keys, roles, patterns, setting keys, tool names, placeholder names, JSON keys, and enum values unchanged. Change the default assistant instruction to answer in English and the reporter/retester prompts to produce English reports and conclusions.

- [ ] **Step 4: Run the migration tests and verify GREEN**

Run: `go test ./db -run 'TestMigrateBuiltinEnglishText|TestOpenSeed' -count=1 -v`

Expected: PASS, or PostgreSQL-dependent tests explicitly SKIP because no DSN is available; no build failures.

- [ ] **Step 5: Commit**

```bash
git add db/english_migration.go db/english_migration_test.go db/db.go db/config.go \
  server/assembly.go server/manager.go server/english_migration.go server/finding_retests.go agent/promptcatalog.go agent/retester.go \
  agent/wrapup.go intercept/prompt.go
git commit -m "feat(db): migrate built-in text to English"
```

### Task 4: Translate Agent Runtime and Prompt Contracts

**Files:**
- Modify: every file reported by `git grep ... -- agent` in Task 1.
- Test: every `agent/*_test.go` file in that report.

- [ ] **Step 1: Translate prompt and tool prose contextually**

Translate model-facing instructions, tool descriptions, error messages, logs, comments, test names, fixtures, and assertions. Preserve Go template fields such as `{{.Goal}}`, Markdown/code fences, tool names such as `report_finding`, JSON field names, terminal-reason enums, and security/authorization semantics.

- [ ] **Step 2: Verify the agent package is English-only**

Run:

```bash
! git grep -I -P -n '[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}]' -- agent
go test ./agent -count=1
```

Expected: no grep output, grep inversion exits 0, and package tests PASS.

- [ ] **Step 3: Commit**

```bash
git add agent
git commit -m "refactor(agent): translate runtime prompts to English"
```

### Task 5: Translate Database Source and Assertions

**Files:**
- Modify: every remaining file reported by `git grep ... -- db` in Task 1.
- Test: every affected `db/*_test.go`.

- [ ] **Step 1: Translate SQL and Go text**

Translate comments, default display text, error messages, migration notes, test fixture text, and assertions. Preserve SQL identifiers, constraints, setting keys, enums, patterns, and historical migration behavior. Express any legacy Chinese match needed by the English migration with Unicode escapes so the source remains Han-free.

- [ ] **Step 2: Verify the database package**

Run:

```bash
git ls-files -z 'db/*.go' | xargs -0 gofmt -w
! git grep -I -P -n '[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}]' -- db
go test ./db -count=1
```

Expected: no Han matches and tests PASS or explicitly SKIP only for unavailable PostgreSQL.

- [ ] **Step 3: Commit**

```bash
git add db
git commit -m "refactor(db): translate database text to English"
```

### Task 6: Translate Server Source and Tests

**Files:**
- Modify: every file reported by `git grep ... -- server` in Task 1.
- Test: every affected `server/*_test.go`.

- [ ] **Step 1: Translate API, logs, prompts, and comments**

Translate user-facing HTTP errors, operational logs, embedded model instructions, DTO descriptions, comments, fixtures, and exact assertions. Preserve HTTP status codes, routes, response keys, event kinds, enums, and retry/control flow.

- [ ] **Step 2: Verify the server package**

Run:

```bash
git ls-files -z 'server/*.go' | xargs -0 gofmt -w
! git grep -I -P -n '[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}]' -- server
go test ./server -count=1
```

Expected: no Han matches and tests PASS.

- [ ] **Step 3: Commit**

```bash
git add server
git commit -m "refactor(server): translate API text to English"
```

### Task 7: Translate Supporting Go Packages

**Files:**
- Modify: every matching file under `notify/`, `intercept/`, `traffic/`, `selfupdate/`, `report/`, `sidequestion/`, `llmpool/`, `llmrec/`, `mcphttp/`, `evidence/`, `guard/`, `enrich/`, `cmd/`, and `config/` from Task 1.
- Test: affected tests in those packages.

- [ ] **Step 1: Translate by package without changing contracts**

Keep notification payload schemas, signature inputs, webhook field names, transport enums, redaction rules, archive formats, and CLI flags unchanged. Translate display strings, email/IM templates, logs, comments, fixtures, and assertions.

- [ ] **Step 2: Run package verification**

Run:

```bash
git ls-files -z 'notify/*.go' 'intercept/*.go' 'traffic/*.go' 'selfupdate/*.go' \
  'report/*.go' 'sidequestion/*.go' 'llmpool/*.go' 'llmrec/*.go' 'mcphttp/*.go' \
  'evidence/*.go' 'guard/*.go' 'enrich/*.go' 'cmd/**/*.go' 'config/*.go' \
  | xargs -0 gofmt -w
! git grep -I -P -n '[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}]' -- \
  notify intercept traffic selfupdate report sidequestion llmpool llmrec \
  mcphttp evidence guard enrich cmd config
go test ./notify ./intercept ./traffic ./selfupdate ./report ./sidequestion \
  ./llmpool ./llmrec ./mcphttp ./evidence ./guard ./enrich ./cmd/... ./config
```

Expected: no Han matches and all selected package tests PASS.

- [ ] **Step 3: Commit**

```bash
git add notify intercept traffic selfupdate report sidequestion llmpool llmrec \
  mcphttp evidence guard enrich cmd config
git commit -m "refactor: translate supporting backend packages"
```

### Task 8: Complete Backend Verification

**Files:**
- Verify: all files in `/tmp/artex-backend-han-files.txt`.

- [ ] **Step 1: Run formatting and the full Go suite**

Run:

```bash
git ls-files -z '*.go' | xargs -0 gofmt -w
go test ./... -count=1
```

Expected: PASS with zero failures; environment-dependent database tests may SKIP with their existing explicit reason.

- [ ] **Step 2: Prove the backend is Han-free**

Run:

```bash
! git grep -I -P -n '[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}]' -- \
  agent db server notify intercept traffic selfupdate report sidequestion \
  llmpool llmrec mcphttp evidence guard enrich cmd config
git diff --check
```

Expected: no grep output and both commands exit 0.
