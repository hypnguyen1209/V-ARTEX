package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

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

func TestAddToolAliasesDelegatesAndAvoidsCollisions(t *testing.T) {
	called := false
	canonical := actool.Build(actool.Spec{
		Name: "WebFetch",
		Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			called = true
			return actool.Text("ok"), nil
		},
	})
	existing := actool.Build(actool.Spec{Name: "webfetch"})

	tools, deferred := addToolAliases([]actool.CoreTool{canonical, existing}, []string{"reserved"})
	byName := make(map[string]actool.CoreTool, len(tools))
	for _, tool := range tools {
		byName[tool.Name()] = tool
	}
	if byName["webfetch"] != existing {
		t.Fatal("existing lowercase tool was replaced")
	}
	alias, ok := byName["web_fetch"]
	if !ok {
		t.Fatal("web_fetch alias missing")
	}
	if !containsName(deferred, "web_fetch") {
		t.Fatalf("web_fetch missing from deferred names: %v", deferred)
	}
	if _, err := alias.Call(context.Background(), json.RawMessage(`{}`), nil); err != nil {
		t.Fatalf("alias call: %v", err)
	}
	if !called {
		t.Fatal("alias did not delegate to canonical handler")
	}
}

func TestAugmentToolsAddsHiddenAliases(t *testing.T) {
	oldAugment, oldResolve := ToolAugment, ToolResolve
	t.Cleanup(func() { ToolAugment, ToolResolve = oldAugment, oldResolve })
	ToolAugment, ToolResolve = nil, nil
	canonical := actool.Build(actool.Spec{Name: "WebFetch"})

	tools, def, cleanup := AugmentTools(t.Context(), "worker", []actool.CoreTool{canonical})
	defer cleanup()

	visible := map[string]bool{}
	for _, tool := range tools {
		visible[tool.Name()] = true
	}
	for _, name := range []string{"WebFetch", "webfetch", "web_fetch", "WEBFETCH"} {
		if !visible[name] {
			t.Errorf("tool %q missing from assembled tools", name)
		}
	}
	if containsName(def.Deferred, "WebFetch") {
		t.Fatal("canonical tool must remain provider-visible")
	}
	for _, name := range []string{"webfetch", "web_fetch", "WEBFETCH"} {
		if !containsName(def.Deferred, name) {
			t.Errorf("alias %q missing from deferred names", name)
		}
	}
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}
