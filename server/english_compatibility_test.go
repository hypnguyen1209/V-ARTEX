package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hypnguyen1209/V-ARTEX/db"
	"github.com/hypnguyen1209/V-ARTEX/traffic"
	"github.com/Autumn-27/norma/llm"
)

func TestConversationAutoTitleRecognizesEnglishAndLegacyDefaults(t *testing.T) {
	s, _ := newRetestServer(t)
	setRetestProvider(s, retestProvider{complete: func(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("Done")}}, "end_turn", llm.Usage{}, nil
	}})
	for _, title := range []string{"", "New conversation", "\u65b0\u5bf9\u8bdd", "Operator title"} {
		c, err := s.m.pg.CreateConversation("auto", title, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { waitRetestIdle(t, s); _, _ = s.m.pg.Exec("DELETE FROM conversations WHERE id=$1", c.ID) })
		rec := retestRequest(s.pgSendConversationMessage, http.MethodPost, c.ID, `{"message":"Review the captured evidence"}`)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("send: %d %s", rec.Code, rec.Body)
		}
		waitRetestIdle(t, s)
		got, err := s.m.pg.GetConversation(c.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := "Review the captured evidence"
		if title == "Operator title" {
			want = title
		}
		if got == nil || got.Title != want {
			t.Fatalf("original title %q: conversation=%+v, want title %q", title, got, want)
		}
	}
}

func TestLegacyServerTextMigrationsPreserveCustomizations(t *testing.T) {
	if _, _, err := db.DSN(); err != nil {
		t.Skip("test postgres not configured")
	}
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	const searchFlag = "finding_workflow_tools_v3_host_search_description"
	const reporterFlag = "reporter_trigger_evidence_version_v1"
	for _, key := range []string{searchFlag, reporterFlag} {
		old, found, err := pg.GetSetting(key)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if found {
				_ = pg.SetSetting(key, old)
			} else {
				_, _ = pg.Exec("DELETE FROM settings WHERE key=$1", key)
			}
		})
	}
	oldTool, err := pg.GetTool("traffic_search")
	if err != nil || oldTool == nil {
		t.Fatalf("traffic_search: %+v %v", oldTool, err)
	}
	t.Cleanup(func() {
		_, _ = pg.Exec("UPDATE tools SET description=$1 WHERE key='traffic_search'", oldTool.Description)
	})
	const legacySearch = "\u67e5\u8be2\u8bb0\u5f55\u4ee3\u7406\u5df2\u6293\u53d6\u7684\u76ee\u6807\u6d41\u91cf（\u5fc5\u987b\u6307\u5b9a host，\u53ef\u518d\u6309 URL \u5b50\u4e32\u6216\u6b63\u6587\u5173\u952e\u8bcd\u8fc7\u6ee4）。body_contains \u4f1a\u5728\u5df2\u6293\u53d6\u7684\u8bf7\u6c42/\u54cd\u5e94\u5934\u4e0e\u6b63\u6587\u4e2d\u505a\u5168\u6587\u641c\u7d22，\u652f\u6301\u4efb\u610f\u5b50\u4e32\u548c\u4e2d\u6587（\u81f3\u5c11 3 \u4e2a\u5b57\u7b26），\u53ef\u7528\u6765\u627e\u54cd\u5e94\u91cc\u7684\u5bc6\u7801、\u5bc6\u94a5、\u62a5\u9519、\u5185\u7f51\u5730\u5740\u7b49。\u4ec5\u8fd4\u56de\u6781\u8f7b\u91cf\u7d22\u5f15(id/method/url/status/resp_len)，\u4e0d\u542b\u4efb\u4f55\u54cd\u5e94\u5185\u5bb9。\u9ed8\u8ba4\u53ea\u8fd4\u56de 3 \u6761、\u6bcf\u9875\u6700\u591a 10 \u6761；\u7ed3\u679c\u591a\u65f6\u7528 page \u7ffb\u9875（page=0 \u8d77）；\u8981\u770b\u67d0\u6761\u7684\u8bf7\u6c42/\u54cd\u5e94\u539f\u6587\u7528 traffic_get(id)。\u56de\u770b\u5df2\u8bbf\u95ee\u8d44\u6e90、\u627e\u7aef\u70b9\u5148\u7528\u5b83，\u907f\u514d\u91cd\u590d curl \u540c\u4e00 URL。"
	for _, tc := range []struct{ before, want string }{
		{legacySearch, traffic.TrafficSearchDescription},
		{"Operator customized traffic search", "Operator customized traffic search"},
	} {
		if _, err := pg.Exec("UPDATE tools SET description=$1 WHERE key='traffic_search'", tc.before); err != nil {
			t.Fatal(err)
		}
		if err := pg.SetSetting(searchFlag, "false"); err != nil {
			t.Fatal(err)
		}
		s.seedFindingWorkflowTools()
		s.seedFindingWorkflowTools()
		got, err := pg.GetTool("traffic_search")
		if err != nil || got == nil || got.Description != tc.want {
			t.Fatalf("traffic description=%+v err=%v want=%q", got, err, tc.want)
		}
	}
	const legacyTrigger = "\u4e0a\u9762\u521a\u6709\u4e00\u4e2a\u6f0f\u6d1e\u88ab report_finding \u767b\u8bb0。\u8bf7\u4ece\u89e6\u53d1\u4e0a\u4e0b\u6587\u91cc\u53d6\u51fa finding_id（\u5de5\u5177\u8fd4\u56de \"finding recorded: <id>\" \u91cc\u7684\u6570\u5b57）\u4e0e\u4efb\u52a1 id，\u6309\u4f60\u7684\u804c\u8d23\u64b0\u5199\u8be5\u6f0f\u6d1e\u7684\u8be6\u7ec6\u62a5\u544a，\u6700\u540e\u8c03\u7528 update_finding_report(finding_id, report) \u4fdd\u5b58。"
	if reporterToolCallMessageV1 != legacyTrigger {
		t.Fatal("legacy reporter matching bytes changed")
	}
	var ids []int64
	wants := map[int64]string{}
	for _, tc := range []struct{ before, want string }{
		{legacyTrigger, reporterToolCallMessage},
		{"Operator customized reporter trigger", "Operator customized reporter trigger"},
	} {
		tr, err := pg.CreateTrigger(&db.AgentTrigger{AgentKey: "reporter", OnToolCall: true, ToolCallMessage: tc.before, ToolNames: []string{"report_finding"}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, tr.ID)
		wants[tr.ID] = tc.want
	}
	t.Cleanup(func() {
		for _, id := range ids {
			_ = pg.DeleteTrigger(id)
		}
	})
	if err := pg.SetSetting(reporterFlag, "false"); err != nil {
		t.Fatal(err)
	}
	s.upgradeReporterTriggerMessage()
	s.upgradeReporterTriggerMessage()
	triggers, err := pg.ListTriggersFor("reporter")
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range triggers {
		if want, ok := wants[tr.ID]; ok {
			if tr.ToolCallMessage != want {
				t.Fatalf("trigger %d=%q, want %q", tr.ID, tr.ToolCallMessage, want)
			}
			delete(wants, tr.ID)
		}
	}
	if len(wants) != 0 {
		t.Fatalf("missing migrated triggers: %+v", wants)
	}
}

func TestLegacyFallbackCommandsRemainRecognized(t *testing.T) {
	if _, _, err := db.DSN(); err != nil {
		t.Skip("test postgres not configured")
	}
	s, _, _ := trafficEvidenceServer(t)
	task, err := s.m.CreateTask("Fallback command test", "Verify command compatibility", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { id, _ := parseTaskID(task.ID); _ = s.m.pg.DeleteTask(id) })
	for _, prefix := range []string{"intent", "\u610f\u56fe"} {
		if got := s.fallbackChat(task, prefix+" inspect headers"); got != "Injected a high-priority intent: inspect headers" {
			t.Fatalf("intent reply=%q", got)
		}
	}
	for _, prefix := range []string{"hint", "\u63d0\u793a"} {
		if got := s.fallbackChat(task, prefix+" check redirect behavior"); got != "Hint saved for the planner's next round: check redirect behavior" {
			t.Fatalf("hint reply=%q", got)
		}
	}
	intents, err := task.Store.Frontier(10)
	if err != nil || len(intents) != 2 {
		t.Fatalf("intents=%+v err=%v", intents, err)
	}
	for _, intent := range intents {
		var payload map[string]any
		if err := json.Unmarshal(intent.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["summary"] != "inspect headers" {
			t.Fatalf("intent payload=%+v", payload)
		}
	}
	hints, err := task.Store.ListByKind(db.KindHint, 10)
	if err != nil || len(hints) != 2 {
		t.Fatalf("hints=%+v err=%v", hints, err)
	}
	for _, hint := range hints {
		if !strings.Contains(string(hint.Payload), "check redirect behavior") {
			t.Fatalf("hint payload=%s", hint.Payload)
		}
	}
}
