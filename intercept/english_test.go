package intercept

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/hypnguyen1209/V-ARTEX/db"
)

func TestJudgeActionLabelsEnglish(t *testing.T) {
	for action, want := range map[string]string{"allow": "Allowed", "deny": "Blocked", "ask": "Human approval required"} {
		if got := judgeActionLabel(action); got != want {
			t.Errorf("judgeActionLabel(%q) = %q, want %q", action, got, want)
		}
	}
}

func TestJudgeEnglishModelPrefix(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires isolated ARTEX_PG_DSN")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	i := New(d)
	original := i.GetJudgeConfig()
	defer func() {
		if err := i.SetJudgeConfig(original); err != nil {
			t.Error(err)
		}
	}()
	if err := i.SetJudgeConfig(JudgeConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"", "Review complete", "[Model] Review complete"} {
		i.SetReviewer(func(context.Context, int64, string, ReviewInput) (Decision, error) {
			return Decision{Action: "allow", Message: message}, nil
		})
		got, judged := i.Judge(t.Context(), "Read", json.RawMessage(`{"path":"report.md"}`))
		want := "[Model] " + strings.TrimPrefix(message, "[Model] ")
		if message == "" {
			want = "[Model] Allowed"
		}
		if !judged || got.Message != want {
			t.Errorf("Judge message %q = %q (judged=%v), want %q", message, got.Message, judged, want)
		}
	}
}
