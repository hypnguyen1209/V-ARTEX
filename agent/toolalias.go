package agent

import (
	"context"
	"iter"
	"strings"

	"github.com/hypnguyen1209/norma/llm"
)

// toolAliasProvider accepts common model spellings at the provider boundary so
// the registry and permission pipeline continue to see only canonical names.
type toolAliasProvider struct{ llm.Provider }

func normalizeToolNames(provider llm.Provider) llm.Provider {
	return &toolAliasProvider{Provider: provider}
}

func (p *toolAliasProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	aliases := toolAliasMap(req.Tools)
	msg, stop, usage, err := p.Provider.Complete(ctx, req)
	if msg.Role != llm.RoleAssistant {
		return msg, stop, usage, err
	}
	for i := range msg.Content {
		if msg.Content[i].Type == llm.BlockToolUse {
			msg.Content[i].Name = canonicalToolName(msg.Content[i].Name, aliases)
		}
	}
	return msg, stop, usage, err
}

func (p *toolAliasProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	aliases := toolAliasMap(req.Tools)
	inner := p.Provider.Stream(ctx, req)
	return func(yield func(llm.StreamEvent, error) bool) {
		for event, err := range inner {
			if event.Type == llm.SEToolUseStart {
				event.ToolName = canonicalToolName(event.ToolName, aliases)
			}
			if !yield(event, err) {
				return
			}
		}
	}
}

func canonicalToolName(name string, aliases map[string]string) string {
	if canonical, ok := aliases[name]; ok {
		return canonical
	}
	return name
}

func toolAliasMap(tools []llm.ToolSchema) map[string]string {
	canonical := make(map[string]bool, len(tools))
	for _, tool := range tools {
		canonical[tool.Name] = true
	}
	aliases := make(map[string]string)
	ambiguous := make(map[string]bool)
	for _, tool := range tools {
		for _, alias := range toolAliasNames(tool.Name) {
			if canonical[alias] || ambiguous[alias] {
				continue
			}
			if existing, ok := aliases[alias]; ok && existing != tool.Name {
				delete(aliases, alias)
				ambiguous[alias] = true
				continue
			}
			aliases[alias] = tool.Name
		}
	}
	return aliases
}

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
