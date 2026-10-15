package server

import (
	"context"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
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
	return db.NewLegacyEnglishDefaults(current)
}
