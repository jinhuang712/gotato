package gotato

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"unicode"
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
func WithToolSearcher(searcher ToolSearcher) Option {
	return func(c *sessionConfig) error {
		if searcher == nil {
			return runtimeError(ErrInvalidArgument, "WithToolSearcher", "ToolSearcher is nil", nil)
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
		Effect: EffectRead,
	}
}

func (t *searchTool) Execute(ctx context.Context, use ToolUse, progress ToolProgressFunc) (ToolResult, error) {
	var input struct {
		Names   []string `json:"names"`
		Queries []string `json:"queries"`
		Toolset string   `json:"toolset"`
	}
	if err := json.Unmarshal(use.ArgumentsJSON, &input); err != nil {
		return ToolResult{}, runtimeError(ErrToolArgumentFailure, SearchToolName, "cannot decode search arguments", err)
	}
	if len(input.Names) == 0 && len(input.Queries) == 0 && strings.TrimSpace(input.Toolset) == "" {
		return ToolResult{}, runtimeError(ErrToolArgumentFailure, SearchToolName, "pass names, queries or toolset", nil)
	}
	var loaded, already, notFound, found []string
	add := func(list *[]string, id string) {
		if !slices.Contains(*list, id) {
			*list = append(*list, id)
		}
	}
	for _, name := range input.Names {
		name = strings.TrimSpace(name)
		switch t.registry.stageLoad(name, ToolLoadedNames) {
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
				if t.registry.stageLoad(id, ToolLoadedQuery) == loadStaged {
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
		Status:  ToolResultOK,
		Content: []ContentPart{{Kind: ContentText, Text: strings.Join(parts, " ")}},
	}, nil
}

// rank asks the searcher about one query. A panic or an error is a failure of
// the call, which the Model reads as a failed Tool Result.
func (r *toolRegistry) rank(ctx context.Context, query string) (ids []string, err error) {
	candidates := r.candidates()
	if len(candidates) == 0 || strings.TrimSpace(query) == "" {
		return nil, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = runtimeError(ErrExtensionFailure, SearchToolName, fmt.Sprintf("ToolSearcher panicked: %v", recovered), nil)
		}
	}()
	ranked, searchErr := r.searcher.Search(ctx, query, candidates)
	if searchErr != nil {
		return nil, runtimeError(ErrExtensionFailure, SearchToolName, "ToolSearcher failed: "+searchErr.Error(), searchErr)
	}
	known := make(map[string]bool, len(candidates))
	for _, spec := range candidates {
		known[spec.ID] = true
	}
	out := make([]string, 0, len(ranked))
	for _, id := range ranked {
		if known[id] && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out, nil
}

// describeSet lists a catalog ToolSet's Tools with their descriptions, and
// loads none.
func (r *toolRegistry) describeSet(name string) string {
	specs, loaded, ok := r.setTools(name)
	if !ok {
		return "Not found: toolset " + name + "."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s has %d tools:", name, len(specs))
	for i, spec := range specs {
		b.WriteString("\n" + spec.ID)
		if first := firstLine(spec.Description); first != "" {
			b.WriteString(" — " + first)
		}
		if loaded[i] {
			b.WriteString(" (loaded)")
		}
	}
	return b.String()
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = strings.TrimSpace(text[:i])
	}
	return text
}

// bm25Searcher is the built-in ToolSearcher: BM25 over each Tool's ID and
// description. A term in the ID counts three times as much as one in the
// description.
type bm25Searcher struct{}

const (
	bm25K1       = 1.2
	bm25B        = 0.75
	bm25IDWeight = 3
)

func (bm25Searcher) Search(_ context.Context, query string, tools []ToolSpec) ([]string, error) {
	terms := uniqueTerms(tokenize(query))
	if len(terms) == 0 || len(tools) == 0 {
		return nil, nil
	}
	type document struct {
		id     string
		counts map[string]float64
		length float64
	}
	docs := make([]document, len(tools))
	var total float64
	for i, spec := range tools {
		doc := document{id: spec.ID, counts: map[string]float64{}}
		for _, term := range tokenize(spec.ID) {
			doc.counts[term] += bm25IDWeight
			doc.length += bm25IDWeight
		}
		for _, term := range tokenize(spec.Description) {
			doc.counts[term]++
			doc.length++
		}
		total += doc.length
		docs[i] = doc
	}
	average := total / float64(len(docs))
	if average == 0 {
		return nil, nil
	}
	n := float64(len(docs))
	scores := make([]float64, len(docs))
	for _, term := range terms {
		containing := 0.0
		for _, doc := range docs {
			if doc.counts[term] > 0 {
				containing++
			}
		}
		if containing == 0 {
			continue
		}
		idf := math.Log(1 + (n-containing+0.5)/(containing+0.5))
		for i, doc := range docs {
			tf := doc.counts[term]
			if tf == 0 {
				continue
			}
			scores[i] += idf * tf * (bm25K1 + 1) / (tf + bm25K1*(1-bm25B+bm25B*doc.length/average))
		}
	}
	order := make([]int, 0, len(docs))
	for i, score := range scores {
		if score > 0 {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool {
		if scores[order[a]] != scores[order[b]] {
			return scores[order[a]] > scores[order[b]]
		}
		return docs[order[a]].id < docs[order[b]].id
	})
	out := make([]string, len(order))
	for i, index := range order {
		out[i] = docs[index].id
	}
	return out, nil
}

// tokenize lower-cases text and splits it at every character that is not a
// letter or a digit, so "orders.list_open" is "orders", "list", "open".
func tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func uniqueTerms(terms []string) []string {
	var out []string
	for _, term := range terms {
		if !slices.Contains(out, term) {
			out = append(out, term)
		}
	}
	return out
}
