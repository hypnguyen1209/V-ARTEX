package server

import (
	"testing"
	"unicode"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/intercept"
)

func TestEnglishMigrationUsesCurrentEnglishDefaults(t *testing.T) {
	defaults := legacyEnglishDefaults()
	for _, group := range []struct {
		name   string
		values map[string]string
		count  int
	}{
		{"prompts", defaults.PromptBySHA256, 9},
		{"wrapups", defaults.WrapupBySHA256, 4},
		{"task-timeouts", defaults.TaskTimeoutBySHA256, 2},
		{"judge", defaults.JudgeBySHA256, 1},
	} {
		if len(group.values) != group.count {
			t.Errorf("%s replacements = %d, want %d", group.name, len(group.values), group.count)
		}
		for hash, text := range group.values {
			if text == "" {
				t.Errorf("%s replacement %s is empty", group.name, hash)
			}
			for _, r := range text {
				if unicode.Is(unicode.Han, r) {
					t.Errorf("%s replacement %s is not English", group.name, hash)
					break
				}
			}
		}
	}
	if got := defaults.PromptBySHA256["a179c990bff1ab4e0a6f326945a1c44036ef4c8a54c7c5b635965f6cefdc9f0a"]; got != agent.DefaultAssistantPrompt {
		t.Error("assistant replacement does not use the current prompt")
	}
	if got := defaults.JudgeBySHA256["d6bf0f5fae9e98f551de5ac73a935a667f1c4eb18b30fd9da2ffb3e851ea2c15"]; got != intercept.DefaultJudgePrompt {
		t.Error("judge replacement does not use the current prompt")
	}
	if got := defaults.WrapupBySHA256["9d0b47458ae54a85cad722d7078eacbe40a11e40e6e7aef774bd8610a61a08b1"]; got != agent.WrapupDefault("goals") {
		t.Error("generic wrap-up replacement does not use the current default")
	}
}
