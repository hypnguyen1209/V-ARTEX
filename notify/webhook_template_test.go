package notify

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// Lock the capabilities of user-supplied webhook templates, the only place user
// strings are evaluated as code. Adding a context method or readFile helper could
// silently expand access despite looking like a harmless small change.

// TestTemplateContextHasNoMethods protects the template boundary. text/template
// can call exported methods, so all reachable context types must expose data fields
// only. Any new webhookTemplateData/webhookItem method requires checking what
// data or capabilities it would expose to template authors.
func TestTemplateContextHasNoMethods(t *testing.T) {
	for _, v := range []any{webhookTemplateData{}, webhookItem{}} {
		typ := reflect.TypeOf(v)
		if n := typ.NumMethod(); n != 0 {
			var names []string
			for i := 0; i < n; i++ {
				names = append(names, typ.Method(i).Name)
			}
			t.Fatalf("%s exposes %d methods (%s) callable by text/template, granting these capabilities to template authors", typ.Name(), n, strings.Join(names, ", "))
		}
	}
}

// TestTemplateFuncsAreMinimal locks the available helpers to json/jsons.
// They serialize JSON and cannot read files, send requests or run commands.
func TestTemplateFuncsAreMinimal(t *testing.T) {
	var got []string
	for name := range webhookTemplateFuncs {
		got = append(got, name)
	}
	sort.Strings(got)
	want := []string{"json", "jsons"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Template helpers changed: got %v, want %v. New helpers must not expand capabilities to file access, network requests or command execution", got, want)
	}
}

// TestTemplateCannotReachUnknownData requires unknown fields to fail without
// echoing internal data.
func TestTemplateCannotReachUnknownData(t *testing.T) {
	_, err := renderWebhookBody(`{"x": {{.Environment}}, "y": {{.Env}}}`, singleMsg())
	if err == nil {
		t.Fatal("Unknown field must fail")
	}
	// Errors must not reveal actual context content such as finding titles or summaries.
	for _, leak := range []string{"SQL injection", "id parameter"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("Template error leaked message content %q: %v", leak, err)
		}
	}
}

// TestTemplateRenderFailsPermanently treats invalid templates as configuration
// failures that retries cannot fix.
func TestTemplateRenderFailsPermanently(t *testing.T) {
	cfg := map[string]any{
		"url":           "https://example.com/hook",
		"body_template": `{{.Items.`,
	}
	if err := (webhookChannel{}).Validate(cfg); err == nil {
		t.Fatal("Template syntax errors must be rejected when saving")
	}
	// Direct delivery bypassing validation must still fail permanently.
	_, err := (webhookChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("Invalid template must fail permanently, got %v", err)
	}
}

// TestTemplateCanOnlyProduceJSON requires valid JSON, preventing templates
// from emitting arbitrary text to trigger other protocols.
func TestTemplateCanOnlyProduceJSON(t *testing.T) {
	// A valid template passes validation.
	ok := map[string]any{"url": "https://example.com/hook", "body_template": `{"t":{{json .Title}}}`}
	if err := (webhookChannel{}).Validate(ok); err != nil {
		t.Fatalf("Valid template must pass validation: %v", err)
	}
	// Reject non-JSON rendered content rather than sending it unchanged.
	bad := map[string]any{"url": "http://127.0.0.1:1/hook", "body_template": `not json {{.Count}}`}
	_, err := (webhookChannel{}).Send(context.Background(), bad, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("Non-JSON output must fail permanently, got %v", err)
	}
	if !strings.Contains(err.Error(), "valid JSON") {
		t.Errorf("Error must identify the JSON problem, got %v", err)
	}
}
