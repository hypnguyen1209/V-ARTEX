package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
)

func TestMigrateBuiltinEnglishTextRollsBackOnFailure(t *testing.T) {
	d := englishMigrationDB(t)
	sentinel := errors.New("stop migration")
	err := withEnglishMigrationTransaction(t.Context(), d.BeginTx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(t.Context(), `INSERT INTO settings(key,value) VALUES('english_migration_rollback','temporary')`); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("migration error = %v, want sentinel", err)
	}
	var count int
	if err := d.QueryRow(`SELECT count(*) FROM settings WHERE key='english_migration_rollback'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed migration committed partial changes")
	}
}

const legacyDefaultPromptNote = "\u5185\u7f6e\u9ed8\u8ba4"
const legacyResetPromptNote = "\u6062\u590d\u4e3a\u5185\u7f6e\u9ed8\u8ba4"
const legacyGenericWrapup = "\u4f60\u5373\u5c06\u56e0\u9884\u7b97\u8017\u5c3d\u88ab\u7ec8\u6b62。\u8bf7\u5148\u628a\u5df2\u5b8c\u6210\u4f46\u672a\u843d\u5e93\u7684\u7ed3\u679c\u5199\u56de,\u518d**\u5355\u72ec\u7528\u4e00\u53e5\u8bdd\u7eaf\u6587\u672c**\u603b\u7ed3\u4f60\u505a\u4e86\u4ec0\u4e48、\u5f97\u5230\u54ea\u4e9b\u5173\u952e\u7ed3\u8bba(\u8fd9\u53e5\u4f1a\u4f5c\u4e3a\u672c\u6b21\u8fd0\u884c\u7684\u7ed3\u679c\u5c55\u793a)。"

// These fingerprints are frozen from the approved pre-translation defaults.
var legacyEnglishPromptFingerprints = map[string]string{
	"builtin:auto":         "b2a71b82243607337b19f0642317fed88c144d536412d80eaaf90807f2905a1d",
	"builtin:goals":        "72a7a0159443b41dde29dfba8a3de6a2c8a15b273935495a7b88561fa708aa68",
	"builtin:mainagent":    "12bb69ee0886c4fb9beb936d882021a91104860f1da69a5e3eda3d9e345cd399",
	"builtin:pentest":      "a54bd5713b1c121d0f2ff3a72e0304820b1368c4b4b8159d5d651895663ac652",
	"builtin:planner":      "14e163f4a44da9fee20a214048a607adc3659b2025275569eb512532ca8c15d5",
	"builtin:worker":       "80fbb1c610dd57e266c20b9145793339eee540b809f6db8f5336261b45879f08",
	"assistant":            "a179c990bff1ab4e0a6f326945a1c44036ef4c8a54c7c5b635965f6cefdc9f0a",
	"reporter":             "8ba98208f6c5f2ccb103c88ef13239c4264733400c0e3dfec96d3a3572d78d39",
	"retester":             "d08326aa9fbd9a1367e716cf7ef46533e9096e9a56493f77d364efbf696f7fe0",
	"wrapup:planner":       "0afba34ad25d8dfe87bac7596dcaeb59040049ce5c412bd62c04f5881ad5dcce",
	"wrapup:mainagent":     "52333c82468d757ce3818c8c88379f9d1e7488d9bc388d116abc3423fe9529fa",
	"wrapup:worker":        "2613bf7d5d9fdc2f549667355eb747d92f53de2be3756a57361732aed8fd7ebe",
	"wrapup:generic":       "9d0b47458ae54a85cad722d7078eacbe40a11e40e6e7aef774bd8610a61a08b1",
	"task-timeout:worker":  "bbefbdf2059d09703a0c6c665d4b4734f99c4df270974bb10c795d987b672343",
	"task-timeout:planner": "d002b51c2a5c42f8197714f5b86b11539f85c3451a19e87472e8483ed0adfd76",
	"judge":                "d6bf0f5fae9e98f551de5ac73a935a667f1c4eb18b30fd9da2ffb3e851ea2c15",
}

func englishMigrationDB(t *testing.T) *DB {
	t.Helper()
	dsn := testDSN(t)
	probe, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres connectivity probe: %v", err)
	}
	defer probe.Close()
	if err := probe.PingContext(t.Context()); err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	d, err := Open(dsn)
	if err != nil {
		t.Fatalf("initialize available postgres: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func englishMigrationTx(t *testing.T, d *DB) *sql.Tx {
	t.Helper()
	tx, err := d.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

func englishMigrationExec(t *testing.T, tx *sql.Tx, query string, args ...any) {
	t.Helper()
	if _, err := tx.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func englishMigrationString(t *testing.T, tx *sql.Tx, want, query string, args ...any) {
	t.Helper()
	var got string
	if err := tx.QueryRowContext(t.Context(), query, args...).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
}

func englishMigrationTwice(t *testing.T, tx *sql.Tx) {
	t.Helper()
	for run := 1; run <= 2; run++ {
		if err := migrateBuiltinEnglishText(t.Context(), tx); err != nil {
			t.Fatalf("migration run %d: %v", run, err)
		}
	}
}

func englishMigrationAgent(t *testing.T, tx *sql.Tx, key, name, description string, builtin bool) int64 {
	t.Helper()
	var id int64
	if err := tx.QueryRowContext(t.Context(), `
INSERT INTO agents(key,name,description,role,builtin,enabled)
VALUES ($1,$2,$3,'assistant',$4,true)
ON CONFLICT (key) DO UPDATE SET name=$2,description=$3,builtin=$4
RETURNING id`, key, name, description, builtin).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func englishMigrationPrompt(t *testing.T, tx *sql.Tx, agentID int64, text, note, author string, current bool) int64 {
	t.Helper()
	var id int64
	if err := tx.QueryRowContext(t.Context(), `
INSERT INTO agent_prompts(agent_id,version,template_text,note,updated_by)
SELECT $1,COALESCE(max(version),0)+1,$2,$3,$4 FROM agent_prompts WHERE agent_id=$1
RETURNING id`, agentID, text, note, author).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if current {
		englishMigrationExec(t, tx, `UPDATE agents SET current_prompt_id=$1 WHERE id=$2`, id, agentID)
	}
	return id
}

func englishMigrationHash(text string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
}

func TestMigrateBuiltinEnglishTextLegacyFingerprints(t *testing.T) {
	if len(legacyEnglishPromptFingerprints) != 16 {
		t.Fatalf("legacy fingerprints = %d, want 16", len(legacyEnglishPromptFingerprints))
	}
	currentByKey := make(map[string]string, len(legacyEnglishPromptFingerprints))
	for name, fingerprint := range legacyEnglishPromptFingerprints {
		raw, err := hex.DecodeString(fingerprint)
		if err != nil || len(raw) != sha256.Size {
			t.Errorf("%s has invalid SHA-256 %q", name, fingerprint)
		}
		currentByKey[name] = "English replacement for " + name
	}
	// Server assembly must use this data-only constructor with current English
	// text from agent/intercept, so these are the production replacement maps.
	defaults := NewLegacyEnglishDefaults(currentByKey)
	for _, group := range []struct {
		name string
		got  map[string]string
		keys []string
	}{
		{"prompts", defaults.PromptBySHA256, []string{"builtin:auto", "builtin:goals", "builtin:mainagent", "builtin:pentest", "builtin:planner", "builtin:worker", "assistant", "reporter", "retester"}},
		{"wrapups", defaults.WrapupBySHA256, []string{"wrapup:planner", "wrapup:mainagent", "wrapup:worker", "wrapup:generic"}},
		{"task-timeouts", defaults.TaskTimeoutBySHA256, []string{"task-timeout:worker", "task-timeout:planner"}},
		{"judge", defaults.JudgeBySHA256, []string{"judge"}},
	} {
		t.Run(group.name, func(t *testing.T) {
			if len(group.got) != len(group.keys) {
				t.Errorf("replacement count = %d, want %d", len(group.got), len(group.keys))
			}
			for _, key := range group.keys {
				fingerprint := legacyEnglishPromptFingerprints[key]
				got, ok := group.got[fingerprint]
				if !ok || got != currentByKey[key] {
					t.Errorf("%s replacement at %s = %q (present %t), want %q", key, fingerprint, got, ok, currentByKey[key])
				}
			}
		})
	}
	const assistant = "\u4f60\u662f\u4e00\u4e2a\u4e50\u4e8e\u52a9\u4eba\u7684 AI \u52a9\u624b。\u8bf7\u7528\u7b80\u6d01、\u51c6\u786e\u7684\u4e2d\u6587\u56de\u7b54\u7528\u6237\u7684\u95ee\u9898；\u5728\u9700\u8981\u65f6\u4f7f\u7528\u53ef\u7528\u7684\u5de5\u5177\u6765\u5b8c\u6210\u4efb\u52a1。\u53ea\u505a\u7528\u6237\u8981\u6c42\u7684\u4e8b，\u4e0d\u81c6\u9020\u4fe1\u606f。"
	if got := englishMigrationHash(assistant); got != legacyEnglishPromptFingerprints["assistant"] {
		t.Fatalf("legacy assistant fingerprint = %s", got)
	}
	// A saved copy of this nonempty generic default also needs translation.
	if got := englishMigrationHash(legacyGenericWrapup); got != legacyEnglishPromptFingerprints["wrapup:generic"] {
		t.Fatalf("generic wrap-up fingerprint = %s", got)
	}
}

func TestMigrateBuiltinEnglishTextMissingReplacements(t *testing.T) {
	for _, current := range []map[string]string{nil, {}, {"assistant": "", "judge": " \n\t"}} {
		defaults := NewLegacyEnglishDefaults(current)
		if len(defaults.PromptBySHA256)+len(defaults.WrapupBySHA256)+len(defaults.TaskTimeoutBySHA256)+len(defaults.JudgeBySHA256) != 0 {
			t.Fatalf("missing or blank replacements must not populate migration maps: %#v", defaults)
		}
	}
}

func TestOpenSeedPreservesCustomizedBuiltinLabels(t *testing.T) {
	d := englishMigrationDB(t)
	a, err := d.GetAgentByKey("planner")
	if err != nil || a == nil {
		t.Fatalf("get planner: %v", err)
	}
	var description, example string
	if err := d.QueryRow(`SELECT description,example FROM agent_prompt_vars WHERE agent_id=$1 AND var_name='Goal'`, a.ID).Scan(&description, &example); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := d.Exec(`UPDATE agents SET name=$1,description=$2 WHERE id=$3`, a.Name, a.Description, a.ID); err != nil {
			t.Error(err)
		}
		if _, err := d.Exec(`UPDATE agent_prompt_vars SET description=$1,example=$2 WHERE agent_id=$3 AND var_name='Goal'`, description, example, a.ID); err != nil {
			t.Error(err)
		}
	})
	if _, err := d.Exec(`UPDATE agents SET name='Operator planner',description='Operator description' WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE agent_prompt_vars SET description='Operator variable',example='Operator example' WHERE agent_id=$1 AND var_name='Goal'`, a.ID); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(testDSN(t))
	if err != nil {
		t.Fatalf("reopen available postgres: %v", err)
	}
	defer reopened.Close()
	var got string
	if err := reopened.QueryRow(`SELECT concat_ws('|',a.name,a.description,v.description,v.example)
FROM agents a JOIN agent_prompt_vars v ON v.agent_id=a.id WHERE a.id=$1 AND v.var_name='Goal'`, a.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if want := "Operator planner|Operator description|Operator variable|Operator example"; got != want {
		t.Fatalf("customized labels = %q, want %q", got, want)
	}
}

func TestMigrateBuiltinEnglishTextAssetNotes(t *testing.T) {
	d := englishMigrationDB(t)
	tx := englishMigrationTx(t, d)
	for _, tc := range []struct{ pattern, legacy, english string }{
		{".gov", "[\u5185\u7f6e] \u653f\u5e9c\u7f51\u7ad9 (.gov)", "[Built-in] Government website (.gov)"},
		{".gov.cn", "[\u5185\u7f6e] \u653f\u5e9c\u7f51\u7ad9 (.gov.cn)", "[Built-in] Government website (.gov.cn)"},
		{".edu", "[\u5185\u7f6e] \u6559\u80b2\u7f51\u7ad9 (.edu)", "[Built-in] Educational website (.edu)"},
		{".edu.cn", "[\u5185\u7f6e] \u6559\u80b2\u7f51\u7ad9 (.edu.cn)", "[Built-in] Educational website (.edu.cn)"},
	} {
		t.Run(tc.pattern, func(t *testing.T) {
			for _, fixture := range []struct {
				name, kind, pattern, note, want string
				builtin                         bool
			}{
				{"legacy", "fuzzy_domain", tc.pattern, tc.legacy, tc.english, true},
				{"customized", "fuzzy_domain", tc.pattern, tc.legacy + "\nOperator's custom note\t", tc.legacy + "\nOperator's custom note\t", true},
				{"user-authored", "fuzzy_domain", tc.pattern, tc.legacy, tc.legacy, false},
				{"different-kind", "exact_domain", tc.pattern, tc.legacy, tc.legacy, true},
				{"different-pattern", "fuzzy_domain", "example.invalid", tc.legacy, tc.legacy, true},
			} {
				t.Run(fixture.name, func(t *testing.T) {
					var id int64
					if err := tx.QueryRowContext(t.Context(), `
INSERT INTO asset_intercept_rules(enabled,kind,pattern,note,builtin)
VALUES(false,$1,$2,$3,$4) RETURNING id`, fixture.kind, fixture.pattern, fixture.note, fixture.builtin).Scan(&id); err != nil {
						t.Fatal(err)
					}
					englishMigrationTwice(t, tx)
					englishMigrationString(t, tx, fixture.want, `SELECT note FROM asset_intercept_rules WHERE id=$1`, id)
					englishMigrationString(t, tx, fixture.kind, `SELECT kind FROM asset_intercept_rules WHERE id=$1`, id)
					englishMigrationString(t, tx, fixture.pattern, `SELECT pattern FROM asset_intercept_rules WHERE id=$1`, id)
					englishMigrationString(t, tx, "false", `SELECT enabled::text FROM asset_intercept_rules WHERE id=$1`, id)
				})
			}
		})
	}
}

func TestMigrateBuiltinEnglishTextInterceptRules(t *testing.T) {
	d := englishMigrationDB(t)
	tx := englishMigrationTx(t, d)
	for _, tc := range []struct{ pattern, legacyName, englishName, legacyMessage, englishMessage string }{
		{`(?i)\brm\b.{0,80}(?:-[a-z]*r[a-z]*f[a-z]*|-[a-z]*f[a-z]*r[a-z]*|--recursive|--no-preserve-root)`,
			"[\u5185\u7f6e] \u9012\u5f52\u5f3a\u5236\u5220\u9664 rm -rf", "[Built-in] Recursive forced deletion rm -rf",
			"\u7981\u6b62\u6267\u884c\u9012\u5f52\u5f3a\u5236\u5220\u9664（rm -rf / rm --recursive），\u53ef\u80fd\u6c38\u4e45\u635f\u574f\u7cfb\u7edf\u6216\u9776\u673a\u73af\u5883", "Recursive forced deletion (rm -rf / rm --recursive) is prohibited because it may permanently damage the system or target environment"},
		{`\brm\b[^"'\n]{0,60}["'\s](/|/etc|/bin|/usr|/boot|/var|/lib|/sys|/proc|/dev|/sbin|/root)`,
			"[\u5185\u7f6e] \u5220\u9664\u7cfb\u7edf\u5173\u952e\u76ee\u5f55", "[Built-in] Delete critical system directories",
			"\u7981\u6b62\u5220\u9664\u7cfb\u7edf\u5173\u952e\u8def\u5f84", "Deleting critical system paths is prohibited"},
		{`\bmkfs\b`, "[\u5185\u7f6e] \u78c1\u76d8\u683c\u5f0f\u5316 mkfs", "[Built-in] Disk formatting mkfs",
			"\u7981\u6b62\u683c\u5f0f\u5316\u78c1\u76d8（mkfs）", "Disk formatting (mkfs) is prohibited"},
		{`\bdd\b[^|\n]{0,100}\bof=\s*/dev/[a-zA-Z]`, "[\u5185\u7f6e] \u8986\u5199\u78c1\u76d8\u8bbe\u5907 dd", "[Built-in] Overwrite disk devices dd",
			"\u7981\u6b62\u4f7f\u7528 dd \u8986\u5199\u78c1\u76d8\u8bbe\u5907", "Overwriting disk devices with dd is prohibited"},
		{`:\(\)\s*\{[^}]*:\|:`, "[\u5185\u7f6e] Fork \u70b8\u5f39", "[Built-in] Fork bomb",
			"\u7981\u6b62\u6267\u884c Fork \u70b8\u5f39", "Executing a fork bomb is prohibited"},
		{`\b(?:shutdown|reboot|halt|poweroff|init\s+[06])\b`, "[\u5185\u7f6e] \u5173\u673a / \u91cd\u542f", "[Built-in] Shutdown / restart",
			"\u7981\u6b62\u6267\u884c\u5173\u673a\u6216\u91cd\u542f\u547d\u4ee4", "Shutdown or restart commands are prohibited"},
		{`\bkill\s+-9\s+-1\b|\bkillall\s+-9\b`, "[\u5185\u7f6e] \u6740\u6b7b\u5168\u90e8\u8fdb\u7a0b", "[Built-in] Kill all processes",
			"\u7981\u6b62 kill -9 -1 \u6216 killall -9（\u6740\u6b7b\u6240\u6709\u8fdb\u7a0b）", "kill -9 -1 or killall -9 (killing all processes) is prohibited"},
		{`\b(?:shred|wipe)\b[^|\n]{0,80}/dev/[a-zA-Z]`, "[\u5185\u7f6e] \u78c1\u76d8\u64e6\u9664 shred / wipe", "[Built-in] Disk erasure shred / wipe",
			"\u7981\u6b62\u5bf9\u78c1\u76d8\u8bbe\u5907\u6267\u884c shred/wipe \u64e6\u9664", "Erasing disk devices with shred/wipe is prohibited"},
		{`\biptables\s+(?:-F|--flush)\b|\bnft\s+flush\s+ruleset\b`, "[\u5185\u7f6e] \u6e05\u7a7a\u9632\u706b\u5899\u89c4\u5219", "[Built-in] Flush firewall rules",
			"\u7981\u6b62\u6e05\u7a7a\u9632\u706b\u5899\u89c4\u5219（iptables -F / nft flush）", "Flushing firewall rules (iptables -F / nft flush) is prohibited"},
		{`(?i)\bDROP\s+(?:DATABASE|TABLE|SCHEMA|INDEX|VIEW|TABLESPACE|USER|ROLE)\b`, "[\u5185\u7f6e] SQL DROP DATABASE / TABLE / SCHEMA", "[Built-in] SQL DROP DATABASE / TABLE / SCHEMA",
			"\u7981\u6b62\u6267\u884c DROP \u64cd\u4f5c，\u53ef\u80fd\u4e0d\u53ef\u9006\u5730\u9500\u6bc1\u6570\u636e\u5e93\u5bf9\u8c61", "DROP operations are prohibited because they may irreversibly destroy database objects"},
		{`(?i)\bTRUNCATE\s+(?:TABLE\s+)?\w`, "[\u5185\u7f6e] SQL TRUNCATE", "[Built-in] SQL TRUNCATE",
			"\u7981\u6b62\u6267\u884c TRUNCATE，\u53ef\u80fd\u6e05\u7a7a\u6570\u636e\u8868\u6240\u6709\u6570\u636e", "TRUNCATE is prohibited because it may erase all data in a table"},
		{`(?i)\.(?:dropDatabase|dropCollection|drop)\s*\(`, "[\u5185\u7f6e] MongoDB drop / dropDatabase", "[Built-in] MongoDB drop / dropDatabase",
			"\u7981\u6b62\u6267\u884c MongoDB drop \u64cd\u4f5c", "MongoDB drop operations are prohibited"},
		{`(?i)\b(?:FLUSHALL|FLUSHDB)\b`, "[\u5185\u7f6e] Redis FLUSHALL / FLUSHDB", "[Built-in] Redis FLUSHALL / FLUSHDB",
			"\u7981\u6b62\u6267\u884c Redis FLUSHALL / FLUSHDB，\u53ef\u80fd\u6e05\u7a7a\u5168\u90e8\u7f13\u5b58\u6570\u636e", "Redis FLUSHALL / FLUSHDB is prohibited because it may erase all cached data"},
		{`(?i)\bcurl\b[^|\n&;"]{0,300}(?:-X\s*DELETE|--request\s+DELETE|-XDELETE)|\bwget\b[^|\n&;"]{0,300}--method[=\s]+DELETE`,
			"[\u5185\u7f6e] curl / wget \u53d1\u9001 DELETE \u8bf7\u6c42", "[Built-in] HTTP DELETE requests via curl / wget",
			"\u7981\u6b62\u901a\u8fc7 curl/wget \u53d1\u9001 HTTP DELETE \u8bf7\u6c42，\u53ef\u80fd\u5220\u9664\u76ee\u6807\u7cfb\u7edf\u6570\u636e", "HTTP DELETE requests via curl/wget are prohibited because they may delete target system data"},
		{`(?i)\b(?:requests|httpx|aiohttp|urllib\.request)\.delete\s*\(|session\.delete\s*\(|client\.delete\s*\(`,
			"[\u5185\u7f6e] Python HTTP \u5ba2\u6237\u7aef DELETE（requests/httpx/aiohttp）", "[Built-in] Python HTTP client DELETE (requests/httpx/aiohttp)",
			"\u7981\u6b62\u4f7f\u7528 Python HTTP \u5ba2\u6237\u7aef\u53d1\u9001 DELETE \u8bf7\u6c42", "DELETE requests via Python HTTP clients are prohibited"},
		{`(?i)axios\.delete\s*\(|method\s*[:=]\s*['"]DELETE['"]`,
			"[\u5185\u7f6e] \u811a\u672c\u4e2d\u58f0\u660e HTTP DELETE \u65b9\u6cd5（JS/\u901a\u7528）", "[Built-in] HTTP DELETE method in scripts (JS/general)",
			"\u7981\u6b62\u5728\u811a\u672c\u4e2d\u58f0\u660e\u5e76\u53d1\u9001 HTTP DELETE \u8bf7\u6c42", "Declaring and sending HTTP DELETE requests in scripts is prohibited"},
		{`(?i)/(?:clear|wipe|flush|purge|truncate|drop|destroy|factory[-_]reset|reset[-_]all)(?:[/?#"'\s]|$)`,
			"[\u5185\u7f6e] \u6279\u91cf\u6e05\u7a7a / \u6e05\u9664\u63a5\u53e3\u8def\u5f84", "[Built-in] Bulk clearing / erasure endpoint paths",
			"\u7981\u6b62\u8c03\u7528\u6279\u91cf\u6e05\u7a7a\u6216\u9500\u6bc1\u7c7b\u63a5\u53e3（/clear /wipe /flush /purge \u7b49）", "Bulk clearing or destruction endpoints (/clear /wipe /flush /purge, etc.) are prohibited"},
		{`(?i)\b(rm\s+-rf\s+/|mkfs|dd\s+if=|:\(\)\s*\{|shutdown|reboot|>\s*/dev/sd)`,
			"[\u5185\u7f6e] \u7834\u574f\u6027\u7cfb\u7edf\u547d\u4ee4", "[Built-in] Destructive system commands",
			"\u7834\u574f\u6027\u547d\u4ee4\u88ab\u62d2\u7edd（rm -rf / / mkfs / dd / fork bomb / \u5173\u673a\u91cd\u542f / \u8986\u5199\u78c1\u76d8\u8bbe\u5907）", "Destructive command denied (rm -rf / / mkfs / dd / fork bomb / shutdown or restart / overwriting disk devices)"},
		{`(?i)(curl|wget|nc|ncat)\b[^|]*\b(\|\s*(curl|wget|nc))`,
			"[\u5185\u7f6e] \u6570\u636e\u5916\u6cc4\u7ba1\u9053", "[Built-in] Data exfiltration pipeline",
			"\u7591\u4f3c\u6570\u636e\u5916\u6cc4\u7ba1\u9053\u88ab\u62d2\u7edd（\u547d\u4ee4\u8f93\u51fa\u7ecf curl/wget/nc \u5916\u4f20）", "Suspected data exfiltration pipeline denied (command output sent externally via curl/wget/nc)"},
		{deleteEndpointPathPattern, "[\u5185\u7f6e] \u5220\u9664\u7c7b\u63a5\u53e3\u8def\u5f84", "[Built-in] Deletion endpoint paths",
			"\u7981\u6b62\u8c03\u7528\u5220\u9664\u7c7b\u63a5\u53e3（/delete /remove /unlink /erase \u7b49），\u4e0d\u8bba\u4f7f\u7528\u54ea\u79cd HTTP \u65b9\u6cd5——\u591a\u6570\u5e94\u7528\u7684\u5220\u9664\u63a5\u53e3\u7528 GET/POST \u5c31\u80fd\u89e6\u53d1，\u540c\u6837\u4f1a\u771f\u5b9e\u5220\u9664\u76ee\u6807\u6570\u636e", "Deletion endpoints (/delete /remove /unlink /erase, etc.) are prohibited regardless of HTTP method; many applications trigger deletion via GET/POST, which also deletes real target data"},
	} {
		t.Run(tc.englishName, func(t *testing.T) {
			for _, fixture := range []struct{ name, pattern, ruleName, message, wantName, wantMessage string }{
				{"legacy", tc.pattern, tc.legacyName, tc.legacyMessage, tc.englishName, tc.englishMessage},
				{"custom-name", tc.pattern, "Custom\t" + tc.legacyName, tc.legacyMessage, "Custom\t" + tc.legacyName, tc.englishMessage},
				{"custom-message", tc.pattern, tc.legacyName, tc.legacyMessage + "\nCustom policy ", tc.englishName, tc.legacyMessage + "\nCustom policy "},
				{"user-rule", tc.pattern, "Operator rule", "  Operator policy\n", "Operator rule", "  Operator policy\n"},
				{"different-pattern", "operator-pattern", tc.legacyName, tc.legacyMessage, tc.legacyName, tc.legacyMessage},
			} {
				t.Run(fixture.name, func(t *testing.T) {
					var id int64
					if err := tx.QueryRowContext(t.Context(), `
INSERT INTO intercept_rules(name,enabled,priority,match_target,match_type,pattern,action,message,timeout_enabled,timeout_seconds,timeout_action)
VALUES($1,false,17,'tool_input','regex',$2,'ask',$3,true,123,'allow') RETURNING id`,
						fixture.ruleName, fixture.pattern, fixture.message).Scan(&id); err != nil {
						t.Fatal(err)
					}
					englishMigrationTwice(t, tx)
					englishMigrationString(t, tx, fixture.wantName, `SELECT name FROM intercept_rules WHERE id=$1`, id)
					englishMigrationString(t, tx, fixture.wantMessage, `SELECT message FROM intercept_rules WHERE id=$1`, id)
					englishMigrationString(t, tx, fixture.pattern, `SELECT pattern FROM intercept_rules WHERE id=$1`, id)
					englishMigrationString(t, tx, "false|17|tool_input|regex|ask|true|123|allow", `
SELECT concat_ws('|',enabled::text,priority,match_target,match_type,action,timeout_enabled::text,timeout_seconds,timeout_action)
FROM intercept_rules WHERE id=$1`, id)
				})
			}
		})
	}
}

func TestMigrateBuiltinEnglishTextAgentLabels(t *testing.T) {
	d := englishMigrationDB(t)
	for _, tc := range []struct {
		key, legacyName, englishName, legacyDescription, englishDescription string
		builtin                                                             bool
	}{
		{"goals", "\u76ee\u6807\u62c6\u89e3", "Goal decomposition", "\u628a\u6e17\u900f\u4efb\u52a1\u76ee\u6807\u62c6\u89e3\u6210\u82e5\u5e72\u72ec\u7acb、\u53ef\u9a8c\u8bc1\u7684\u5b50\u76ee\u6807。", "Break penetration testing objectives into independent, verifiable subgoals.", true},
		{"planner", "\u89c4\u5212", "Planner", "\u8bfb\u53d6\u6001\u52bf、\u5224\u5b9a\u76ee\u6807，\u53ea\u5728\u786e\u6709\u672a\u8986\u76d6\u7684\u65b0\u65b9\u5411\u65f6\u8865\u5145\u63a2\u7d22\u610f\u56fe（\u6bcf\u4efb\u52a1\u4e00\u4e2a\u89c4\u5212\u5faa\u73af）。", "Assess the current situation and objectives, and add exploration intents only for new, uncovered directions (one planning loop per task).", true},
		{"mainagent", "\u4e3b", "Main", "\u4eba\u673a\u63a5\u53e3：\u89c2\u5bdf\u8fdb\u5c55，\u628a\u4eba\u7684\u610f\u56fe\u843d\u6210 hint \u6216\u9ad8\u4f18\u5148\u7ea7\u610f\u56fe。", "Human interface: observe progress and turn user requests into hints or high-priority intents.", true},
		{"worker", "\u6267\u884c", "Worker", "\u9886\u53d6\u4e00\u6761\u610f\u56fe\u6267\u884c，\u628a\u53d1\u73b0\u7684\u4e8b\u5b9e/\u6f0f\u6d1e\u5199\u56de\u77e5\u8bc6\u56fe\u8c31\u540e\u505c\u6b62。", "Claim and execute one intent, record discovered facts and vulnerabilities in the knowledge graph, then stop.", true},
		{"auto", "Auto", "Auto", "\u5e73\u53f0\u64cd\u4f5c\u52a9\u624b：\u7528\u5de5\u5177\u7ba1\u7406\u4efb\u52a1(\u5efa/\u770b/\u6682\u505c/\u7ed9\u63d0\u793a)\u4e0e\u8d44\u4ea7，\u5e76\u53ef\u521b\u5efa/\u4fee\u6539 skill、\u81ea\u5b9a\u4e49\u5de5\u5177、MCP。", "Platform assistant: use tools to manage tasks (create/view/pause/hint) and assets, and create or update skills, custom tools, and MCP servers.", true},
		{"pentest", "\u6e17\u900f\u6d4b\u8bd5", "Penetration testing", "\u72ec\u7acb\u6e17\u900f agent：\u4e00\u4eba\u4ece\u4fa6\u5bdf→\u627e\u653b\u51fb\u9762→\u6df1\u5165\u5229\u7528→\u9a8c\u8bc1→\u6536\u5c3e\u8d70\u5b8c\u6574\u6761\u94fe，\u81ea\u5df1\u89c4\u5212、\u81ea\u5df1\u6267\u884c、\u81ea\u5df1\u5bf9\u6297\u5f0f\u9a8c\u8bc1。", "Standalone penetration testing agent: handle reconnaissance, attack surface discovery, exploitation, verification, and wrap-up with independent planning, execution, and adversarial verification.", true},
		{"reporter", "\u62a5\u544a\u64b0\u5199", "Report writer", "\u6f0f\u6d1e\u8be6\u7ec6\u62a5\u544a\u64b0\u5199：\u53d1\u73b0\u6f0f\u6d1e\u65f6\u81ea\u52a8\u89e6\u53d1，\u67e5\u53d6\u8bc1\u636e\u4e0e\u6267\u884c\u8fc7\u7a0b\u540e\u5199 Markdown \u62a5\u544a\u5e76\u56de\u5199。", "Write detailed vulnerability reports: automatically triggered by findings, review evidence and execution traces, then write and save a Markdown report.", false},
		{"retester", "\u6f0f\u6d1e\u590d\u6d4b", "Vulnerability retest", "\u4ece\u6f0f\u6d1e\u8be6\u60c5\u624b\u52a8\u542f\u52a8，\u8bfb\u53d6\u539f\u8bc1\u636e\u5e76\u4fdd\u5b58\u72ec\u7acb\u590d\u6d4b\u7ed3\u8bba。", "Start manually from vulnerability details, review original evidence, and save an independent retest conclusion.", false},
	} {
		t.Run(tc.key, func(t *testing.T) {
			tx := englishMigrationTx(t, d)
			id := englishMigrationAgent(t, tx, tc.key, tc.legacyName, tc.legacyDescription, tc.builtin)
			englishMigrationTwice(t, tx)
			englishMigrationString(t, tx, tc.englishName, `SELECT name FROM agents WHERE id=$1`, id)
			englishMigrationString(t, tx, tc.englishDescription, `SELECT description FROM agents WHERE id=$1`, id)

			customName := "  " + tc.legacyName + "\nCustomized"
			customDescription := tc.legacyDescription + "\tOperator edits "
			englishMigrationExec(t, tx, `UPDATE agents SET name=$1,description=$2 WHERE id=$3`, customName, tc.legacyDescription, id)
			englishMigrationTwice(t, tx)
			englishMigrationString(t, tx, customName, `SELECT name FROM agents WHERE id=$1`, id)
			englishMigrationString(t, tx, tc.englishDescription, `SELECT description FROM agents WHERE id=$1`, id)
			englishMigrationExec(t, tx, `UPDATE agents SET name=$1,description=$2 WHERE id=$3`, tc.legacyName, customDescription, id)
			englishMigrationTwice(t, tx)
			englishMigrationString(t, tx, tc.englishName, `SELECT name FROM agents WHERE id=$1`, id)
			englishMigrationString(t, tx, customDescription, `SELECT description FROM agents WHERE id=$1`, id)
			englishMigrationExec(t, tx, `UPDATE agents SET name=$1,description=$2 WHERE id=$3`, customName, customDescription, id)
			englishMigrationTwice(t, tx)
			englishMigrationString(t, tx, customName, `SELECT name FROM agents WHERE id=$1`, id)
			englishMigrationString(t, tx, customDescription, `SELECT description FROM agents WHERE id=$1`, id)

			customID := englishMigrationAgent(t, tx, "english_migration_user", tc.legacyName, tc.legacyDescription, false)
			englishMigrationTwice(t, tx)
			englishMigrationString(t, tx, tc.legacyName, `SELECT name FROM agents WHERE id=$1`, customID)
			englishMigrationString(t, tx, tc.legacyDescription, `SELECT description FROM agents WHERE id=$1`, customID)
			if tc.builtin {
				englishMigrationExec(t, tx, `UPDATE agents SET name=$1,description=$2,builtin=false WHERE id=$3`, tc.legacyName, tc.legacyDescription, id)
				englishMigrationTwice(t, tx)
				englishMigrationString(t, tx, tc.legacyName, `SELECT name FROM agents WHERE id=$1`, id)
				englishMigrationString(t, tx, tc.legacyDescription, `SELECT description FROM agents WHERE id=$1`, id)
			}
		})
	}
}

func TestMigrateBuiltinEnglishTextPromptVariableMetadata(t *testing.T) {
	d := englishMigrationDB(t)
	for _, tc := range []struct{ key, name, legacyDescription, englishDescription, legacyExample, englishExample, source string }{
		{"goals", "EngagementDescription", "\u4efb\u52a1\u63cf\u8ff0（\u6d4b\u8bd5\u5bf9\u8c61/\u80cc\u666f）", "Task description (target/background)", "\u6d4b\u8bd5 example.com \u7ad9\u70b9", "Test the example.com site", "exploration"},
		{"planner", "Goal", "\u4efb\u52a1\u603b\u76ee\u6807", "Overall task objective", "\u62ff\u4e0b example.com \u7684\u7ba1\u7406\u5458\u6743\u9650", "Obtain administrator access to example.com", "exploration"},
		{"planner", "AssetSummary", "\u8d44\u4ea7\u8ba1\u6570/\u7c7b\u578b\u5206\u5e03\u6458\u8981(\u53ef\u9009)", "Asset counts and type distribution summary (optional)", "domain:3 ip:5 site:2", "domain:3 ip:5 site:2", "distilled"},
		{"mainagent", "Goal", "\u5f53\u524d\u4efb\u52a1\u76ee\u6807", "Current task objective", "\u62ff\u4e0b example.com \u7684\u7ba1\u7406\u5458\u6743\u9650", "Obtain administrator access to example.com", "exploration"},
		{"mainagent", "AssetSummary", "\u5f00\u5c40\u6001\u52bf\u6458\u8981(\u53ef\u9009)", "Initial situation summary (optional)", "domain:3 ip:5", "domain:3 ip:5", "distilled"},
		{"mainagent", "FindingsSummary", "\u5df2\u786e\u8ba4\u6f0f\u6d1e\u6458\u8981(\u53ef\u9009)", "Confirmed vulnerability summary (optional)", "high:1 medium:2", "high:1 medium:2", "distilled"},
		{"worker", "ProxyAddr", "\u8bb0\u5f55\u4ee3\u7406\u5730\u5740(\u9a71\u52a8 if \u53cc\u6587\u6848)", "Recording proxy address (controls conditional prompt text)", "127.0.0.1:8080", "127.0.0.1:8080", "runtime"},
		{"worker", "WorkerName", "worker \u81ea\u6211\u6807\u8bc6(\u53ef\u9009)", "Worker self-identification (optional)", "worker-1", "worker-1", "runtime"},
	} {
		t.Run(tc.key+"/"+tc.name, func(t *testing.T) {
			tx := englishMigrationTx(t, d)
			id := englishMigrationAgent(t, tx, tc.key, "Operator label", "Operator description", true)
			englishMigrationExec(t, tx, `
INSERT INTO agent_prompt_vars(agent_id,var_name,description,example,source) VALUES($1,$2,$3,$4,$5)
ON CONFLICT(agent_id,var_name) DO UPDATE SET description=$3,example=$4,source=$5`,
				id, tc.name, tc.legacyDescription, tc.legacyExample, tc.source)
			englishMigrationTwice(t, tx)
			englishMigrationString(t, tx, tc.englishDescription, `SELECT description FROM agent_prompt_vars WHERE agent_id=$1 AND var_name=$2`, id, tc.name)
			englishMigrationString(t, tx, tc.englishExample, `SELECT example FROM agent_prompt_vars WHERE agent_id=$1 AND var_name=$2`, id, tc.name)
			englishMigrationString(t, tx, tc.source, `SELECT source FROM agent_prompt_vars WHERE agent_id=$1 AND var_name=$2`, id, tc.name)

			customDescription, customExample := tc.legacyDescription+"\nCustom ", tc.legacyExample+"\tCustom "
			englishMigrationExec(t, tx, `UPDATE agent_prompt_vars SET description=$1,example=$2 WHERE agent_id=$3 AND var_name=$4`, customDescription, tc.legacyExample, id, tc.name)
			englishMigrationTwice(t, tx)
			englishMigrationString(t, tx, customDescription, `SELECT description FROM agent_prompt_vars WHERE agent_id=$1 AND var_name=$2`, id, tc.name)
			englishMigrationString(t, tx, tc.englishExample, `SELECT example FROM agent_prompt_vars WHERE agent_id=$1 AND var_name=$2`, id, tc.name)
			englishMigrationExec(t, tx, `UPDATE agent_prompt_vars SET description=$1,example=$2 WHERE agent_id=$3 AND var_name=$4`, tc.legacyDescription, customExample, id, tc.name)
			englishMigrationTwice(t, tx)
			englishMigrationString(t, tx, tc.englishDescription, `SELECT description FROM agent_prompt_vars WHERE agent_id=$1 AND var_name=$2`, id, tc.name)
			englishMigrationString(t, tx, customExample, `SELECT example FROM agent_prompt_vars WHERE agent_id=$1 AND var_name=$2`, id, tc.name)
			englishMigrationExec(t, tx, `UPDATE agent_prompt_vars SET description=$1,example=$2 WHERE agent_id=$3 AND var_name=$4`, customDescription, customExample, id, tc.name)
			englishMigrationExec(t, tx, `INSERT INTO agent_prompt_vars(agent_id,var_name,description,example,source) VALUES($1,'OperatorVar',$2,$3,$4)`, id, tc.legacyDescription, tc.legacyExample, tc.source)
			userID := englishMigrationAgent(t, tx, "english_migration_user", "User", "User", false)
			englishMigrationExec(t, tx, `INSERT INTO agent_prompt_vars(agent_id,var_name,description,example,source) VALUES($1,$2,$3,$4,$5)`, userID, tc.name, tc.legacyDescription, tc.legacyExample, tc.source)
			englishMigrationTwice(t, tx)
			englishMigrationString(t, tx, customDescription, `SELECT description FROM agent_prompt_vars WHERE agent_id=$1 AND var_name=$2`, id, tc.name)
			englishMigrationString(t, tx, customExample, `SELECT example FROM agent_prompt_vars WHERE agent_id=$1 AND var_name=$2`, id, tc.name)
			for _, row := range []struct {
				id   int64
				name string
			}{{id, "OperatorVar"}, {userID, tc.name}} {
				englishMigrationString(t, tx, tc.legacyDescription, `SELECT description FROM agent_prompt_vars WHERE agent_id=$1 AND var_name=$2`, row.id, row.name)
				englishMigrationString(t, tx, tc.legacyExample, `SELECT example FROM agent_prompt_vars WHERE agent_id=$1 AND var_name=$2`, row.id, row.name)
			}
		})
	}
}

func TestMigrateBuiltinEnglishTextPromptNotes(t *testing.T) {
	d := englishMigrationDB(t)
	tx := englishMigrationTx(t, d)
	agentID := englishMigrationAgent(t, tx, "planner", "Operator label", "Operator description", true)
	for _, tc := range []struct{ note, author, want string }{
		{legacyDefaultPromptNote, "system", "Built-in default"},
		{legacyResetPromptNote, "system", "Restored built-in default"},
		{legacyDefaultPromptNote + "\nOperator edit ", "system", legacyDefaultPromptNote + "\nOperator edit "},
		{legacyDefaultPromptNote, "user", legacyDefaultPromptNote},
		{legacyResetPromptNote, "user", legacyResetPromptNote},
	} {
		t.Run(tc.want+"/"+tc.author, func(t *testing.T) {
			id := englishMigrationPrompt(t, tx, agentID, "Operator's prompt body\n", tc.note, tc.author, true)
			englishMigrationTwice(t, tx)
			englishMigrationString(t, tx, tc.want, `SELECT note FROM agent_prompts WHERE id=$1`, id)
			englishMigrationString(t, tx, "Operator's prompt body\n", `SELECT template_text FROM agent_prompts WHERE id=$1`, id)
		})
	}
}

func TestMigrateBuiltinEnglishTextHashGuardedPrompts(t *testing.T) {
	d := englishMigrationDB(t)
	for _, tc := range []struct {
		key     string
		builtin bool
	}{
		{"auto", true}, {"goals", true}, {"mainagent", true}, {"pentest", true},
		{"planner", true}, {"worker", true}, {"reporter", false}, {"retester", false},
		{"english_migration_assistant", false},
	} {
		t.Run(tc.key, func(t *testing.T) {
			tx := englishMigrationTx(t, d)
			agentID := englishMigrationAgent(t, tx, tc.key, "Operator label", "Operator description", tc.builtin)
			legacy := "\u65e7\u63d0\u793a\u8bcd\n" + tc.key + "\n{{.Goal}}"
			english := "English default\n" + tc.key + "\n{{.Goal}}"
			defaults := LegacyEnglishDefaults{PromptBySHA256: map[string]string{englishMigrationHash(legacy): english}}
			legacyID := englishMigrationPrompt(t, tx, agentID, legacy, legacyDefaultPromptNote, "system", true)
			historyID := englishMigrationPrompt(t, tx, agentID, legacy, legacyDefaultPromptNote, "user", false)
			systemHistoryID := englishMigrationPrompt(t, tx, agentID, legacy, legacyDefaultPromptNote, "system", false)
			for run := 1; run <= 2; run++ {
				if err := migrateLegacyEnglishDefaults(t.Context(), tx, defaults); err != nil {
					t.Fatalf("migration run %d: %v", run, err)
				}
			}
			englishMigrationString(t, tx, english, `SELECT template_text FROM agent_prompts WHERE id=$1`, legacyID)
			englishMigrationString(t, tx, legacy, `SELECT template_text FROM agent_prompts WHERE id=$1`, historyID)
			englishMigrationString(t, tx, legacy, `SELECT template_text FROM agent_prompts WHERE id=$1`, systemHistoryID)
			englishMigrationString(t, tx, legacyDefaultPromptNote, `SELECT note FROM agent_prompts WHERE id=$1`, systemHistoryID)
			englishMigrationString(t, tx, "system", `SELECT updated_by FROM agent_prompts WHERE id=$1`, systemHistoryID)
			englishMigrationString(t, tx, fmt.Sprint(legacyID), `SELECT current_prompt_id::text FROM agents WHERE id=$1`, agentID)
			for _, note := range []string{legacyResetPromptNote, "Built-in default", "Restored built-in default"} {
				id := englishMigrationPrompt(t, tx, agentID, legacy, note, "system", true)
				if err := migrateLegacyEnglishDefaults(t.Context(), tx, defaults); err != nil {
					t.Fatal(err)
				}
				englishMigrationString(t, tx, english, `SELECT template_text FROM agent_prompts WHERE id=$1`, id)
			}

			for _, fixture := range []struct{ name, text, note, author string }{
				{"customized-default", legacy + "\nOperator change\t", legacyDefaultPromptNote, "system"},
				{"user-authored-match", legacy, legacyDefaultPromptNote, "user"},
				{"user-note", legacy, "Operator's own prompt", "system"},
			} {
				t.Run(fixture.name, func(t *testing.T) {
					id := englishMigrationPrompt(t, tx, agentID, fixture.text, fixture.note, fixture.author, true)
					if err := migrateLegacyEnglishDefaults(t.Context(), tx, defaults); err != nil {
						t.Fatal(err)
					}
					englishMigrationString(t, tx, fixture.text, `SELECT template_text FROM agent_prompts WHERE id=$1`, id)
					englishMigrationString(t, tx, fixture.note, `SELECT note FROM agent_prompts WHERE id=$1`, id)
					englishMigrationString(t, tx, fixture.author, `SELECT updated_by FROM agent_prompts WHERE id=$1`, id)
				})
			}
		})
	}
}

func TestMigrateBuiltinEnglishTextSavedWrapupsAndTaskTimeouts(t *testing.T) {
	d := englishMigrationDB(t)
	for _, tc := range []struct{ key, column string }{
		{"planner", "wrapup_prompt"}, {"mainagent", "wrapup_prompt"}, {"worker", "wrapup_prompt"},
		{"worker", "task_timeout_wrapup_prompt"}, {"planner", "task_timeout_wrapup_prompt"},
	} {
		t.Run(tc.key+"/"+tc.column, func(t *testing.T) {
			tx := englishMigrationTx(t, d)
			id := englishMigrationAgent(t, tx, tc.key, "Operator label", "Operator description", true)
			legacy := "\u65e7\u6536\u5c3e\n" + tc.key + "/" + tc.column
			english := "English wrap-up\n" + tc.key + "/" + tc.column
			replacements := map[string]string{englishMigrationHash(legacy): english}
			defaults := LegacyEnglishDefaults{}
			if tc.column == "wrapup_prompt" {
				defaults.WrapupBySHA256 = replacements
			} else {
				defaults.TaskTimeoutBySHA256 = replacements
			}
			query := "SELECT " + tc.column + " FROM agents WHERE id=$1"
			set := "UPDATE agents SET " + tc.column + "=$1,wrapup_max_turns=7,task_timeout_wrapup_max_turns=8 WHERE id=$2"
			englishMigrationExec(t, tx, set, legacy, id)
			for run := 1; run <= 2; run++ {
				if err := migrateLegacyEnglishDefaults(t.Context(), tx, defaults); err != nil {
					t.Fatalf("migration run %d: %v", run, err)
				}
			}
			englishMigrationString(t, tx, english, query, id)
			englishMigrationString(t, tx, "7|8", `SELECT concat_ws('|',wrapup_max_turns,task_timeout_wrapup_max_turns) FROM agents WHERE id=$1`, id)

			customID := englishMigrationAgent(t, tx, "english_migration_user", "User", "User", false)
			englishMigrationExec(t, tx, set, legacy, customID)
			for _, custom := range []string{"", legacy + "\nOperator change\t"} {
				englishMigrationExec(t, tx, set, custom, id)
				if err := migrateLegacyEnglishDefaults(t.Context(), tx, defaults); err != nil {
					t.Fatal(err)
				}
				englishMigrationString(t, tx, custom, query, id)
				englishMigrationString(t, tx, legacy, query, customID)
			}
		})
	}
}

func TestMigrateBuiltinEnglishTextJudgeSetting(t *testing.T) {
	d := englishMigrationDB(t)
	tx := englishMigrationTx(t, d)
	const legacy = "\u65e7\u62e6\u622a\u7b56\u7565\n  {{.ToolInput}}\t"
	const english = "English interception policy\n  {{.ToolInput}}\t"
	defaults := LegacyEnglishDefaults{JudgeBySHA256: map[string]string{englishMigrationHash(legacy): english}}
	englishMigrationExec(t, tx, `INSERT INTO settings(key,value) VALUES('llm_judge_prompt',$1) ON CONFLICT(key) DO UPDATE SET value=$1`, legacy)
	englishMigrationExec(t, tx, `INSERT INTO settings(key,value) VALUES('english_migration_user_setting',$1) ON CONFLICT(key) DO UPDATE SET value=$1`, legacy)
	for run := 1; run <= 2; run++ {
		if err := migrateLegacyEnglishDefaults(t.Context(), tx, defaults); err != nil {
			t.Fatalf("migration run %d: %v", run, err)
		}
	}
	englishMigrationString(t, tx, english, `SELECT value FROM settings WHERE key='llm_judge_prompt'`)
	englishMigrationString(t, tx, legacy, `SELECT value FROM settings WHERE key='english_migration_user_setting'`)
	for _, custom := range []string{"", legacy + "\nOperator policy\t"} {
		englishMigrationExec(t, tx, `UPDATE settings SET value=$1 WHERE key='llm_judge_prompt'`, custom)
		if err := migrateLegacyEnglishDefaults(t.Context(), tx, defaults); err != nil {
			t.Fatal(err)
		}
		englishMigrationString(t, tx, custom, `SELECT value FROM settings WHERE key='llm_judge_prompt'`)
	}
	englishMigrationExec(t, tx, `DELETE FROM settings WHERE key='llm_judge_prompt'`)
	if err := migrateLegacyEnglishDefaults(t.Context(), tx, defaults); err != nil {
		t.Fatal(err)
	}
	englishMigrationString(t, tx, "0", `SELECT count(*)::text FROM settings WHERE key='llm_judge_prompt'`)
}

func TestMigrateBuiltinEnglishTextSavedGenericWrapups(t *testing.T) {
	d := englishMigrationDB(t)
	const english = "Persist unsaved results, then summarize your actions and conclusions."
	defaults := NewLegacyEnglishDefaults(map[string]string{"wrapup:generic": english})
	for _, tc := range []struct {
		key              string
		builtin, upgrade bool
	}{
		{"goals", true, true}, {"auto", true, true}, {"pentest", true, true},
		{"goals", false, false}, {"auto", false, false}, {"pentest", false, false},
		{"english_migration_custom", false, false},
		{"english_migration_unrelated_builtin", true, false},
		{"reporter", false, false}, {"retester", false, false},
	} {
		t.Run(fmt.Sprintf("%s/builtin=%t", tc.key, tc.builtin), func(t *testing.T) {
			tx := englishMigrationTx(t, d)
			id := englishMigrationAgent(t, tx, tc.key, "Operator label", "Operator description", tc.builtin)
			englishMigrationExec(t, tx, `UPDATE agents SET wrapup_prompt=$1,wrapup_max_turns=7 WHERE id=$2`, legacyGenericWrapup, id)
			for run := 1; run <= 2; run++ {
				if err := migrateLegacyEnglishDefaults(t.Context(), tx, defaults); err != nil {
					t.Fatalf("migration run %d: %v", run, err)
				}
			}
			want := legacyGenericWrapup
			if tc.upgrade {
				want = english
			}
			englishMigrationString(t, tx, want, `SELECT wrapup_prompt FROM agents WHERE id=$1`, id)
			englishMigrationString(t, tx, "7", `SELECT wrapup_max_turns::text FROM agents WHERE id=$1`, id)
			for _, custom := range []string{"", legacyGenericWrapup + "\nOperator change\t", " " + legacyGenericWrapup} {
				englishMigrationExec(t, tx, `UPDATE agents SET wrapup_prompt=$1 WHERE id=$2`, custom, id)
				if err := migrateLegacyEnglishDefaults(t.Context(), tx, defaults); err != nil {
					t.Fatal(err)
				}
				englishMigrationString(t, tx, custom, `SELECT wrapup_prompt FROM agents WHERE id=$1`, id)
			}
		})
	}
}
