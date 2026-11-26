package gotato

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// SearchToolName is the local name of the built-in Tool the Model loads Tools
// with. Its description is the catalog. It is visible while any ToolSet is
// installed with WithToolSet or WithFoldedToolSet, even when that ToolSet is
// unavailable now or holds no Tool the Session may load.
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
// [SEAM] a team plugs in its own search; the built-in BM25 is the default.
type ToolSearcher interface {
	Search(ctx context.Context, query string, tools []ToolSpec) ([]string, error)
}

// WithToolSearcher replaces the built-in BM25 search of search_tools.
func WithToolSearcher(searcher ToolSearcher) SessionOption {
	return func(c *sessionConfig) error {
		if err := c.once("WithToolSearcher"); err != nil {
			return err
		}
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

func (t *searchTool) Execute(ctx context.Context, use ToolUse, _ ToolProgressFunc) (ToolResult, error) {
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
	var outcome searchOutcome
	outcome.byNames(t.registry, input.Names)
	if err := outcome.byQueries(ctx, t.registry, input.Queries); err != nil {
		return ToolResult{}, err
	}
	outcome.found = slices.DeleteFunc(outcome.found, func(id string) bool { return slices.Contains(outcome.loaded, id) })
	return ToolResult{
		Status:      ToolResultStatusOK,
		Content:     []ContentPart{{Kind: ContentKindText, Text: outcome.text(t.registry, input.Toolset)}},
		LoadedTools: outcome.loaded,
	}, nil
}

// searchOutcome is what one search_tools call came to, list by list.
type searchOutcome struct {
	loaded, already, unavailable, notFound, found, noMatch []string
}

// addUnique appends id to list unless list holds it already.
func addUnique(list *[]string, id string) {
	if !slices.Contains(*list, id) {
		*list = append(*list, id)
	}
}

// byNames sorts each of names into the list its loadable state puts it in.
func (o *searchOutcome) byNames(registry *toolRegistry, names []string) {
	for _, name := range names {
		name = strings.TrimSpace(name)
		switch registry.loadable(name) {
		case loadOK:
			addUnique(&o.loaded, name)
		case loadAlready:
			addUnique(&o.already, name)
		case loadUnavailable:
			addUnique(&o.unavailable, name)
		default:
			addUnique(&o.notFound, name)
		}
	}
}

// byQueries ranks the Tools against each of queries, loads the top matches
// and lists the next ones.
func (o *searchOutcome) byQueries(ctx context.Context, registry *toolRegistry, queries []string) error {
	for _, query := range queries {
		ranked, err := registry.rank(ctx, query, o.loaded)
		if err != nil {
			return err
		}
		if len(ranked) == 0 {
			o.noMatch = append(o.noMatch, query)
			continue
		}
		for i, id := range ranked {
			switch {
			case i < searchLoadCount:
				addUnique(&o.loaded, id)
			case i < searchLoadCount+searchListCount:
				addUnique(&o.found, id)
			}
		}
	}
	return nil
}

// text is the result the Model reads: each list that is not empty, then the
// ToolSet named by toolset.
func (o *searchOutcome) text(registry *toolRegistry, toolset string) string {
	var parts []string
	if len(o.loaded) > 0 {
		parts = append(parts, "Loaded: "+strings.Join(o.loaded, ", ")+".")
	}
	if len(o.already) > 0 {
		parts = append(parts, "Already available: "+strings.Join(o.already, ", ")+".")
	}
	if len(o.unavailable) > 0 {
		parts = append(parts, "Unavailable now, its ToolSet is unavailable; try again in a later Run: "+strings.Join(o.unavailable, ", ")+".")
	}
	if len(o.found) > 0 {
		parts = append(parts, "Found but not loaded, so do not call them yet; call search_tools with these names first: "+strings.Join(o.found, ", ")+".")
	}
	if len(o.notFound) > 0 {
		parts = append(parts, "Not found: "+strings.Join(o.notFound, ", ")+".")
	}
	if len(o.noMatch) > 0 {
		parts = append(parts, fmt.Sprintf("No tool matches: %s.", strings.Join(o.noMatch, "; ")))
	}
	if name := strings.TrimSpace(toolset); name != "" {
		parts = append(parts, registry.describeSet(name))
	}
	return strings.Join(parts, " ")
}

// loadWhy says why a committed Tool Result of call loaded id: search_tools
// loads by names when id is among the names it was passed, and by query
// otherwise; any other result is a call to a Tool that was not loaded.
func loadWhy(call ToolCall, id string) ToolLoadedWhy {
	if call.ToolID != SearchToolName {
		return ToolLoadedWhyCall
	}
	var input struct {
		Names []string `json:"names"`
	}
	_ = json.Unmarshal(call.Arguments, &input)
	if slices.ContainsFunc(input.Names, func(name string) bool { return strings.TrimSpace(name) == id }) {
		return ToolLoadedWhyNames
	}
	return ToolLoadedWhyQuery
}
