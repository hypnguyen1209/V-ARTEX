package server

import (
	"context"

	"github.com/hypnguyen1209/V-ARTEX/agent"
	"github.com/hypnguyen1209/V-ARTEX/db"
	"github.com/hypnguyen1209/V-ARTEX/intercept"
	"github.com/hypnguyen1209/V-ARTEX/traffic"
	actool "github.com/Autumn-27/norma/tool"
)

func migrateEnglishDefaults(ctx context.Context, pg *db.DB) error {
	return pg.MigrateLegacyEnglishDefaults(ctx, legacyEnglishDefaults())
}

func legacyEnglishDefaults() db.LegacyEnglishDefaults {
	current := agent.EnglishPromptDefaults()
	for key, text := range agent.EnglishWrapupDefaults() {
		current[key] = text
	}
	current["judge"] = intercept.DefaultJudgePrompt
	defaults := db.NewLegacyEnglishDefaults(current)
	defaults.ToolDefaults = make(map[string]db.EnglishToolDefault)
	for _, seed := range agent.BuiltinToolSeeds() {
		defaults.ToolDefaults[seed.Key] = db.EnglishToolDefault{Description: seed.Desc, Schema: seed.Schema}
	}
	// Constructors only capture handlers; metadata does not require live stores.
	s := &Server{}
	for _, group := range [][]actool.CoreTool{
		traffic.SeedToolMetas(), s.orchestrationTools(), s.platformTools(), s.findingRetestTools(),
	} {
		for _, tool := range group {
			defaults.ToolDefaults[tool.Name()] = db.EnglishToolDefault{Description: tool.Description(), Schema: tool.InputSchema()}
		}
	}
	return defaults
}
