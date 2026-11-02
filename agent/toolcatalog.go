package agent

import (
	"context"
	"encoding/json"
	"strings"

	actool "github.com/Autumn-27/norma/tool"
)

// This file exposes built-in tools as an enumerable catalog with DB-overridable metadata:
//   - BuiltinToolSeeds() expands the three execution agents' tool sets into seed records
//     (key, description, parameter schema, default agent bindings) for idempotent startup insertion into tools.
//   - ToolResolve filters assembled tools by agent, overrides descriptions/schema, and injects
//     parameter defaults from DB tools rows. Keys and handlers remain in code; the DB changes only prose/defaults.
// Call behavior always comes from code; the DB can only change model-facing explanations and default inputs.

// ToolSeed is a seedable tool snapshot: Key is CoreTool.Name(), bound to its handler and read-only
// in the UI. Desc/Schema come from code definitions; Agents identifies default code bindings.
type ToolSeed struct {
	Key    string         // CoreTool.Name(): immutable primary key.
	Desc   string         // Top-level description, overridable in the UI.
	Schema map[string]any // Parameter JSON Schema; structure read-only, description/default editable in the UI.
	Agents []string       // Default agent keys (worker/planner/mainagent).
}

// builtinToolsByAgent constructs each execution agent's domain tools with a read-only shell ToolSet
// and nil stores. Constructors only capture closures and never dereference stores, so nil is safe.
// These tools are used only for Name()/Description()/InputSchema(), never Call.
//
// Intentionally excludes SDK generic actool.DefaultTools() (Read/Write/Edit/MultiEdit/LS/Glob/
// Grep/Bash): all agents always own them, and most instructions live in Prompt(). Overriding
// only Description() would misleadingly override half their prose. No seed means no DB row,
// so ToolResolve passes them through unchanged. Only ARTEX domain tools are managed in this catalog.
func builtinToolsByAgent() map[string][]actool.CoreTool {
	ts := NewToolSet(nil, "")
	return map[string][]actool.CoreTool{
		"mainagent": ts.MainAgentTools(),
		"planner":   ts.PlannerTools(),
		"worker":    ts.WorkerTools(),
		// goals defaults to set_goals + set_constraints to persist decomposed goals and extracted
		// constraints. Shared managed tools with mainagent; the UI edits descriptions/schema and per-agent bindings.
		"goals": {ts.setGoals(), ts.setConstraints()},
		// auto defaults to finding reporting and asset management; other domain tools can be enabled in the UI.
		// Seed new databases here; seedAutoDefaultBindings migrates existing databases.
		"auto": {ts.addFinding(), ts.insertAssets(), ts.addCompanyScope(), ts.listAssets(), ts.listCompanies()},
		// pentest defaults to querying/inserting assets, reporting/listing findings, and listing companies.
		// Seed new databases here; seedPentestDefaultBindings migrates existing databases.
		"pentest": {ts.listAssets(), ts.insertAssets(), ts.addFinding(), ts.listFindings(), ts.listCompanies()},
	}
}

// defaultUnbound tools still appear in the catalog and can be manually enabled per agent, but
// have no default bindings. ToolResolve drops unbound tools for every agent until explicitly enabled.
// Keep them in an agent's base set (e.g. goal_met in PlannerTools) so seeds can read desc/schema
// and ToolResolve can retain them at runtime after the user manually enables a binding.
//
// goal_met declares overall task completion without individual prove_goal calls. It risks premature
// completion and duplicates automatic completion after the last proven goal, so requires manual binding.
var defaultUnbound = map[string]bool{"goal_met": true}

// BuiltinToolSeeds merges and deduplicates agent tool sets: tools with the same name (e.g. list_assets)
// become one seed with the union of Agents; defaultUnbound tools always have empty bindings.
func BuiltinToolSeeds() []ToolSeed {
	byAgent := builtinToolsByAgent()
	order := []string{"mainagent", "goals", "planner", "worker", "auto", "pentest"}

	type acc struct {
		tool   actool.CoreTool
		agents []string
	}
	m := map[string]*acc{}
	var keys []string
	for _, ak := range order {
		for _, t := range byAgent[ak] {
			a, ok := m[t.Name()]
			if !ok {
				a = &acc{tool: t}
				m[t.Name()] = a
				keys = append(keys, t.Name())
			}
			a.agents = append(a.agents, ak)
		}
	}

	out := make([]ToolSeed, 0, len(keys))
	for _, k := range keys {
		a := m[k]
		agents := a.agents
		if defaultUnbound[k] {
			agents = []string{} // Catalogued and manually bindable, but unbound by default; store [] rather than null like other tools.
		}
		out = append(out, ToolSeed{
			Key:    k,
			Desc:   a.tool.Description(),
			Schema: a.tool.InputSchema(),
			Agents: agents,
		})
	}
	return out
}

// ToolResolve, if set, post-processes an agent's fully-assembled tool list against
// the DB tools table: it drops tools not bound to this agent (or globally disabled)
// and wraps the rest so the model sees the DB-overridden description/schema and
// default inputs are injected. Tools with no matching DB row (MCP/skill/host tools like
// traffic) pass through untouched. nil = tools unchanged. Wired in server/assembly.go.
var ToolResolve func(ctx context.Context, agentKey string, tools []actool.CoreTool) []actool.CoreTool

type aliasTool struct {
	actool.CoreTool
	name string
}

func (a *aliasTool) Name() string { return a.name }

func toolAliasNames(name string) []string {
	lower := strings.ToLower(name)
	if name == lower {
		return nil
	}
	candidates := []string{lower, camelToSnake(name), strings.ToUpper(name)}
	seen := map[string]bool{name: true}
	var aliases []string
	for _, alias := range candidates {
		if seen[alias] {
			continue
		}
		seen[alias] = true
		aliases = append(aliases, alias)
	}
	return aliases
}

func camelToSnake(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'A' && c <= 'Z' && i > 0 && name[i-1] != '_' {
			prev := name[i-1]
			nextLower := i+1 < len(name) && name[i+1] >= 'a' && name[i+1] <= 'z'
			if prev >= 'a' && prev <= 'z' || prev >= '0' && prev <= '9' || nextLower {
				b.WriteByte('_')
			}
		}
		b.WriteByte(c)
	}
	return strings.ToLower(b.String())
}

func addToolAliases(tools []actool.CoreTool, deferred []string) ([]actool.CoreTool, []string) {
	occupied := make(map[string]bool, len(tools)+len(deferred))
	for _, tool := range tools {
		occupied[tool.Name()] = true
	}
	for _, name := range deferred {
		occupied[name] = true
	}
	out := append([]actool.CoreTool(nil), tools...)
	deferredOut := append([]string(nil), deferred...)
	for _, tool := range tools {
		for _, name := range toolAliasNames(tool.Name()) {
			if occupied[name] {
				continue
			}
			occupied[name] = true
			out = append(out, &aliasTool{CoreTool: tool, name: name})
			deferredOut = append(deferredOut, name)
		}
	}
	return out, deferredOut
}

// DecorateTool wraps t so Description()/InputSchema() report the DB overrides and
// Call() injects scalar parameter defaults (from schema's "default" props) whenever
// the model omitted them. Name/Prompt/permission/scheduler flags delegate to t, so
// the tool's identity and handler are unchanged. Empty desc/schema fall back to t's.
func DecorateTool(t actool.CoreTool, desc string, schema map[string]any) actool.CoreTool {
	if desc == "" {
		desc = t.Description()
	}
	if len(schema) == 0 {
		schema = t.InputSchema()
	}
	return &overriddenTool{CoreTool: t, desc: desc, schema: schema}
}

// overriddenTool is a CoreTool decorator: it embeds the original (so all behavioral
// methods — Prompt/IsReadOnly/IsConcurrencySafe/CheckPermissions/Name — delegate)
// and overrides only the model-facing description/schema plus default injection.
type overriddenTool struct {
	actool.CoreTool
	desc   string
	schema map[string]any
}

func (o *overriddenTool) Description() string         { return o.desc }
func (o *overriddenTool) InputSchema() map[string]any { return o.schema }

func (o *overriddenTool) Call(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	return o.CoreTool.Call(ctx, injectDefaults(in, o.schema), tc)
}

// injectDefaults fills scalar parameter defaults declared in the (possibly edited)
// schema into the input JSON whenever the model omitted the field or left it empty/
// null. Structure (names/types/required) is untouched; only default values are merged in.
func injectDefaults(in json.RawMessage, schema map[string]any) json.RawMessage {
	defs := scalarDefaults(schema)
	if len(defs) == 0 {
		return in
	}
	m := map[string]json.RawMessage{}
	if len(in) > 0 {
		if err := json.Unmarshal(in, &m); err != nil {
			return in // non-object input: don't touch it
		}
	}
	changed := false
	for k, dv := range defs {
		if cur, ok := m[k]; !ok || isEmptyJSON(cur) {
			m[k] = dv
			changed = true
		}
	}
	if !changed {
		return in
	}
	b, err := json.Marshal(m)
	if err != nil {
		return in
	}
	return b
}

// scalarDefaults extracts properties[k]["default"] for scalar params (string/
// integer/number/boolean). Array/object defaults are skipped: merging them is
// ambiguous and not worth the surprise.
func scalarDefaults(schema map[string]any) map[string]json.RawMessage {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return nil
	}
	out := map[string]json.RawMessage{}
	for name, raw := range props {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		dv, ok := p["default"]
		if !ok || dv == nil {
			continue
		}
		switch p["type"] {
		case "string", "integer", "number", "boolean":
			if b, err := json.Marshal(dv); err == nil {
				out[name] = b
			}
		}
	}
	return out
}

func isEmptyJSON(raw json.RawMessage) bool {
	s := string(raw)
	return s == "null" || s == `""`
}
