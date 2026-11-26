package gotato

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// toolCatalog is the catalog side of a toolRegistry: the installed ToolSets,
// the Tools they resolved, which of them are loaded, and the Effect cap and
// filter that hide a Tool.
type toolCatalog struct {
	sets      []*toolSetState
	catalog   []catalogTool
	byCatalog map[string]*catalogTool
	// loaded are the qualified IDs of the loaded catalog Tools, in load order.
	loaded []string
	// parked are the loaded Tools of a ToolSet that is unavailable now. They
	// are not offered, and they come back when the ToolSet does. Only a
	// Transcript that declares no Loaded needs them; a Record remembers.
	parked []string
	filter func(string) bool
	// kept is the filter's answer for every catalog Tool ID, asked outside
	// the locks when the catalog or the root surface is resolved (screen).
	kept map[string]bool
	// maxEffect hides every Tool above it; ToolEffectUnspecified means no cap.
	maxEffect ToolEffect
}

// installedToolSets names the ToolSets of configs, in install order. A
// ToolSet whose Spec fails, or whose name is empty, holds a dot or repeats
// another's, fails them all.
func installedToolSets(configs []toolSetConfig) ([]*toolSetState, error) {
	names := map[string]bool{}
	var sets []*toolSetState
	for _, entry := range configs {
		spec, err := toolSetSpec(entry.set)
		if err != nil {
			return nil, err
		}
		// Normalize before the uniqueness check so "files" and " files " are
		// the same ToolSet and qualified Tool IDs never contain stray spaces.
		spec.Name = strings.TrimSpace(spec.Name)
		if spec.Name == "" {
			return nil, runtimeError(CodeInvalidArgument, "ToolSet", "ToolSet name is empty", nil)
		}
		if strings.Contains(spec.Name, ".") {
			return nil, runtimeError(CodeInvalidArgument, "ToolSet", "ToolSet name cannot contain a dot: "+spec.Name, nil)
		}
		if names[spec.Name] {
			return nil, runtimeError(CodeInvalidArgument, "ToolSet", "duplicate ToolSet name: "+spec.Name, nil)
		}
		names[spec.Name] = true
		sets = append(sets, &toolSetState{set: entry.set, spec: spec, mode: entry.mode})
	}
	return sets, nil
}

// catalogIDs lists the qualified IDs of the catalog that results resolve, or
// of the catalog as it is when results is nil. Only the updater calls it.
func (c *toolCatalog) catalogIDs(results []setResult) []string {
	if results == nil {
		ids := make([]string, 0, len(c.catalog))
		for i := range c.catalog {
			ids = append(ids, c.catalog[i].id)
		}
		return ids
	}
	var ids []string
	for _, res := range results {
		for _, entry := range res.tools {
			ids = append(ids, qualify(res.state.spec.Name, entry.spec.ID))
		}
	}
	return ids
}

// screen asks the WithToolFilter keep about every ID, with no lock held. A
// Session without a filter keeps every Tool, and screen returns nil.
func (c *toolCatalog) screen(ids []string) (map[string]bool, error) {
	if c.filter == nil {
		return nil, nil
	}
	kept := make(map[string]bool, len(ids))
	err := callHost("WithToolFilter", func() error {
		for _, id := range ids {
			kept[id] = c.filter(id)
		}
		return nil
	})
	// [FAILURE-CLASS] a filter that panics fails the Run, loudly, rather than change the Tool surface unseen.
	if err != nil {
		return nil, runtimeError(CodeExtensionFailure, "WithToolFilter", PublicText(err, "WithToolFilter failed"), err)
	}
	return kept, nil
}

// refused reports whether the filter switched the catalog Tool id off.
func (c *toolCatalog) refused(id string) bool {
	return c.filter != nil && !c.kept[id]
}

// install replaces the catalog with what results resolved. A qualified ID is
// unique: intake refuses a ToolSet that repeats a Tool ID, and a ToolSet's
// name holds no dot. The caller holds mu, or is constructing.
func (c *toolCatalog) install(results []setResult) {
	for _, res := range results {
		res.state.resolved, res.state.failure = res.tools, res.err
	}
	c.catalog = c.catalog[:0:0]
	c.byCatalog = map[string]*catalogTool{}
	for _, state := range c.sets {
		for _, resolved := range state.resolved {
			id := qualify(state.spec.Name, resolved.spec.ID)
			visible := cloneToolSpec(resolved.spec)
			visible.ID = id
			if visible.Name == "" {
				visible.Name = resolved.spec.ID
			}
			c.catalog = append(c.catalog, catalogTool{id: id, tool: resolved.tool, spec: visible, set: state})
		}
	}
	sort.SliceStable(c.catalog, func(i, j int) bool { return c.catalog[i].id < c.catalog[j].id })
	for i := range c.catalog {
		c.byCatalog[c.catalog[i].id] = &c.catalog[i]
	}
}

// settle sorts the loaded Tools ids against the catalog: parked are those of
// a ToolSet that is unavailable now, and report names the Tools that are gone
// and the ToolSets that are unavailable.
func (c *toolCatalog) settle(ids []string) (report setReport, parked []string) {
	for _, id := range ids {
		name, _, _ := strings.Cut(id, ".")
		state := c.setNamed(name)
		if state == nil || state.mode == modeActive {
			continue
		}
		switch {
		case state.failure != nil:
			addUnique(&parked, id)
		case c.byCatalog[id] == nil:
			addUnique(&report.gone, id)
		}
	}
	report.unavailable = c.unavailableNames()
	return report, parked
}

// unavailableNames lists the names of the ToolSets that did not resolve, in
// install order.
func (c *toolCatalog) unavailableNames() []string {
	var names []string
	for _, state := range c.sets {
		if state.failure != nil {
			names = append(names, state.spec.Name)
		}
	}
	return names
}

func (c *toolCatalog) setNamed(name string) *toolSetState {
	for _, state := range c.sets {
		if state.spec.Name == name {
			return state
		}
	}
	return nil
}

// known keeps the IDs that name a catalog Tool the Model can load: one that an
// installed ToolSet carries and that is not installed active.
func (c *toolCatalog) known(ids []string) []string {
	var out []string
	for _, id := range ids {
		entry, ok := c.byCatalog[id]
		if !ok || entry.set.mode == modeActive || slices.Contains(out, id) {
			continue
		}
		out = append(out, id)
	}
	return out
}

// hidden reports whether the Effect cap keeps a Tool from the Model.
func (c *toolCatalog) hidden(spec ToolSpec) bool {
	return c.maxEffect != ToolEffectUnspecified && !spec.Effect.Within(c.maxEffect)
}

// indexable reports whether the Model may find and load a catalog Tool: the
// Effect cap and the filter decide. No static Tool holds a catalog Tool's ID,
// since none may take a name under a ToolSet's namespace (rootName).
func (c *toolCatalog) indexable(entry *catalogTool) bool {
	return !c.hidden(entry.spec) && !c.refused(entry.id)
}

// catalogVisible reports whether a catalog Tool is offered: every Tool of an
// active ToolSet that the cap and the filter let through, and a loaded Tool
// the Model may find.
func (c *toolCatalog) catalogVisible(entry *catalogTool) bool {
	if entry.set.mode == modeActive {
		return !c.hidden(entry.spec) && !c.refused(entry.id)
	}
	return c.indexable(entry) && slices.Contains(c.loaded, entry.id)
}

func qualify(namespace, local string) string {
	if namespace == "" {
		return local
	}
	return namespace + "." + local
}

func (c *toolCatalog) unavailableSetLocked(id string) string {
	name, _, dotted := strings.Cut(id, ".")
	if !dotted {
		return ""
	}
	if state := c.setNamed(name); state != nil && state.failure != nil {
		return name
	}
	return ""
}

// catalogText is the description of search_tools: each catalog ToolSet by what
// its install mode shows. It lists a loaded Tool like any other, so the text
// does not change while Tools load. The caller holds mu or is constructing.
func (c *toolCatalog) catalogText() string {
	var b strings.Builder
	b.WriteString("Load tools before you call them. Pass names to load tools exactly, queries to search, or toolset to list a folded ToolSet.\n")
	for _, state := range c.sets {
		if state.mode == modeActive {
			continue
		}
		var ids []string
		for i := range c.catalog {
			entry := &c.catalog[i]
			if entry.set == state && c.indexable(entry) {
				ids = append(ids, entry.id)
			}
		}
		writeCatalogEntry(&b, state, ids)
	}
	return b.String()
}

// writeCatalogEntry writes the catalog entry of a ToolSet that is not active,
// whose indexable Tools are ids, as its install mode shows it.
func writeCatalogEntry(b *strings.Builder, state *toolSetState, ids []string) {
	unavailable := state.failure != nil
	if len(ids) == 0 && !unavailable {
		return
	}
	description := strings.TrimSpace(state.spec.Description)
	b.WriteString("\n")
	b.WriteString(state.spec.Name)
	if description != "" {
		b.WriteString(" — ")
		b.WriteString(description)
	}
	if unavailable {
		b.WriteString(" (unavailable now; no tools)\n")
		return
	}
	if state.mode == modeFolded {
		fmt.Fprintf(b, " (%d tools; pass toolset=%q to list them)\n", len(ids), state.spec.Name)
		return
	}
	b.WriteString("\n  ")
	b.WriteString(strings.Join(ids, ", "))
	b.WriteString("\n")
}
