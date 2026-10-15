package db

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// EnglishToolDefault is current metadata from the package that owns each tool.
type EnglishToolDefault struct {
	Description string
	Schema      map[string]any
}

// Frozen from every SeedTool catalog at 2a9c4ac, plus the earlier traffic_search
// description recognized by finding_workflow_tools_v3_host_search_description.
//
//go:embed legacy_tool_metadata.json
var legacyToolMetadataJSON []byte

type legacyToolMetadata struct {
	Key          string   `json:"key"`
	Descriptions []string `json:"descriptions"`
	Schema       []struct {
		Path  []string `json:"path"`
		Value string   `json:"value"`
	} `json:"schema"`
}

func englishToolText(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			return false
		}
	}
	return true
}

func toolSchemaText(schema map[string]any, path []string) string {
	var value any = schema
	for _, key := range path {
		object, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		value = object[key]
	}
	text, _ := value.(string)
	return text
}

// Each field is guarded independently, so partial operator edits survive.
// jsonb_set(create_missing=false) preserves structure, defaults and numeric data.
// This runs inside the existing startup transaction/advisory lock, regardless of
// old refresh flags, before persisted metadata is used to resolve runtime tools.
func migrateLegacyEnglishToolMetadata(ctx context.Context, store englishMigrationStore, current map[string]EnglishToolDefault) error {
	if len(current) == 0 {
		return nil
	}
	var legacy []legacyToolMetadata
	if err := json.Unmarshal(legacyToolMetadataJSON, &legacy); err != nil {
		return fmt.Errorf("decode frozen legacy catalog: %w", err)
	}
	for _, tool := range legacy {
		replacement, known := current[tool.Key]
		if !known {
			continue
		}
		if englishToolText(replacement.Description) {
			for _, description := range tool.Descriptions {
				if _, err := store.ExecContext(ctx, `UPDATE tools SET description=$3
WHERE key=$1 AND system AND description=$2`, tool.Key, description, replacement.Description); err != nil {
					return fmt.Errorf("%s description: %w", tool.Key, err)
				}
			}
		}
		for _, field := range tool.Schema {
			text := toolSchemaText(replacement.Schema, field.Path)
			if !englishToolText(text) {
				continue
			}
			if _, err := store.ExecContext(ctx, `UPDATE tools
SET schema=jsonb_set(schema,$2::text[],to_jsonb($4::text),false)
WHERE key=$1 AND system AND schema #> $2::text[]=to_jsonb($3::text)`,
				tool.Key, field.Path, field.Value, text); err != nil {
				return fmt.Errorf("%s schema %v: %w", tool.Key, field.Path, err)
			}
		}
	}
	return nil
}
