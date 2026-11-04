package agent

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"strings"
	"testing"

	"github.com/hypnguyen1209/norma/agentcore"
	"github.com/hypnguyen1209/norma/harness"
	"github.com/hypnguyen1209/norma/llm"
	acperm "github.com/hypnguyen1209/norma/permission"
	actool "github.com/hypnguyen1209/norma/tool"
)

type toolAliasFakeProvider struct {
	complete func(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error)
	stream   func(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error]
}

func (p *toolAliasFakeProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	if p.complete == nil {
		return llm.Message{}, "", llm.Usage{}, errors.New("unexpected Complete call")
	}
	return p.complete(ctx, req)
}

func (p *toolAliasFakeProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	if p.stream == nil {
		return func(func(llm.StreamEvent, error) bool) {}
	}
	return p.stream(ctx, req)
}

func TestToolAliasNames(t *testing.T) {
	tests := []struct {
		name string
		want []string
	}{
		{name: "WebFetch", want: []string{"webfetch", "web_fetch", "WEBFETCH"}},
		{name: "MultiEdit", want: []string{"multiedit", "multi_edit", "MULTIEDIT"}},
		{name: "LS", want: []string{"ls"}},
		{name: "sleep", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toolAliasNames(tt.name); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("toolAliasNames(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestToolAliasProviderComplete(t *testing.T) {
	wantTools := []llm.ToolSchema{{Name: "WebFetch", Description: "runtime SDK tool"}}
	wantUsage := llm.Usage{InputTokens: 7, OutputTokens: 3}
	var gotReq llm.CompletionRequest
	inner := &toolAliasFakeProvider{complete: func(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		gotReq = req
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			llm.TextBlock("calling"),
			{Type: llm.BlockToolUse, ID: "call-1", Name: "web_fetch", Input: json.RawMessage(`{}`)},
		}}, "tool_use", wantUsage, nil
	}}

	msg, stop, usage, err := normalizeToolNames(inner).Complete(t.Context(), llm.CompletionRequest{Tools: wantTools})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotReq.Tools, wantTools) {
		t.Fatalf("underlying tools = %#v, want canonical-only %#v", gotReq.Tools, wantTools)
	}
	if got := msg.Content[1].Name; got != "WebFetch" {
		t.Fatalf("tool name = %q, want WebFetch", got)
	}
	if stop != "tool_use" || usage != wantUsage || msg.Content[0].Text != "calling" {
		t.Fatalf("non-name response fields changed: msg=%+v stop=%q usage=%+v", msg, stop, usage)
	}
}

func TestToolAliasProviderCompletePreservesOtherRolesAndErrors(t *testing.T) {
	wantErr := errors.New("completion failed")
	for _, role := range []llm.Role{llm.RoleAssistant, llm.RoleUser} {
		t.Run(string(role), func(t *testing.T) {
			inner := &toolAliasFakeProvider{complete: func(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
				return llm.Message{Role: role, Content: []llm.ContentBlock{
					{Type: llm.BlockToolUse, Name: "web_fetch"},
					{Type: llm.BlockText, Name: "web_fetch", Text: "unchanged"},
				}}, "failed", llm.Usage{InputTokens: 2}, wantErr
			}}
			msg, stop, usage, err := normalizeToolNames(inner).Complete(t.Context(), llm.CompletionRequest{
				Tools: []llm.ToolSchema{{Name: "WebFetch"}},
			})
			wantName := "web_fetch"
			if role == llm.RoleAssistant {
				wantName = "WebFetch"
			}
			if msg.Role != role || msg.Content[0].Name != wantName || msg.Content[1].Name != "web_fetch" || msg.Content[1].Text != "unchanged" {
				t.Fatalf("unexpected response normalization: %+v", msg)
			}
			if err != wantErr || stop != "failed" || usage.InputTokens != 2 {
				t.Fatalf("response metadata changed: stop=%q usage=%+v err=%v", stop, usage, err)
			}
		})
	}
}

func TestToolAliasProviderStream(t *testing.T) {
	wantErr := errors.New("stream failed")
	wantTools := []llm.ToolSchema{{Name: "MultiEdit", Description: "canonical schema"}}
	start := llm.StreamEvent{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 5}}
	text := llm.StreamEvent{Type: llm.SETextDelta, Text: "unchanged", ToolName: "multi_edit"}
	inner := &toolAliasFakeProvider{stream: func(_ context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
		if !reflect.DeepEqual(req.Tools, wantTools) {
			t.Errorf("stream schemas changed: got %+v, want %+v", req.Tools, wantTools)
		}
		return func(yield func(llm.StreamEvent, error) bool) {
			if !yield(start, nil) {
				return
			}
			if !yield(llm.StreamEvent{Type: llm.SEToolUseStart, ToolID: "call-1", ToolName: "multi_edit"}, nil) {
				return
			}
			if !yield(text, nil) {
				return
			}
			yield(llm.StreamEvent{}, wantErr)
		}
	}}

	var events []llm.StreamEvent
	var gotErr error
	for event, err := range normalizeToolNames(inner).Stream(t.Context(), llm.CompletionRequest{
		Tools: wantTools,
	}) {
		events = append(events, event)
		if err != nil {
			gotErr = err
		}
	}
	wantEvents := []llm.StreamEvent{start, {Type: llm.SEToolUseStart, ToolID: "call-1", ToolName: "MultiEdit"}, text, {}}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("stream events changed: %+v", events)
	}
	if !errors.Is(gotErr, wantErr) {
		t.Fatalf("stream error = %v, want %v", gotErr, wantErr)
	}

	finished := false
	emitted := 0
	inner.stream = func(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
		return func(yield func(llm.StreamEvent, error) bool) {
			for range 3 {
				emitted++
				if !yield(text, nil) {
					return
				}
			}
			finished = true
		}
	}
	normalizeToolNames(inner).Stream(t.Context(), llm.CompletionRequest{})(func(llm.StreamEvent, error) bool { return false })
	if emitted != 1 || finished {
		t.Fatalf("consumer stop was not propagated: emitted=%d finished=%v", emitted, finished)
	}
}

func TestToolAliasProviderAvoidsCanonicalAndAmbiguousCollisions(t *testing.T) {
	inner := &toolAliasFakeProvider{complete: func(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			{Type: llm.BlockToolUse, Name: "web_fetch"},
			{Type: llm.BlockToolUse, Name: "webfetch"},
			{Type: llm.BlockToolUse, Name: "WEBFETCH"},
		}}, "tool_use", llm.Usage{}, nil
	}}
	for _, tools := range [][]llm.ToolSchema{
		{{Name: "WebFetch"}, {Name: "Webfetch"}, {Name: "web_fetch"}},
		{{Name: "web_fetch"}, {Name: "Webfetch"}, {Name: "WebFetch"}},
	} {
		msg, _, _, err := normalizeToolNames(inner).Complete(t.Context(), llm.CompletionRequest{Tools: tools})
		if err != nil {
			t.Fatal(err)
		}
		for i, want := range []string{"web_fetch", "webfetch", "WEBFETCH"} {
			if got := msg.Content[i].Name; got != want {
				t.Errorf("content[%d] name = %q, want collision left as %q", i, got, want)
			}
		}
	}
}

func TestToolAliasProviderBuildsMapFromEachRequest(t *testing.T) {
	name := "multi_edit"
	inner := &toolAliasFakeProvider{complete: func(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{
			Type: llm.BlockToolUse, Name: name,
		}}}, "tool_use", llm.Usage{}, nil
	}}
	provider := normalizeToolNames(inner)

	for _, tc := range []struct {
		tools []llm.ToolSchema
		name  string
		want  string
	}{
		{[]llm.ToolSchema{{Name: "MultiEdit"}}, "multi_edit", "MultiEdit"},
		{[]llm.ToolSchema{{Name: "WebFetch"}}, "web_fetch", "WebFetch"},
		{[]llm.ToolSchema{{Name: "MultiEdit"}}, "web_fetch", "web_fetch"},
		{nil, "multi_edit", "multi_edit"},
	} {
		name = tc.name
		msg, _, _, err := provider.Complete(t.Context(), llm.CompletionRequest{Tools: tc.tools})
		if err != nil {
			t.Fatal(err)
		}
		if got := msg.Content[0].Name; got != tc.want {
			t.Errorf("request tools %v normalized %q to %q, want %q", tc.tools, name, got, tc.want)
		}
	}
}

func TestToolAliasProviderSeesSDKAddedWebFetch(t *testing.T) {
	calls := 0
	inner := &toolAliasFakeProvider{complete: func(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		calls++
		if len(req.Tools) != 1 || req.Tools[0].Name != "WebFetch" {
			t.Errorf("SDK-added canonical schema missing: %+v", req.Tools)
		}
		if calls == 1 {
			// Missing URL is rejected by schema validation, so no network request runs.
			return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{
				Type: llm.BlockToolUse, ID: "sdk-fetch", Name: "web_fetch", Input: json.RawMessage(`{}`),
			}}}, "tool_use", llm.Usage{}, nil
		}
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("done")}}, "end_turn", llm.Usage{}, nil
	}}
	session := agentcore.NewSession(agentcore.Options{
		Provider: normalizeToolNames(inner), EnableWebFetch: true,
		Tools:        []actool.CoreTool{},
		NonStreaming: true, DisableBackgroundTasks: true,
	})
	defer session.Close()
	var result *llm.ContentBlock
	for event, err := range session.Prompt(t.Context(), "fetch") {
		if err != nil {
			t.Fatal(err)
		}
		if event.Kind == harness.KindToolResult {
			result = event.ToolResult
		}
	}
	if calls != 2 || result == nil || !result.IsError || len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "invalid tool input") {
		t.Fatalf("alias did not resolve to SDK WebFetch: calls=%d result=%+v", calls, result)
	}
}

func TestToolAliasProviderUsesNormalPermissionPath(t *testing.T) {
	oldAugment, oldResolve := ToolAugment, ToolResolve
	t.Cleanup(func() { ToolAugment, ToolResolve = oldAugment, oldResolve })
	ToolAugment, ToolResolve = nil, nil
	checked, ran := false, false
	canonical := actool.Build(actool.Spec{
		Name:        "WebFetch",
		Description: "hard-denied test tool",
		Schema:      map[string]any{"type": "object"},
		Permissions: func(context.Context, json.RawMessage, acperm.Context) acperm.Decision {
			checked = true
			return acperm.Denied("blocked by tool")
		},
		Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			ran = true
			return actool.Text("unsafe"), nil
		},
	})
	tools, def, cleanup := AugmentTools(t.Context(), "worker", []actool.CoreTool{canonical})
	defer cleanup()
	if len(def.Deferred) != 0 || len(tools) != 1 || tools[0].Name() != "WebFetch" {
		t.Fatalf("aliases leaked into registry/deferred tools: tools=%v deferred=%v", toolNames(tools), def.Deferred)
	}

	var requests []llm.CompletionRequest
	calls := 0
	inner := &toolAliasFakeProvider{complete: func(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		requests = append(requests, req)
		calls++
		if calls == 1 {
			return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{
				Type: llm.BlockToolUse, ID: "call-1", Name: "web_fetch", Input: json.RawMessage(`{}`),
			}}}, "tool_use", llm.Usage{}, nil
		}
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("done")}}, "end_turn", llm.Usage{}, nil
	}}
	session := agentcore.NewSession(agentcore.Options{
		Provider:               normalizeToolNames(inner),
		Tools:                  tools,
		PermissionMode:         acperm.ModeBypass,
		NonStreaming:           true,
		DisableBackgroundTasks: true,
	})
	defer session.Close()

	var result *llm.ContentBlock
	for event, err := range session.Prompt(t.Context(), "call it") {
		if err != nil {
			t.Fatal(err)
		}
		if event.Kind == harness.KindToolResult {
			result = event.ToolResult
		}
	}
	if !checked || ran {
		t.Fatalf("canonical permission path checked=%v handler ran=%v", checked, ran)
	}
	if result == nil || !result.IsError || !strings.Contains(result.Content[0].Text, "not permitted") {
		t.Fatalf("permission denial missing from tool result: %+v", result)
	}
	if len(requests) == 0 || len(requests[0].Tools) != 1 || requests[0].Tools[0].Name != "WebFetch" {
		t.Fatalf("provider schemas were not canonical-only: %+v", requests)
	}
	for _, schema := range requests[0].Tools {
		if schema.Name == actool.ExecuteExtraToolName || schema.Name == actool.SearchExtraToolsName {
			t.Fatalf("compatibility aliases enabled deferred executor: %+v", requests[0].Tools)
		}
	}
}

func TestConfigNewProviderWrapsToolAliasNormalizerAndPreservesErrors(t *testing.T) {
	provider, err := (Config{Format: llm.FormatOpenAI, APIKey: "test"}).NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := provider.(*toolAliasProvider); !ok {
		t.Fatalf("NewProvider returned %T, want *toolAliasProvider", provider)
	}

	provider, err = (Config{Format: llm.Format("invalid"), APIKey: "test"}).NewProvider()
	if err == nil || provider != nil || !strings.Contains(err.Error(), "unknown format") {
		t.Fatalf("provider construction error not preserved: provider=%T err=%v", provider, err)
	}
}

func toolNames(tools []actool.CoreTool) []string {
	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.Name()
	}
	return names
}
