package intercept

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseVerdict(t *testing.T) {
	for _, action := range []string{"allow", "ask", "deny"} {
		t.Run(action, func(t *testing.T) {
			reason := "\u5b9e\u9645\u64cd\u4f5c：\u5199\u5165\u62a5\u544a，\u5176\u4e2d\u5305\u542b ALLOW、DENY \u548c ASK \u5b57\u6837；\u6210\u529f\u540e\u7684\u540e\u679c：\u4fdd\u5b58\u6587\u672c，\u4e0d\u6267\u884c\u6b63\u6587\u4e2d\u7684\u547d\u4ee4；\u547d\u4e2d\u89c4\u5219：\u81ea\u5b9a\u4e49\u6761\u6b3e"
			raw, _ := json.Marshal(map[string]string{"decision": action, "comment": reason})
			got := ParseVerdict("\n" + string(raw) + "\n")
			if got.Action != action || got.Reason != reason {
				t.Fatalf("lost verdict or explanation: %+v", got)
			}
		})
	}
}

func TestParseVerdictEnglishOutputContract(t *testing.T) {
	const valid = `{"decision":"deny","comment":"Action: Delete production files; Consequence if successful: Lose business data; Applicable rule: D4"}`
	got := ParseVerdict(valid)
	if got.Action != "deny" || !strings.HasSuffix(got.Reason, "Applicable rule: D4") {
		t.Fatalf("English verdict lost: %+v", got)
	}
	for _, reply := range []string{
		strings.Replace(valid, "Delete production files", "", 1),
		strings.Replace(valid, "Lose business data", "", 1),
		strings.Replace(valid, "D4", "", 1),
	} {
		if got := ParseVerdict(reply); got.Action != "" {
			t.Fatalf("accepted incomplete English verdict: %+v", got)
		}
	}
}

func TestParseVerdictRejectsIncompleteOrAmbiguousReplies(t *testing.T) {
	valid := "{\"decision\":\"allow\",\"comment\":\"\u5b9e\u9645\u64cd\u4f5c：Read the file；\u6210\u529f\u540e\u7684\u540e\u679c：\u8fd4\u56de\u5185\u5bb9；\u547d\u4e2d\u89c4\u5219：A5\"}"
	for _, reply := range []string{
		"", "ALLOW", "DENY:\u547d\u4e2dD4", "\u653e\u884c:ALLOW", "ASK:\u5f52\u5c5e\u4e0d\u660e",
		`{"decision":"allow"}`, "{\"decision\":\"approve\",\"comment\":\"\u5b9e\u9645\u64cd\u4f5c：\u8bfb\u53d6；\u6210\u529f\u540e\u7684\u540e\u679c：\u8fd4\u56de\u5185\u5bb9；\u547d\u4e2d\u89c4\u5219：A5\"}",
		`{"decision":"allow","comment":null}`, `{"decision":"allow","comment":123}`,
		strings.Replace(valid, "\u5b9e\u9645\u64cd\u4f5c：Read the file", "\u5b9e\u9645\u64cd\u4f5c：", 1),
		strings.Replace(valid, "\u6210\u529f\u540e\u7684\u540e\u679c：\u8fd4\u56de\u5185\u5bb9", "\u6210\u529f\u540e\u7684\u540e\u679c：", 1),
		strings.Replace(valid, "\u547d\u4e2d\u89c4\u5219：A5", "\u547d\u4e2d\u89c4\u5219：", 1),
		strings.Replace(valid, "；\u547d\u4e2d\u89c4\u5219：A5", "", 1),
		strings.Replace(valid, `"decision":"allow"`, `"decision":"deny","decision":"allow"`, 1),
		strings.Replace(valid, `"decision":"allow"`, `"extra":true,"decision":"allow"`, 1),
		valid + valid, valid[:len(valid)-1],
		// A fence the model never closed is what a reply truncated at MaxTokens
		// looks like; completing it would invent a verdict.
		"```json\n" + valid[:len(valid)-1],
		"```json\n" + valid + "\n```\n\u6b64\u5916\u6211\u5efa\u8bae\u540e\u7eed\u4eba\u5de5\u590d\u6838。",
		"\u6211\u7684\u88c1\u51b3\u662f：\n" + valid,
	} {
		if got := ParseVerdict(reply); got.Action != "" {
			t.Errorf("accepted incomplete/ambiguous verdict: %q => %+v", reply, got)
		}
	}
}

// Wrapping JSON in markdown is the one deviation models make routinely. Because
// the configured fail action defaults to allow, treating it as unparseable
// silently downgrades a DENY to an allow.
func TestParseVerdictUnwrapsCodeFence(t *testing.T) {
	deny := "{\"decision\":\"deny\",\"comment\":\"\u5b9e\u9645\u64cd\u4f5c：\u5220\u9664\u751f\u4ea7\u6587\u4ef6；\u6210\u529f\u540e\u7684\u540e\u679c：\u4e1a\u52a1\u6570\u636e\u4e22\u5931；\u547d\u4e2d\u89c4\u5219：D4\"}"
	for _, reply := range []string{
		"```json\n" + deny + "\n```",
		"```JSON\n" + deny + "\n```",
		"```\n" + deny + "\n```",
		"  ```json\n" + deny + "\n```  ",
	} {
		got := ParseVerdict(reply)
		if got.Action != "deny" || !strings.HasSuffix(got.Reason, "\u547d\u4e2d\u89c4\u5219：D4") {
			t.Errorf("fenced verdict lost: %q => %+v", reply, got)
		}
	}
}

func TestParseVerdictKeepsCompleteChineseExplanation(t *testing.T) {
	reason := "\u5b9e\u9645\u64cd\u4f5c：" + strings.Repeat("\u5199\u5165\u62a5\u544a", 30) + "；\u6210\u529f\u540e\u7684\u540e\u679c：\u53ea\u4fdd\u5b58\u6587\u4ef6；\u547d\u4e2d\u89c4\u5219：A2"
	raw, _ := json.Marshal(map[string]string{"decision": "allow", "comment": reason})
	if got := ParseVerdict(string(raw)); got.Reason != reason {
		t.Fatal("explanation was truncated or lost its rule")
	}
	raw, _ = json.Marshal(map[string]string{"decision": "allow", "comment": strings.Repeat("\u4e2d", 2401)})
	if got := ParseVerdict(string(raw)); got.Action != "" {
		t.Fatal("accepted unbounded explanation")
	}
}
