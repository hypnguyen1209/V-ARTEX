package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"unicode"

	"github.com/hypnguyen1209/V-ARTEX/agent"
	"github.com/hypnguyen1209/V-ARTEX/db"
	"github.com/hypnguyen1209/V-ARTEX/traffic"
	actool "github.com/Autumn-27/norma/tool"
)

type legacyToolFixture struct {
	Key         string
	Description string
	Schema      map[string]any
	Source      string
}

// Frozen by executing every SeedTool catalog at pre-translation commit 2a9c4ac.
func legacyToolCatalog(t *testing.T) []legacyToolFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/legacy_tool_catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var tools []legacyToolFixture
	if err = json.Unmarshal(raw, &tools); err != nil {
		t.Fatal(err)
	}
	return tools
}

func toolMigrationDB(t *testing.T) *db.DB {
	t.Helper()
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skipf("test postgres not configured: %v", err)
	}
	probe, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	if err = probe.PingContext(t.Context()); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.Close() })
	return pg
}

func setPriorToolMigrationFlags(t *testing.T, pg *db.DB) {
	t.Helper()
	for _, flag := range []string{
		"tool_schema_refresh_v7_list_facts_paging",
		"finding_workflow_tools_v3_host_search_description",
		"finding_workflow_tools_v2_reporter", "finding_traffic_tools_v1",
		"finding_retester_seed_v1", "goal_met_unbind_default_v1",
		"auto_report_finding_v1", "planner_report_finding_v1", "planner_list_assets_v1",
		"company_scope_rebind_v1", "worker_readtools_unbind_v1", "worker_readback_rebind_v2",
		"auto_default_bindings_v3", "reporter_agent_seed_v1",
		"goals_prompt_constraint_step_v1", "mainagent_prompt_goalless_intent_v1",
		"planner_prompt_compact_realistic_v2", "worker_prompt_compact_v4",
		"reporter_trigger_evidence_version_v1",
	} {
		old, found, err := pg.GetSetting(flag)
		if err != nil {
			t.Fatal(err)
		}
		if err = pg.SetSetting(flag, "true"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if found {
				_ = pg.SetSetting(flag, old)
			} else {
				_, _ = pg.Exec("DELETE FROM settings WHERE key=$1", flag)
			}
		})
	}
}

func installLegacyTool(t *testing.T, pg *db.DB, tool legacyToolFixture) {
	t.Helper()
	var old json.RawMessage
	err := pg.QueryRow("SELECT to_jsonb(t) FROM tools t WHERE key=$1", tool.Key).Scan(&old)
	if err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if len(old) > 0 {
			_, err = pg.Exec("INSERT INTO tools SELECT * FROM jsonb_populate_record(NULL::tools,$1::jsonb) ON CONFLICT(key) DO UPDATE SET system=EXCLUDED.system,description=EXCLUDED.description,schema=EXCLUDED.schema,agents=EXCLUDED.agents,enabled=EXCLUDED.enabled,kind=EXCLUDED.kind,exec=EXCLUDED.exec,deferred=EXCLUDED.deferred", string(old))
		} else {
			_, err = pg.Exec("DELETE FROM tools WHERE key=$1", tool.Key)
		}
		if err != nil {
			t.Errorf("restore tool %s: %v", tool.Key, err)
		}
	})
	schema, err := json.Marshal(tool.Schema)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pg.Exec(`INSERT INTO tools(key,system,description,schema,agents,enabled,kind,exec,deferred)
 VALUES($1,true,$2,$3::jsonb,'["operator"]',false,'script','{"script":"operator"}',true)
 ON CONFLICT(key) DO UPDATE SET system=true,description=EXCLUDED.description,schema=EXCLUDED.schema,agents=EXCLUDED.agents,enabled=EXCLUDED.enabled,kind=EXCLUDED.kind,exec=EXCLUDED.exec,deferred=EXCLUDED.deferred`, tool.Key, tool.Description, string(schema))
	if err != nil {
		t.Fatal(err)
	}
}

func hasHan(text string) bool {
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func currentToolCatalog(t *testing.T) map[string]legacyToolFixture {
	t.Helper()
	out := map[string]legacyToolFixture{}
	for _, tool := range agent.BuiltinToolSeeds() {
		out[tool.Key] = legacyToolFixture{Key: tool.Key, Description: tool.Desc, Schema: tool.Schema, Source: "agent.BuiltinToolSeeds"}
	}
	s := &Server{}
	for _, group := range []struct {
		name  string
		tools []actool.CoreTool
	}{
		{"traffic.SeedToolMetas", traffic.SeedToolMetas()},
		{"server.orchestrationTools", s.orchestrationTools()},
		{"server.platformTools", s.platformTools()},
		{"server.findingRetestTools", s.findingRetestTools()},
	} {
		for _, tool := range group.tools {
			if _, exists := out[tool.Name()]; exists {
				t.Fatalf("duplicate seed key %s", tool.Name())
			}
			out[tool.Name()] = legacyToolFixture{tool.Name(), tool.Description(), tool.InputSchema(), group.name}
		}
	}
	return out
}

func toolSnapshot(t *testing.T, pg *db.DB, key string) string {
	t.Helper()
	var raw string
	if err := pg.QueryRow("SELECT to_jsonb(t)::text FROM tools t WHERE key=$1", key).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestEnglishStartupMigratesCompleteFrozenToolCatalog(t *testing.T) {
	pg := toolMigrationDB(t)
	setPriorToolMigrationFlags(t, pg)
	legacy := legacyToolCatalog(t)
	current := currentToolCatalog(t)
	if len(legacy) != 51 || len(current) != 51 {
		t.Fatalf("catalog count legacy=%d current=%d, want 51 each", len(legacy), len(current))
	}
	for _, tool := range legacy {
		installLegacyTool(t, pg, tool)
	}
	if err := migrateEnglishDefaults(t.Context(), pg); err != nil {
		t.Fatal(err)
	}
	snapshots := map[string]string{}
	for _, old := range legacy {
		t.Run(old.Key, func(t *testing.T) {
			want, ok := current[old.Key]
			if !ok {
				t.Fatal("current seed missing")
			}
			if want.Source != old.Source {
				t.Fatalf("seed source changed: %s != %s", want.Source, old.Source)
			}
			got, err := pg.GetTool(old.Key)
			if err != nil {
				t.Fatal(err)
			}
			if got.Description != want.Description {
				t.Errorf("description is not current English seed")
			}
			var schema map[string]any
			if err = json.Unmarshal(got.Schema, &schema); err != nil {
				t.Fatal(err)
			}
			// Canonicalize Go integer/slice types through JSON before comparing.
			raw, _ := json.Marshal(want.Schema)
			var expected map[string]any
			_ = json.Unmarshal(raw, &expected)
			if !reflect.DeepEqual(schema, expected) {
				t.Errorf("schema does not match current English seed")
			}
			if hasHan(got.Description) || hasHan(string(got.Schema)) {
				t.Error("legacy Han remains")
			}
			if got.Enabled || !got.Deferred || got.Kind != "script" || !reflect.DeepEqual(got.Agents, []string{"operator"}) || string(got.Exec) != `{"script": "operator"}` {
				t.Errorf("operator settings changed: %+v", got)
			}
			snapshots[old.Key] = toolSnapshot(t, pg, old.Key)
		})
	}
	// Start the complete database and server English migration paths again.
	dsn, _, err := db.DSN()
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = migrateEnglishDefaults(t.Context(), reopened); err != nil {
		t.Fatal(err)
	}
	for key, before := range snapshots {
		if after := toolSnapshot(t, pg, key); after != before {
			t.Errorf("%s changed on repeated startup (including updated_at)", key)
		}
	}
}

func TestEnglishStartupPreservesIndependentToolCustomizations(t *testing.T) {
	pg := toolMigrationDB(t)
	setPriorToolMigrationFlags(t, pg)
	current := currentToolCatalog(t)
	for _, tc := range []struct {
		key  string
		path []string
	}{
		{"list_assets", []string{"properties", "dsl"}},
		{"insert_assets", []string{"properties", "assets", "items", "properties", "domain"}},
		{"add_hint", []string{"properties", "hints", "items", "properties", "traffic_refs", "items", "properties", "note"}},
		{"traffic_get", []string{"properties", "id"}},
		{"spawn_task", []string{"properties", "description"}},
	} {
		t.Run(tc.key, func(t *testing.T) {
			key := tc.key
			var old legacyToolFixture
			for _, tool := range legacyToolCatalog(t) {
				if tool.Key == key {
					old = tool
				}
			}
			raw, _ := json.Marshal(old.Schema)
			_ = json.Unmarshal(raw, &old.Schema)
			props := old.Schema["properties"].(map[string]any)
			// A custom description/default on one parameter must not block sibling paths.
			edited := old.Schema
			for _, name := range tc.path {
				edited = edited[name].(map[string]any)
			}
			const custom = "Operator \u81ea\u5b9a\u4e49 description"
			edited["description"] = custom
			edited["default"] = map[string]any{"value": custom, "number": json.Number("9007199254740993")}
			props["operator_extension"] = map[string]any{"type": "string", "description": custom, "default": custom}
			old.Description = custom
			installLegacyTool(t, pg, old)
			if err := migrateEnglishDefaults(t.Context(), pg); err != nil {
				t.Fatal(err)
			}
			got, err := pg.GetTool(key)
			if err != nil {
				t.Fatal(err)
			}
			if got.Description != custom {
				t.Error("custom top-level description changed")
			}
			raw, _ = json.Marshal(current[key].Schema)
			var expected map[string]any
			_ = json.Unmarshal(raw, &expected)
			expectedProps := expected["properties"].(map[string]any)
			expectedEdited := expected
			for _, name := range tc.path {
				expectedEdited = expectedEdited[name].(map[string]any)
			}
			expectedEdited["description"] = custom
			expectedEdited["default"] = map[string]any{"value": custom, "number": json.Number("9007199254740993")}
			expectedProps["operator_extension"] = props["operator_extension"]
			// Compare in PostgreSQL to keep arbitrary numeric defaults exact.
			expectedRaw, _ := json.Marshal(expected)
			var equal bool
			if err = pg.QueryRow("SELECT schema=$2::jsonb FROM tools WHERE key=$1", key, string(expectedRaw)).Scan(&equal); err != nil {
				t.Fatal(err)
			}
			if !equal {
				t.Errorf("custom schema/default/structure changed or legacy sibling paths were not migrated: %s", got.Schema)
			}
		})
	}
}

func TestEnglishStartupLeavesCustomUnknownAndMissingSchemaPaths(t *testing.T) {
	pg := toolMigrationDB(t)
	setPriorToolMigrationFlags(t, pg)
	legacy := legacyToolCatalog(t)
	for _, tc := range []struct {
		name, key string
		system    bool
		schema    map[string]any
	}{
		{"custom same built-in key", "list_assets", false, nil},
		{"unknown system tool", "operator_unknown_tool", true, nil},
		{"custom schema structure", "insert_assets", true, map[string]any{"type": "object", "properties": map[string]any{"assets": "operator replacement", "extra": map[string]any{"description": legacy[0].Description}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var old legacyToolFixture
			for _, tool := range legacy {
				if tool.Key == tc.key || (tc.key == "operator_unknown_tool" && tool.Key == "list_assets") {
					old = tool
					break
				}
			}
			old.Key = tc.key
			if tc.schema != nil {
				old.Schema = tc.schema
				old.Description = "Operator schema"
			}
			installLegacyTool(t, pg, old)
			if _, err := pg.Exec("UPDATE tools SET system=$2 WHERE key=$1", tc.key, tc.system); err != nil {
				t.Fatal(err)
			}
			before := toolSnapshot(t, pg, tc.key)
			if err := migrateEnglishDefaults(t.Context(), pg); err != nil {
				t.Fatal(err)
			}
			if after := toolSnapshot(t, pg, tc.key); after != before {
				t.Errorf("protected row changed")
			}
		})
	}
}

func TestEnglishToolCatalogCoversEveryFrozenHanPath(t *testing.T) {
	current := currentToolCatalog(t)
	descriptions, paths := 0, 0
	var walk func(any, any, string)
	walk = func(old, now any, path string) {
		if text, ok := old.(string); ok {
			if hasHan(text) {
				paths++
				replacement, ok := now.(string)
				if !ok || replacement == "" || hasHan(replacement) {
					t.Errorf("%s has no current English replacement", path)
				}
			}
			return
		}
		switch old := old.(type) {
		case map[string]any:
			now, _ := now.(map[string]any)
			for key, value := range old {
				walk(value, now[key], path+"/"+key)
			}
		case []any:
			now, _ := now.([]any)
			for i, value := range old {
				if i < len(now) {
					walk(value, now[i], fmt.Sprintf("%s/%d", path, i))
				} else {
					t.Errorf("missing current path %s/%d", path, i)
				}
			}
		}
	}
	for _, old := range legacyToolCatalog(t) {
		now, ok := current[old.Key]
		if !ok {
			t.Fatalf("missing seed %s", old.Key)
		}
		if hasHan(old.Description) {
			descriptions++
		}
		if now.Description == "" || hasHan(now.Description) {
			t.Errorf("%s current description is not English", old.Key)
		}
		raw, _ := json.Marshal(now.Schema)
		var schema any
		_ = json.Unmarshal(raw, &schema)
		if hasHan(string(raw)) {
			t.Errorf("%s current schema is not English", old.Key)
		}
		walk(old.Schema, schema, old.Key+"/schema")
	}
	if descriptions != 51 || paths != 192 {
		t.Fatalf("frozen legacy coverage descriptions=%d schema paths=%d, want 51/192", descriptions, paths)
	}
}

func TestEnglishStartupMigratesEarlierTrafficSearchWithPriorFlagSet(t *testing.T) {
	pg := toolMigrationDB(t)
	setPriorToolMigrationFlags(t, pg)
	raw, err := os.ReadFile("testdata/legacy_traffic_search_description.json")
	if err != nil {
		t.Fatal(err)
	}
	var earlier string
	if err = json.Unmarshal(raw, &earlier); err != nil {
		t.Fatal(err)
	}
	var old legacyToolFixture
	for _, tool := range legacyToolCatalog(t) {
		if tool.Key == "traffic_search" {
			old = tool
		}
	}
	old.Description = earlier
	installLegacyTool(t, pg, old)
	if err = migrateEnglishDefaults(t.Context(), pg); err != nil {
		t.Fatal(err)
	}
	got, err := pg.GetTool(old.Key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != traffic.TrafficSearchDescription || hasHan(string(got.Schema)) {
		t.Fatal("earlier traffic_search metadata did not migrate")
	}
	// The old special case must also be harmless after English startup.
	before := toolSnapshot(t, pg, old.Key)
	if err = pg.SetSetting("finding_workflow_tools_v3_host_search_description", "false"); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{pg: pg}}
	s.seedFindingWorkflowTools()
	got, err = pg.GetTool(old.Key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != traffic.TrafficSearchDescription {
		t.Fatal("workflow migration conflicts with English migration")
	}
	if after := toolSnapshot(t, pg, old.Key); after != before {
		t.Fatal("workflow special case rewrote migrated metadata")
	}
}

func TestEnglishToolMigrationRollsBackAllMetadataOnFailure(t *testing.T) {
	pg := toolMigrationDB(t)
	legacy := legacyToolCatalog(t)[:2]
	for _, tool := range legacy {
		installLegacyTool(t, pg, tool)
	}
	before := toolSnapshot(t, pg, legacy[0].Key)
	// Reject the second tool update after the first has succeeded.
	if _, err := pg.Exec(`ALTER TABLE tools ADD CONSTRAINT english_tool_migration_test_failure
CHECK (key <> 'list_findings' OR description <> 'Rejected English tool description') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := pg.Exec("ALTER TABLE tools DROP CONSTRAINT english_tool_migration_test_failure")
		if err != nil {
			t.Error(err)
		}
	})
	defaults := legacyEnglishDefaults()
	rejected := defaults.ToolDefaults["list_findings"]
	rejected.Description = "Rejected English tool description"
	defaults.ToolDefaults["list_findings"] = rejected
	if err := pg.MigrateLegacyEnglishDefaults(t.Context(), defaults); err == nil {
		t.Fatal("expected tool migration error")
	}
	if after := toolSnapshot(t, pg, legacy[0].Key); after != before {
		t.Fatal("failed startup committed partial tool metadata")
	}
}

func TestFreshToolSeedsAreCurrentEnglishAndStartupNoOp(t *testing.T) {
	pg := toolMigrationDB(t)
	setPriorToolMigrationFlags(t, pg)
	current := currentToolCatalog(t)
	for _, tool := range legacyToolCatalog(t) {
		installLegacyTool(t, pg, tool)
		if _, err := pg.Exec("DELETE FROM tools WHERE key=$1", tool.Key); err != nil {
			t.Fatal(err)
		}
	}
	oldResolve, oldTrafficBinding := agent.ToolResolve, agent.FindingTrafficBindingEnabled
	t.Cleanup(func() { agent.ToolResolve, agent.FindingTrafficBindingEnabled = oldResolve, oldTrafficBinding })
	wireTools(pg, nil)
	s := &Server{m: &Manager{pg: pg}}
	s.seedOrchestrationTools()
	if err := s.seedFindingRetester(); err != nil {
		t.Fatal(err)
	}
	snapshots := make(map[string]string)
	for key, want := range current {
		got, err := pg.GetTool(key)
		if err != nil || got == nil {
			t.Fatalf("seed %s: %+v %v", key, got, err)
		}
		if got.Description != want.Description || hasHan(got.Description) || hasHan(string(got.Schema)) {
			t.Errorf("%s seeded metadata is not current English", key)
		}
		raw, _ := json.Marshal(want.Schema)
		var equal bool
		if err = pg.QueryRow("SELECT schema=$2::jsonb FROM tools WHERE key=$1", key, string(raw)).Scan(&equal); err != nil {
			t.Fatal(err)
		}
		if !equal {
			t.Errorf("%s seeded schema differs from code", key)
		}
		snapshots[key] = toolSnapshot(t, pg, key)
	}
	if err := migrateEnglishDefaults(t.Context(), pg); err != nil {
		t.Fatal(err)
	}
	for key, before := range snapshots {
		if after := toolSnapshot(t, pg, key); after != before {
			t.Errorf("%s current seed changed on English startup", key)
		}
	}
}

func TestEnglishToolMigrationFailsClosedForMissingReplacements(t *testing.T) {
	pg := toolMigrationDB(t)
	var old legacyToolFixture
	for _, tool := range legacyToolCatalog(t) {
		if tool.Key == "list_assets" {
			old = tool
		}
	}
	installLegacyTool(t, pg, old)
	before := toolSnapshot(t, pg, old.Key)
	for _, defaults := range []db.LegacyEnglishDefaults{{}, {ToolDefaults: map[string]db.EnglishToolDefault{
		"list_assets":           {Description: " ", Schema: map[string]any{"properties": map[string]any{"dsl": map[string]any{"description": old.Description}}}},
		"operator_unknown_tool": {Description: "Unknown", Schema: old.Schema},
	}}} {
		if err := pg.MigrateLegacyEnglishDefaults(t.Context(), defaults); err != nil {
			t.Fatal(err)
		}
		if after := toolSnapshot(t, pg, old.Key); after != before {
			t.Fatal("incomplete or non-English replacements changed metadata")
		}
	}
}

func TestEnglishStartupMigratesLegacyToolWithPriorFlagsSet(t *testing.T) {
	pg := toolMigrationDB(t)
	setPriorToolMigrationFlags(t, pg)
	var legacy legacyToolFixture
	for _, tool := range legacyToolCatalog(t) {
		if tool.Key == "list_assets" {
			legacy = tool
			break
		}
	}
	if legacy.Key == "" {
		t.Fatal("list_assets fixture missing")
	}
	installLegacyTool(t, pg, legacy)
	if err := migrateEnglishDefaults(t.Context(), pg); err != nil {
		t.Fatal(err)
	}
	got, err := pg.GetTool(legacy.Key)
	if err != nil {
		t.Fatal(err)
	}
	if hasHan(got.Description) || hasHan(string(got.Schema)) {
		t.Fatalf("legacy list_assets metadata remains Chinese after English startup with all prior tool flags true: description=%q schema=%s", got.Description, got.Schema)
	}
	if got.Enabled || got.Kind != "script" || !got.Deferred || !reflect.DeepEqual(got.Agents, []string{"operator"}) || string(got.Exec) != `{"script": "operator"}` {
		t.Fatalf("operator tool settings changed: %+v", got)
	}
}
