package gotato

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
)

// catalogTool is one Tool of an installed ToolSet under its qualified ID.
type catalogTool struct {
	id   string
	tool Tool
	// spec is the visible spec: the qualified ID, and a Name that is never empty.
	spec ToolSpec
	set  *toolSetState
}

// sessionTools is the Session's Tools: the registry of what is installed and
// loaded, and the lock the visibility of a Tool changes under. mu is the lock
// ToolInspector.Tools reads under, around the registry's own: the holder of the
// Session's admission token takes it only to swap in what it learned from the
// host, which cannot fail. It is held with no other lock of the Session, and no
// host call is made under it.
type sessionTools struct {
	mu       sync.Mutex
	registry *toolRegistry
}

// toolRegistry owns Tool identity and visibility for one Session. The Session
// goroutine loads a Tool when the Tool Result that lists it commits;
// search_tools only reads it, from its own goroutine.
type toolRegistry struct {
	toolCatalog
	rootTools   []toolEntry
	sources     []ToolSource
	sourceTools []toolEntry
	byQualified map[string]toolEntry
	specs       []ToolSpec
	// fresh is true until the first Run: NewSession just resolved every
	// ToolSet, so that Run does not resolve them again.
	fresh    bool
	search   *searchTool
	searcher ToolSearcher
	// mu guards sets, loaded, kept, specs, and byQualified. The only updater
	// is the holder of the Session's admission token: it asks the host
	// (ToolSources, ToolSets, the filter) with no lock held, takes in what it
	// learned through intake, where every check lives, and takes the locks
	// only to swap it in, which cannot fail. It holds the Session's
	// tools lock (sessionTools.mu), the lock ToolInspector.Tools reads under,
	// around mu, the inner lock. Every reader on another goroutine, such as search_tools,
	// takes mu.
	mu sync.Mutex
}

func newToolRegistry(ctx context.Context, cfg *sessionConfig, caps Capabilities) (*toolRegistry, error) {
	registry := &toolRegistry{
		toolCatalog: toolCatalog{
			maxEffect: cfg.maxEffect,
			filter:    cfg.toolFilter,
			byCatalog: map[string]*catalogTool{},
		},
		sources:  cfg.toolSources,
		searcher: cfg.toolSearcher,
	}
	if registry.searcher == nil {
		registry.searcher = bm25Searcher{}
	}
	registry.search = &searchTool{registry: registry}
	var err error
	if registry.sets, err = installedToolSets(cfg.toolSets); err != nil {
		return nil, err
	}
	// The root surface is taken in once the ToolSets are named, so it cannot
	// take one of their names.
	if registry.rootTools, err = intake("WithTools", cfg.tools, nil, registry.rootName); err != nil {
		return nil, err
	}
	if registry.sourceTools, err = registry.collectSources(ctx); err != nil {
		return nil, err
	}
	// Resolving every ToolSet during construction keeps the catalog and the
	// first Model request deterministic. A ToolSet that fails is unavailable,
	// not fatal: the Session starts and its other Tools work.
	results, err := registry.resolveAll(ctx)
	if err != nil {
		return nil, err
	}
	if registry.kept, err = registry.screen(registry.catalogIDs(results)); err != nil {
		return nil, err
	}
	registry.install(results)
	registry.fresh = true
	if source := caps.Loaded; source != nil {
		recorded, err := loadedTools(source)
		if err != nil {
			return nil, err
		}
		registry.loaded = registry.known(recorded)
	}
	registry.rebuild()
	return registry, nil
}

// loadedTools reads the loaded Tools the Transcript lists. It is called with no
// lock held; a panic in it fails the Run, with a code.
// [LIVENESS] a panic in the host's LoadedTools would end the Session goroutine, or leave the registry lock held.
func loadedTools(source LoadedToolSource) (ids []string, err error) {
	if failure := transcriptCall(func() { ids = source.LoadedTools() }); failure != nil {
		return nil, failure
	}
	return ids, nil
}

// setResult is one ToolSet's answer to a resolution.
type setResult struct {
	state *toolSetState
	tools []toolEntry
	err   error
}

// resolveAll asks every installed ToolSet for its Tools. A ToolSet that fails
// is reported in its result, never as an error; only a context that ended
// fails the call, so a canceled caller gets no half-resolved Session. After
// NewSession's own resolution it returns nil for the first Run, which uses
// that answer. It resolves the ToolSets concurrently and takes no lock: a
// ToolSet may be slow.
func (r *toolRegistry) resolveAll(ctx context.Context) ([]setResult, error) {
	if r.fresh {
		r.fresh = false
		return nil, nil
	}
	// Every ToolSet resolves at once, so a Run waits for the slowest ToolSet,
	// not the sum. Each answer lands in its own slot, in install order, so the
	// catalog bytes never depend on which ToolSet answered first.
	results := make([]setResult, len(r.sets))
	var wg sync.WaitGroup
	for i, state := range r.sets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tools, err := resolveToolSet(ctx, state)
			results[i] = setResult{state: state, tools: tools, err: err}
		}()
	}
	wg.Wait()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, runtimeError(codeForContext(ctxErr), "ToolSet", "context ended while resolving ToolSets", ctxErr)
	}
	return results, nil
}

// setReport is what a Run's start learned about the ToolSets.
type setReport struct {
	// unavailable are the names of the ToolSets that did not resolve.
	unavailable []string
	// gone are the loaded Tools their ToolSet no longer carries.
	gone []string
}

// apply installs a resolution (nil results keep the catalog as it is) and
// the filter's answer kept for it, then settles the loaded Tools against it. recorded, when not nil, is the
// Transcript's loaded list, read before the lock (Capabilities.Loaded), so every
// instance decides from the Record. A loaded Tool whose ToolSet answered
// without it is gone: it leaves the loaded Tools, and the caller stores a
// tool_unloaded Event so the Record says so too. A loaded Tool of a ToolSet
// that is unavailable is only hidden, since the ToolSet said nothing about it.
func (r *toolRegistry) apply(results []setResult, kept map[string]bool, recorded *[]string) setReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kept = kept
	if results != nil {
		r.install(results)
	}
	ids := slices.Concat(r.loaded, r.parked)
	if recorded != nil {
		ids = *recorded
	}
	report, parked := r.settle(ids)
	r.loaded, r.parked = r.known(ids), parked
	r.rebuild()
	return report
}

// syncLoaded sets the loaded Tools to ids, the Transcript's list after a
// rewrite such as a compaction. A Tool installed active is never among them.
func (r *toolRegistry) syncLoaded(ids []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := r.known(ids)
	if slices.Equal(next, r.loaded) {
		return
	}
	r.loaded = next
	r.rebuild()
}

// cappedEffect reports whether a Tool carries the qualified ID id and the
// Effect cap hides it, and the Effect it declares. A Tool the filter refuses is
// not reported: the filter is how a Session keeps a Tool's existence quiet.
func (r *toolRegistry) cappedEffect(id string) (ToolEffect, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, entry := range r.staticTools() {
		if entry.spec.ID == id && r.hidden(entry.spec) {
			return entry.spec.Effect, true
		}
	}
	if entry, ok := r.byCatalog[id]; ok && r.hidden(entry.spec) {
		return entry.spec.Effect, true
	}
	return ToolEffectUnspecified, false
}

// visibleTools is the visible Tool set as rebuild gathers it.
type visibleTools struct {
	byQualified map[string]toolEntry
	specs       []ToolSpec
}

// add makes a Tool visible under its qualified ID, with a Name that is never empty.
func (v *visibleTools) add(qualified string, tool Tool, spec ToolSpec) {
	visible := cloneToolSpec(spec)
	visible.ID = qualified
	if visible.Name == "" {
		visible.Name = spec.ID
	}
	v.byQualified[qualified] = toolEntry{tool: tool, spec: visible}
	v.specs = append(v.specs, visible)
}

// rebuild recomputes the visible Tool set. Ordering is deterministic: Tools
// sort by qualified ID. A Tool above the Effect cap is left out entirely, so
// it is neither offered nor resolvable by any name. It cannot fail: intake
// checked every Tool, and no two of them share an ID (rootName, install).
func (r *toolRegistry) rebuild() {
	visible := visibleTools{byQualified: map[string]toolEntry{}, specs: make([]ToolSpec, 0, len(r.rootTools))}
	for _, entry := range r.staticTools() {
		if r.hidden(entry.spec) {
			continue
		}
		visible.add(entry.spec.ID, entry.tool, entry.spec)
	}
	// search_tools stays while a catalog ToolSet is installed, even one that
	// is unavailable now or whose Tools are all hidden: the Model reads the
	// truth from it, and the Tool list stays the same.
	hasCatalog := slices.ContainsFunc(r.sets, func(state *toolSetState) bool { return state.mode != modeActive })
	for i := range r.catalog {
		entry := &r.catalog[i]
		if r.catalogVisible(entry) {
			visible.add(entry.id, entry.tool, entry.spec)
		}
	}
	if hasCatalog {
		r.search.setDescription(r.catalogText())
		visible.add(qualify("", SearchToolName), r.search, r.search.Spec())
	}
	specs := visible.specs
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	r.byQualified = visible.byQualified
	r.specs = specs
	r.aliasStatic()
}

// aliasStatic lets the ID and the Name of every visible static Tool resolve
// too, where no visible Tool holds them already.
func (r *toolRegistry) aliasStatic() {
	// A local Tool name stays reachable when it is unambiguous, so a Model
	// that answers with the bare name still resolves.
	for _, entry := range r.staticTools() {
		if r.hidden(entry.spec) {
			continue
		}
		for _, alias := range []string{entry.spec.ID, entry.spec.Name} {
			r.alias(alias, entry)
		}
	}
}

// alias lets name resolve to entry, unless name is empty or taken.
func (r *toolRegistry) alias(name string, entry toolEntry) {
	if name == "" {
		return
	}
	if _, taken := r.byQualified[name]; !taken {
		r.byQualified[name] = entry
	}
}

// staticTools is the root surface: Tools installed with WithTools(s) followed
// by the current Tools of every ToolSource.
func (r *toolRegistry) staticTools() []toolEntry {
	if len(r.sourceTools) == 0 {
		return r.rootTools
	}
	return slices.Concat(r.rootTools, r.sourceTools)
}

// lookup returns the visible Tool the Model calls by id, with its Spec; ok is
// false when no visible Tool answers to id.
func (r *toolRegistry) lookup(id string) (entry toolEntry, ok bool) {
	entry, ok = r.byQualified[id]
	return entry, ok
}

func (r *toolRegistry) visibleSpecs() []ToolSpec { return cloneToolSpecs(r.specs) }

// loadStatus says whether a Tool can be loaded.
type loadStatus int

const (
	// loadOK: a catalog Tool the Session may load, not loaded yet.
	loadOK loadStatus = iota
	// loadAlready: the Tool is visible or loaded already.
	loadAlready
	// loadUnavailable: the ID is in the namespace of a ToolSet that is
	// unavailable now.
	loadUnavailable
	// loadNotFound: no Tool the Session may load carries that ID.
	loadNotFound
)

// loadable reports whether id names a catalog Tool the Model may load. It
// changes nothing: a Tool loads when the Tool Result that lists it commits
// (load). A Tool the Model sees already, a root Tool among them, is already
// available. A Tool the Effect cap or the filter refuses, or that no ToolSet
// carries, is not found, unless its ToolSet is unavailable now.
func (r *toolRegistry) loadable(id string) loadStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loadableLocked(id)
}

func (r *toolRegistry) loadableLocked(id string) loadStatus {
	if _, visible := r.byQualified[id]; visible {
		return loadAlready
	}
	entry, ok := r.byCatalog[id]
	if !ok && r.unavailableSetLocked(id) != "" {
		return loadUnavailable
	}
	if !ok || !r.indexable(entry) {
		return loadNotFound
	}
	if entry.set.mode == modeActive || slices.Contains(r.loaded, id) {
		return loadAlready
	}
	return loadOK
}

// unavailableSet names the installed ToolSet that is unavailable now and whose
// namespace holds id, or returns "". A ToolSet's name holds no dot, and no
// root Tool takes a name in its namespace (rootName), so the prefix names one
// ToolSet.
func (r *toolRegistry) unavailableSet(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.unavailableSetLocked(id)
}

// load makes a catalog Tool visible when the Tool Result that lists it
// commits, on the Session goroutine. It reports whether the Tool loaded now:
// false when it was loaded already, or the Session may not load it.
func (r *toolRegistry) load(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loadableLocked(id) != loadOK {
		return false
	}
	r.loaded = append(slices.Clone(r.loaded), id)
	r.rebuild()
	return true
}

// candidates lists, in ID order, the specs of the catalog Tools the Model may
// still load: not loaded, in the Session's index.
func (r *toolRegistry) candidates() []ToolSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []ToolSpec
	for i := range r.catalog {
		entry := &r.catalog[i]
		if entry.set.mode == modeActive || !r.indexable(entry) || slices.Contains(r.loaded, entry.id) {
			continue
		}
		out = append(out, cloneToolSpec(entry.spec))
	}
	return out
}

// catalogView reports the loaded catalog Tools (their IDs) and the specs of
// the catalog Tools still to load, both in ID order, for an adapter that
// defers tools natively. A Session without a catalog reports neither.
func (r *toolRegistry) catalogView() (loaded []string, unloaded []ToolSpec) {
	r.mu.Lock()
	for i := range r.catalog {
		entry := &r.catalog[i]
		if entry.set.mode == modeActive || !r.indexable(entry) {
			continue
		}
		if slices.Contains(r.loaded, entry.id) {
			loaded = append(loaded, entry.id)
		}
	}
	r.mu.Unlock()
	unloaded = r.candidates()
	sort.Strings(loaded)
	sort.Slice(unloaded, func(i, j int) bool { return unloaded[i].ID < unloaded[j].ID })
	return loaded, unloaded
}

// setTools lists the Tools of a catalog ToolSet the Model may load, with
// whether each is loaded, and whether the ToolSet is unavailable now. ok is
// false when no catalog ToolSet has that name.
func (r *toolRegistry) setTools(name string) (specs []ToolSpec, loaded []bool, unavailable, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, state := range r.sets {
		if state.spec.Name != name || state.mode == modeActive {
			continue
		}
		for i := range r.catalog {
			entry := &r.catalog[i]
			if entry.set != state || !r.indexable(entry) {
				continue
			}
			specs = append(specs, cloneToolSpec(entry.spec))
			loaded = append(loaded, slices.Contains(r.loaded, entry.id))
		}
		return specs, loaded, state.failure != nil, true
	}
	return nil, nil, false, false
}

// toolSetStatus reports every installed ToolSet and its Tools for ToolSets.
func (r *toolRegistry) toolSetStatus() []ToolSetStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ToolSetStatus, 0, len(r.sets))
	for _, state := range r.sets {
		status := ToolSetStatus{Name: state.spec.Name, Description: state.spec.Description, Unavailable: state.failure != nil}
		for i := range r.catalog {
			entry := &r.catalog[i]
			if entry.set != state || r.hidden(entry.spec) {
				continue
			}
			loaded := (state.mode == modeActive || slices.Contains(r.loaded, entry.id))
			status.Tools = append(status.Tools, ToolSetTool{Spec: cloneToolSpec(entry.spec), Loaded: loaded})
		}
		sort.Slice(status.Tools, func(i, j int) bool { return status.Tools[i].Spec.ID < status.Tools[j].Spec.ID })
		out = append(out, status)
	}
	return out
}

// rank asks the searcher about one query. A panic or an error is a failure of
// the call, which the Model reads as a failed Tool Result. A Tool in exclude,
// one this call loads already, is not ranked.
func (r *toolRegistry) rank(ctx context.Context, query string, exclude []string) (ids []string, err error) {
	candidates := slices.DeleteFunc(r.candidates(), func(spec ToolSpec) bool { return slices.Contains(exclude, spec.ID) })
	if len(candidates) == 0 || strings.TrimSpace(query) == "" {
		return nil, nil
	}
	var ranked []string
	searchErr := callHost("ToolSearcher", func() (failure error) {
		ranked, failure = r.searcher.Search(ctx, query, candidates)
		return failure
	})
	if searchErr != nil {
		return nil, runtimeError(CodeExtensionFailure, SearchToolName, PublicText(searchErr, "ToolSearcher failed"), searchErr)
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
	specs, loaded, unavailable, ok := r.setTools(name)
	if !ok {
		return "Not found: toolset " + name + "."
	}
	if unavailable {
		return name + " is unavailable now; try again in a later Run."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s has %d tools:", name, len(specs))
	for i, spec := range specs {
		b.WriteString("\n")
		b.WriteString(spec.ID)
		if first := firstLine(spec.Description); first != "" {
			b.WriteString(" — ")
			b.WriteString(first)
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
