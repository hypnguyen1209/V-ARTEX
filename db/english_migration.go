package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type englishMigrationStore interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

const legacyBuiltinPromptNote = "\u5185\u7f6e\u9ed8\u8ba4"
const legacyRestoredPromptNote = "\u6062\u590d\u4e3a\u5185\u7f6e\u9ed8\u8ba4"

// Frozen seed values are paired with stable identities; operator edits never match.
var englishAgentLabels = []struct {
	key, legacyName, name, legacyDescription, description string
	builtin                                               bool
}{
	{"goals", "\u76ee\u6807\u62c6\u89e3", "Goal decomposition", "\u628a\u6e17\u900f\u4efb\u52a1\u76ee\u6807\u62c6\u89e3\u6210\u82e5\u5e72\u72ec\u7acb、\u53ef\u9a8c\u8bc1\u7684\u5b50\u76ee\u6807。", "Break penetration testing objectives into independent, verifiable subgoals.", true},
	{"planner", "\u89c4\u5212", "Planner", "\u8bfb\u53d6\u6001\u52bf、\u5224\u5b9a\u76ee\u6807，\u53ea\u5728\u786e\u6709\u672a\u8986\u76d6\u7684\u65b0\u65b9\u5411\u65f6\u8865\u5145\u63a2\u7d22\u610f\u56fe（\u6bcf\u4efb\u52a1\u4e00\u4e2a\u89c4\u5212\u5faa\u73af）。", "Assess the current situation and objectives, and add exploration intents only for new, uncovered directions (one planning loop per task).", true},
	{"mainagent", "\u4e3b", "Main", "\u4eba\u673a\u63a5\u53e3：\u89c2\u5bdf\u8fdb\u5c55，\u628a\u4eba\u7684\u610f\u56fe\u843d\u6210 hint \u6216\u9ad8\u4f18\u5148\u7ea7\u610f\u56fe。", "Human interface: observe progress and turn user requests into hints or high-priority intents.", true},
	{"worker", "\u6267\u884c", "Worker", "\u9886\u53d6\u4e00\u6761\u610f\u56fe\u6267\u884c，\u628a\u53d1\u73b0\u7684\u4e8b\u5b9e/\u6f0f\u6d1e\u5199\u56de\u77e5\u8bc6\u56fe\u8c31\u540e\u505c\u6b62。", "Claim and execute one intent, record discovered facts and vulnerabilities in the knowledge graph, then stop.", true},
	{"auto", "Auto", "Auto", "\u5e73\u53f0\u64cd\u4f5c\u52a9\u624b：\u7528\u5de5\u5177\u7ba1\u7406\u4efb\u52a1(\u5efa/\u770b/\u6682\u505c/\u7ed9\u63d0\u793a)\u4e0e\u8d44\u4ea7，\u5e76\u53ef\u521b\u5efa/\u4fee\u6539 skill、\u81ea\u5b9a\u4e49\u5de5\u5177、MCP。", "Platform assistant: use tools to manage tasks (create/view/pause/hint) and assets, and create or update skills, custom tools, and MCP servers.", true},
	{"pentest", "\u6e17\u900f\u6d4b\u8bd5", "Penetration testing", "\u72ec\u7acb\u6e17\u900f agent：\u4e00\u4eba\u4ece\u4fa6\u5bdf→\u627e\u653b\u51fb\u9762→\u6df1\u5165\u5229\u7528→\u9a8c\u8bc1→\u6536\u5c3e\u8d70\u5b8c\u6574\u6761\u94fe，\u81ea\u5df1\u89c4\u5212、\u81ea\u5df1\u6267\u884c、\u81ea\u5df1\u5bf9\u6297\u5f0f\u9a8c\u8bc1。", "Standalone penetration testing agent: handle reconnaissance, attack surface discovery, exploitation, verification, and wrap-up with independent planning, execution, and adversarial verification.", true},
	{"reporter", "\u62a5\u544a\u64b0\u5199", "Report writer", "\u6f0f\u6d1e\u8be6\u7ec6\u62a5\u544a\u64b0\u5199：\u53d1\u73b0\u6f0f\u6d1e\u65f6\u81ea\u52a8\u89e6\u53d1，\u67e5\u53d6\u8bc1\u636e\u4e0e\u6267\u884c\u8fc7\u7a0b\u540e\u5199 Markdown \u62a5\u544a\u5e76\u56de\u5199。", "Write detailed vulnerability reports: automatically triggered by findings, review evidence and execution traces, then write and save a Markdown report.", false},
	{"retester", "\u6f0f\u6d1e\u590d\u6d4b", "Vulnerability retest", "\u4ece\u6f0f\u6d1e\u8be6\u60c5\u624b\u52a8\u542f\u52a8，\u8bfb\u53d6\u539f\u8bc1\u636e\u5e76\u4fdd\u5b58\u72ec\u7acb\u590d\u6d4b\u7ed3\u8bba。", "Start manually from vulnerability details, review original evidence, and save an independent retest conclusion.", false},
}

var englishPromptVars = []struct {
	key, name, legacyDescription, description, legacyExample, example, source string
}{
	{"goals", "EngagementDescription", "\u4efb\u52a1\u63cf\u8ff0（\u6d4b\u8bd5\u5bf9\u8c61/\u80cc\u666f）", "Task description (target/background)", "\u6d4b\u8bd5 example.com \u7ad9\u70b9", "Test the example.com site", "exploration"},
	{"planner", "Goal", "\u4efb\u52a1\u603b\u76ee\u6807", "Overall task objective", "\u62ff\u4e0b example.com \u7684\u7ba1\u7406\u5458\u6743\u9650", "Obtain administrator access to example.com", "exploration"},
	{"planner", "AssetSummary", "\u8d44\u4ea7\u8ba1\u6570/\u7c7b\u578b\u5206\u5e03\u6458\u8981(\u53ef\u9009)", "Asset counts and type distribution summary (optional)", "domain:3 ip:5 site:2", "domain:3 ip:5 site:2", "distilled"},
	{"mainagent", "Goal", "\u5f53\u524d\u4efb\u52a1\u76ee\u6807", "Current task objective", "\u62ff\u4e0b example.com \u7684\u7ba1\u7406\u5458\u6743\u9650", "Obtain administrator access to example.com", "exploration"},
	{"mainagent", "AssetSummary", "\u5f00\u5c40\u6001\u52bf\u6458\u8981(\u53ef\u9009)", "Initial situation summary (optional)", "domain:3 ip:5", "domain:3 ip:5", "distilled"},
	{"mainagent", "FindingsSummary", "\u5df2\u786e\u8ba4\u6f0f\u6d1e\u6458\u8981(\u53ef\u9009)", "Confirmed vulnerability summary (optional)", "high:1 medium:2", "high:1 medium:2", "distilled"},
	{"worker", "ProxyAddr", "\u8bb0\u5f55\u4ee3\u7406\u5730\u5740(\u9a71\u52a8 if \u53cc\u6587\u6848)", "Recording proxy address (controls conditional prompt text)", "127.0.0.1:8080", "127.0.0.1:8080", "runtime"},
	{"worker", "WorkerName", "worker \u81ea\u6211\u6807\u8bc6(\u53ef\u9009)", "Worker self-identification (optional)", "worker-1", "worker-1", "runtime"},
}

var englishAssetNotes = []struct{ pattern, legacy, english string }{
	{".gov", "[\u5185\u7f6e] \u653f\u5e9c\u7f51\u7ad9 (.gov)", "[Built-in] Government website (.gov)"},
	{".gov.cn", "[\u5185\u7f6e] \u653f\u5e9c\u7f51\u7ad9 (.gov.cn)", "[Built-in] Government website (.gov.cn)"},
	{".edu", "[\u5185\u7f6e] \u6559\u80b2\u7f51\u7ad9 (.edu)", "[Built-in] Educational website (.edu)"},
	{".edu.cn", "[\u5185\u7f6e] \u6559\u80b2\u7f51\u7ad9 (.edu.cn)", "[Built-in] Educational website (.edu.cn)"},
}

var englishInterceptRules = []struct {
	pattern, legacyName, name, legacyMessage, message string
}{
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
}

// migrateBuiltinEnglishText uses a transaction when called on Open's locked connection.
// Tests and other transactional callers can supply their existing transaction.
func migrateBuiltinEnglishText(ctx context.Context, store englishMigrationStore) error {
	if conn, ok := store.(*sql.Conn); ok {
		return withEnglishMigrationTransaction(ctx, conn.BeginTx, func(tx *sql.Tx) error {
			return migrateBuiltinEnglishText(ctx, tx)
		})
	}
	for _, a := range englishAgentLabels {
		if _, err := store.ExecContext(ctx, `UPDATE agents SET
name=CASE WHEN name=$3 THEN $4 ELSE name END,
description=CASE WHEN description=$5 THEN $6 ELSE description END
WHERE key=$1 AND builtin=$2 AND (name=$3 OR description=$5)`,
			a.key, a.builtin, a.legacyName, a.name, a.legacyDescription, a.description); err != nil {
			return fmt.Errorf("agent %s: %w", a.key, err)
		}
	}
	for _, v := range englishPromptVars {
		if _, err := store.ExecContext(ctx, `UPDATE agent_prompt_vars v SET
description=CASE WHEN v.description=$3 THEN $4 ELSE v.description END,
example=CASE WHEN v.example=$5 THEN $6 ELSE v.example END
FROM agents a WHERE a.id=v.agent_id AND a.builtin AND a.key=$1 AND v.var_name=$2
AND (v.description=$3 OR v.example=$5)`,
			v.key, v.name, v.legacyDescription, v.description, v.legacyExample, v.example); err != nil {
			return fmt.Errorf("agent %s variable %s: %w", v.key, v.name, err)
		}
	}
	for _, r := range englishInterceptRules {
		if _, err := store.ExecContext(ctx, `UPDATE intercept_rules SET
name=CASE WHEN name=$2 THEN $3 ELSE name END,
message=CASE WHEN message=$4 THEN $5 ELSE message END
WHERE match_target='tool_input' AND match_type='regex' AND pattern=$1
AND (name=$2 OR message=$4)`, r.pattern, r.legacyName, r.name, r.legacyMessage, r.message); err != nil {
			return fmt.Errorf("intercept rule %s: %w", r.name, err)
		}
	}
	for _, r := range englishAssetNotes {
		if _, err := store.ExecContext(ctx, `UPDATE asset_intercept_rules SET note=$3
WHERE builtin AND kind='fuzzy_domain' AND pattern=$1 AND note=$2`, r.pattern, r.legacy, r.english); err != nil {
			return fmt.Errorf("asset intercept rule %s: %w", r.pattern, err)
		}
	}
	_, err := store.ExecContext(ctx, `UPDATE agent_prompts p SET
note=CASE WHEN note=$1 THEN 'Built-in default' ELSE 'Restored built-in default' END
FROM agents a WHERE a.current_prompt_id=p.id AND a.id=p.agent_id
AND p.updated_by='system' AND p.note IN ($1,$2)`, legacyBuiltinPromptNote, legacyRestoredPromptNote)
	return err
}

// LegacyEnglishDefaults supplies current English replacements without coupling db
// to the packages that own model-facing prompts. Keys are frozen legacy SHA-256s.
type LegacyEnglishDefaults struct {
	PromptBySHA256      map[string]string
	WrapupBySHA256      map[string]string
	TaskTimeoutBySHA256 map[string]string
	JudgeBySHA256       map[string]string
}

// NewLegacyEnglishDefaults binds approved legacy fingerprints to current text.
// Missing or blank replacements are omitted so incomplete input cannot erase data.
func NewLegacyEnglishDefaults(currentByKey map[string]string) LegacyEnglishDefaults {
	defaults := LegacyEnglishDefaults{
		PromptBySHA256:      map[string]string{},
		WrapupBySHA256:      map[string]string{},
		TaskTimeoutBySHA256: map[string]string{},
		JudgeBySHA256:       map[string]string{},
	}
	for _, group := range []struct {
		target map[string]string
		hashes map[string]string
	}{
		{defaults.PromptBySHA256, map[string]string{
			"builtin:auto":      "b2a71b82243607337b19f0642317fed88c144d536412d80eaaf90807f2905a1d",
			"builtin:goals":     "72a7a0159443b41dde29dfba8a3de6a2c8a15b273935495a7b88561fa708aa68",
			"builtin:mainagent": "12bb69ee0886c4fb9beb936d882021a91104860f1da69a5e3eda3d9e345cd399",
			"builtin:pentest":   "a54bd5713b1c121d0f2ff3a72e0304820b1368c4b4b8159d5d651895663ac652",
			"builtin:planner":   "14e163f4a44da9fee20a214048a607adc3659b2025275569eb512532ca8c15d5",
			"builtin:worker":    "80fbb1c610dd57e266c20b9145793339eee540b809f6db8f5336261b45879f08",
			"assistant":         "a179c990bff1ab4e0a6f326945a1c44036ef4c8a54c7c5b635965f6cefdc9f0a",
			"reporter":          "8ba98208f6c5f2ccb103c88ef13239c4264733400c0e3dfec96d3a3572d78d39",
			"retester":          "d08326aa9fbd9a1367e716cf7ef46533e9096e9a56493f77d364efbf696f7fe0",
		}},
		{defaults.WrapupBySHA256, map[string]string{
			"wrapup:generic":   "9d0b47458ae54a85cad722d7078eacbe40a11e40e6e7aef774bd8610a61a08b1",
			"wrapup:planner":   "0afba34ad25d8dfe87bac7596dcaeb59040049ce5c412bd62c04f5881ad5dcce",
			"wrapup:mainagent": "52333c82468d757ce3818c8c88379f9d1e7488d9bc388d116abc3423fe9529fa",
			"wrapup:worker":    "2613bf7d5d9fdc2f549667355eb747d92f53de2be3756a57361732aed8fd7ebe",
		}},
		{defaults.TaskTimeoutBySHA256, map[string]string{
			"task-timeout:worker":  "bbefbdf2059d09703a0c6c665d4b4734f99c4df270974bb10c795d987b672343",
			"task-timeout:planner": "d002b51c2a5c42f8197714f5b86b11539f85c3451a19e87472e8483ed0adfd76",
		}},
		{defaults.JudgeBySHA256, map[string]string{
			"judge": "d6bf0f5fae9e98f551de5ac73a935a667f1c4eb18b30fd9da2ffb3e851ea2c15",
		}},
	} {
		for key, hash := range group.hashes {
			if text := currentByKey[key]; strings.TrimSpace(text) != "" {
				group.target[hash] = text
			}
		}
	}
	return defaults
}

func withEnglishMigrationTransaction(ctx context.Context,
	begin func(context.Context, *sql.TxOptions) (*sql.Tx, error),
	action func(*sql.Tx) error) (err error) {
	tx, err := begin(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rollback English migration: %w", rollbackErr))
		}
	}()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, schemaMigrationLockKey); err != nil {
		return err
	}
	if err = action(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// MigrateLegacyEnglishDefaults serializes startup migrations with schema changes
// and commits all guarded prompt replacements together.
func (d *DB) MigrateLegacyEnglishDefaults(ctx context.Context, defaults LegacyEnglishDefaults) error {
	return withEnglishMigrationTransaction(ctx, d.BeginTx, func(tx *sql.Tx) error {
		return migrateLegacyEnglishDefaults(ctx, tx, defaults)
	})
}

type englishTextRow struct {
	id   int64
	text string
}

// migrateEnglishTextRows closes the cursor before updates, including on a pinned
// PostgreSQL connection, and guards updates against changes to the original text.
func migrateEnglishTextRows(ctx context.Context, store englishMigrationStore,
	replacements map[string]string, query, update string, args ...any) error {
	if len(replacements) == 0 {
		return nil
	}
	rows, err := store.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	var candidates []englishTextRow
	for rows.Next() {
		var row englishTextRow
		if err := rows.Scan(&row.id, &row.text); err != nil {
			return errors.Join(err, rows.Close())
		}
		candidates = append(candidates, row)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, row := range candidates {
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(row.text)))
		if replacement, ok := replacements[hash]; ok && replacement != "" && replacement != row.text {
			if _, err := store.ExecContext(ctx, update, row.id, row.text, replacement); err != nil {
				return err
			}
		}
	}
	return nil
}

func migrateLegacyEnglishDefaults(ctx context.Context, store englishMigrationStore, defaults LegacyEnglishDefaults) error {
	if err := migrateEnglishTextRows(ctx, store, defaults.PromptBySHA256,
		`SELECT p.id,p.template_text FROM agent_prompts p JOIN agents a
ON a.current_prompt_id=p.id AND a.id=p.agent_id
WHERE p.updated_by='system' AND p.note IN ($1,$2,'Built-in default','Restored built-in default')
FOR UPDATE OF a,p`,
		`UPDATE agent_prompts SET template_text=$3 WHERE id=$1 AND template_text=$2`,
		legacyBuiltinPromptNote, legacyRestoredPromptNote); err != nil {
		return fmt.Errorf("default prompts: %w", err)
	}
	if err := migrateEnglishTextRows(ctx, store, defaults.WrapupBySHA256,
		`SELECT id,wrapup_prompt FROM agents WHERE builtin AND key IN ('planner','mainagent','worker','goals','auto','pentest') FOR UPDATE`,
		`UPDATE agents SET wrapup_prompt=$3 WHERE id=$1 AND wrapup_prompt=$2`); err != nil {
		return fmt.Errorf("saved wrap-up prompts: %w", err)
	}
	if err := migrateEnglishTextRows(ctx, store, defaults.TaskTimeoutBySHA256,
		`SELECT id,task_timeout_wrapup_prompt FROM agents WHERE builtin AND key IN ('planner','worker') FOR UPDATE`,
		`UPDATE agents SET task_timeout_wrapup_prompt=$3 WHERE id=$1 AND task_timeout_wrapup_prompt=$2`); err != nil {
		return fmt.Errorf("saved task timeout prompts: %w", err)
	}
	if err := migrateEnglishTextRows(ctx, store, defaults.JudgeBySHA256,
		`SELECT 0::bigint,value FROM settings WHERE key='llm_judge_prompt' FOR UPDATE`,
		`UPDATE settings SET value=$3 WHERE key='llm_judge_prompt' AND value=$2 AND $1::bigint=0`); err != nil {
		return fmt.Errorf("judge prompt: %w", err)
	}
	return nil
}
