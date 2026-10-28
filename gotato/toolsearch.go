package gotato

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// SearchToolName is the local name of the built-in Tool the Model loads Tools
// with. Its description is the catalog. It is visible while any ToolSet
// installed with WithToolSet or WithFoldedToolSet holds a Tool the Session may
// load.
const SearchToolName = "search_tools"

// Fixed counts of one search: the top matches of a query are loaded, and the
// next ones are listed unloaded. They are not settings.
const (
	searchLoadCount = 3
	searchListCount = 5
)

// ToolSearcher ranks the Tools a Session may still load against a query. A team
// plugs in its own search, such as one over embeddings, with WithToolSearcher.
// Gotato hands it one query and the specs of those Tools (the ID of each is
// its qualified ID, and none is above the Effect cap, switched off or loaded
// already), and loads the first three IDs it returns and lists the next five.
// An ID that is not among tools is ignored.
type ToolSearcher interface {
	Search(ctx context.Context, query string, tools []ToolSpec) ([]string, error)
}

// WithToolSearcher replaces the built-in BM25 search of search_tools.
func WithToolSearcher(searcher ToolSearcher) SessionOption {
	return func(c *sessionConfig) error {
		if searcher == nil {
			return runtimeError(CodeInvalidArgument, "WithToolSearcher", "ToolSearcher is nil", nil)
		}
		c.toolSearcher = searcher
		return nil
	}
}

// searchTool is the ordinary Tool the Model calls to load Tools.
type searchTool struct {
	registry    *toolRegistry
	description string
}

func (t *searchTool) setDescription(text string) { t.description = text }

func (t *searchTool) Spec() ToolSpec {
	schema, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"names": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Qualified tool names to load exactly.",
			},
			"queries": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Searches that load the best matching tools.",
			},
			"toolset": map[string]any{
				"type":        "string",
				"description": "A ToolSet whose tools to list without loading any.",
			},
		},
	})
	return ToolSpec{
		ID:          SearchToolName,
		Name:        SearchToolName,
		Description: t.description,
		Sequential:  true,
		InputSchema: schema,
		// It changes only which Tools the Model sees, and those Tools pass
		// the Effect cap like any other, so it stays visible under every cap.
		Effect: ToolEffectRead,
	}
}

func (t *searchTool) Execute(ctx context.Context, use ToolUse, progress ToolProgressFunc) (ToolResult, error) {
	var input struct {
		Names   []string `json:"names"`
		Queries []string `json:"queries"`
		Toolset string   `json:"toolset"`
	}
	if err := json.Unmarshal(use.Arguments, &input); err != nil {
		return ToolResult{}, runtimeError(CodeToolArgumentFailure, SearchToolName, "cannot decode search arguments", err)
	}
	if len(input.Names) == 0 && len(input.Queries) == 0 && strings.TrimSpace(input.Toolset) == "" {
		return ToolResult{}, runtimeError(CodeToolArgumentFailure, SearchToolName, "pass names, queries or toolset", nil)
	}
	var loaded, already, notFound, found []string
	add := func(list *[]string, id string) {
		if !slices.Contains(*list, id) {
			*list = append(*list, id)
		}
	}
	for _, name := range input.Names {
		name = strings.TrimSpace(name)
		switch t.registry.stageLoad(name, ToolLoadedWhyNames) {
		case loadStaged:
			add(&loaded, name)
		case loadAlready:
			add(&already, name)
		default:
			add(&notFound, name)
		}
	}
	var noMatch []string
	for _, query := range input.Queries {
		ranked, err := t.registry.rank(ctx, query)
		if err != nil {
			return ToolResult{}, err
		}
		if len(ranked) == 0 {
			noMatch = append(noMatch, query)
			continue
		}
		for i, id := range ranked {
			switch {
			case i < searchLoadCount:
				if t.registry.stageLoad(id, ToolLoadedWhyQuery) == loadStaged {
					add(&loaded, id)
				}
			case i < searchLoadCount+searchListCount:
				add(&found, id)
			}
		}
	}
	found = slices.DeleteFunc(found, func(id string) bool { return slices.Contains(loaded, id) })
	var parts []string
	if len(loaded) > 0 {
		parts = append(parts, "Loaded: "+strings.Join(loaded, ", ")+".")
	}
	if len(already) > 0 {
		parts = append(parts, "Already available: "+strings.Join(already, ", ")+".")
	}
	if len(found) > 0 {
		parts = append(parts, "Also found, not loaded: "+strings.Join(found, ", ")+".")
	}
	if len(notFound) > 0 {
		parts = append(parts, "Not found: "+strings.Join(notFound, ", ")+".")
	}
	if len(noMatch) > 0 {
		parts = append(parts, fmt.Sprintf("No tool matches: %s.", strings.Join(noMatch, "; ")))
	}
	if name := strings.TrimSpace(input.Toolset); name != "" {
		parts = append(parts, t.registry.describeSet(name))
	}
	return ToolResult{
		Status:      ToolResultStatusOK,
		Content:     []ContentPart{{Kind: ContentKindText, Text: strings.Join(parts, " ")}},
		LoadedTools: loaded,
	}, nil
}
