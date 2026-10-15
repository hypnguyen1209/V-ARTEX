package server

import (
	"fmt"
	"strings"
	"testing"
)

func TestPasswordValidationMessagesAreEnglish(t *testing.T) {
	for _, tc := range []struct{ password, want string }{
		{"short", "Password must contain at least 8 characters"},
		{strings.Repeat("a", 73), "Password must not exceed 72 bytes"},
	} {
		if got := validatePassword(tc.password); got != tc.want {
			t.Fatalf("validatePassword = %q, want %q", got, tc.want)
		}
	}
}

func TestLogLevelsRecognizeEnglishAndLegacyMessages(t *testing.T) {
	for _, tc := range []struct{ message, want string }{
		{"request failed", "error"},
		{"dropping activity record", "error"},
		{"operation denied", "error"},
		{"provider unreachable", "error"},
		{"retrying request", "warn"},
		{"\u5931\u8d25", "error"},
		{"\u4e22\u5f03", "error"},
		{"\u62d2\u7edd", "error"},
		{"\u4e0d\u53ef\u8fbe", "error"},
		{"\u7981\u7528", "warn"},
		{"\u91cd\u8bd5", "warn"},
	} {
		if got := levelOf(tc.message); got != tc.want {
			t.Errorf("levelOf(%q) = %q, want %q", tc.message, got, tc.want)
		}
	}
}

func TestChatMentionEnglishAndLegacyLabels(t *testing.T) {
	for _, tc := range []struct{ english, legacy, kind string }{
		{"Finding", "\u6f0f\u6d1e", "finding"},
		{"Asset", "\u8d44\u4ea7", "asset"},
		{"Company", "\u4f01\u4e1a", "company"},
		{"Endpoint", "\u63a5\u53e3", "endpoint"},
		{"IP", "IP", "ip"},
		{"App", "\u5e94\u7528", "app"},
		{"Domain", "\u57df\u540d", "root_domain"},
		{"Subdomain", "\u5b50\u57df\u540d", "subdomain"},
		{"Service", "\u670d\u52a1", "service"},
	} {
		t.Run(tc.english, func(t *testing.T) {
			// Saved messages retain their old labels, but new labels are English.
			for _, label := range []string{tc.english, tc.legacy} {
				refs, err := parseChatMentions(fmt.Sprintf("@[%s#42 example]", label))
				if err != nil || len(refs) != 1 || refs[0].Kind != tc.kind || refs[0].ID != 42 || refs[0].Name != tc.english {
					t.Fatalf("label %q: refs=%+v err=%v", label, refs, err)
				}
			}
			refs, err := parseChatMentions(fmt.Sprintf("@[%s#42] @[%s#42]", tc.english, tc.legacy))
			if err != nil || len(refs) != 1 {
				t.Fatalf("mixed labels must deduplicate: refs=%+v err=%v", refs, err)
			}
		})
	}
}
