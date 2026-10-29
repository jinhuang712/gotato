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

// toolRegistry owns Tool identity and visibility for one Session. The Session
// goroutine loads a Tool when the Tool Result that lists it commits;
// search_tools only reads it, from its own goroutine.
type toolRegistry struct {
	rootTools   []toolEntry
	sources     []ToolSource
	sourceTools []toolEntry
	sets        []*toolSetState
	catalog     []catalogTool
	byCatalog   map[string]*catalogTool
	byQualified map[string]toolEntry
	specs       []ToolSpec
	// loaded are the qualified IDs of the loaded catalog Tools, in load order.
	loaded []string
	// parked are the loaded Tools of a ToolSet that is unavailable now. They
	// are not offered, and they come back when the ToolSet does. Only a
	// Transcript that is not a LoadedToolSource needs them; a Record remembers.
	parked []string
	// fresh is true until the first Run: NewSession just resolved every
	// ToolSet, so that Run does not resolve them again.
	fresh    bool
	search   *searchTool
	searcher ToolSearcher
	filter   func(string) bool
	// kept is the filter's answer for every catalog Tool ID, asked outside
	// the locks when the catalog or the root surface is resolved (screen).
	kept map[string]bool
	// maxEffect hides every Tool above it; ToolEffectUnspecified means no cap.
	maxEffect ToolEffect
	// mu guards sets, loaded, kept, specs, and byQualified. The only updater
	// is the holder of the Session's admission token: it asks the host
	// (ToolSources, ToolSets, the filter) with no lock held, takes in what it
	// learned through intake, where every check lives, and takes the locks
	// only to swap it in, which cannot fail. It holds the Session's
	// registryMu, the lock ToolInspector.Tools reads under, around mu, the
	// inner lock. Every reader on another goroutine, such as search_tools,
	// takes mu.
	mu sync.Mutex
}

func newToolRegistry(ctx context.Context, cfg *sessionConfig) (*toolRegistry, error) {
	registry := &toolRegistry{
		sources:   cfg.toolSources,
		maxEffect: cfg.maxEffect,
		filter:    cfg.toolFilter,
		searcher:  cfg.toolSearcher,
		byCatalog: map[string]*catalogTool{},
	}
	if registry.searcher == nil {
		registry.searcher = bm25Searcher{}
	}
	registry.search = &searchTool{registry: registry}
	names := map[string]bool{}
	for _, entry := range cfg.toolSets {
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
		registry.sets = append(registry.sets, &toolSetState{set: entry.set, spec: spec, mode: entry.mode})
	}
	// The root surface is taken in once the ToolSets are named, so it cannot
	// take one of their names.
	var err error
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
	if source, ok := cfg.transcript.(LoadedToolSource); ok {
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

// catalogIDs lists the qualified IDs of the catalog that results resolve, or
// of the catalog as it is when results is nil. Only the updater calls it.
func (r *toolRegistry) catalogIDs(results []setResult) []string {
	if results == nil {
		ids := make([]string, 0, len(r.catalog))
		for i := range r.catalog {
			ids = append(ids, r.catalog[i].id)
		}
		return ids
	}
	var ids []string
	for _, res := range results {
		for _, entry := range res.tools {
			ids = append(ids, r.qualify(res.state.spec.Name, entry.spec.ID))
		}
	}
	return ids
}

// screen asks the WithToolFilter keep about every ID, with no lock held. A
// Session without a filter keeps every Tool, and screen returns nil.
func (r *toolRegistry) screen(ids []string) (map[string]bool, error) {
	if r.filter == nil {
		return nil, nil
	}
	kept := make(map[string]bool, len(ids))
	err := callHost("WithToolFilter", func() error {
		for _, id := range ids {
			kept[id] = r.filter(id)
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
func (r *toolRegistry) refused(id string) bool {
	return r.filter != nil && !r.kept[id]
}

// install replaces the catalog with what results resolved. A qualified ID is
// unique: intake refuses a ToolSet that repeats a Tool ID, and a ToolSet's
// name holds no dot. The caller holds mu, or is constructing.
func (r *toolRegistry) install(results []setResult) {
	for _, res := range results {
		res.state.resolved, res.state.failure = res.tools, res.err
	}
	r.catalog = r.catalog[:0:0]
	r.byCatalog = map[string]*catalogTool{}
	for _, state := range r.sets {
		for _, resolved := range state.resolved {
			id := r.qualify(state.spec.Name, resolved.spec.ID)
			visible := cloneToolSpec(resolved.spec)
			visible.ID = id
			if visible.Name == "" {
				visible.Name = resolved.spec.ID
			}
			r.catalog = append(r.catalog, catalogTool{id: id, tool: resolved.tool, spec: visible, set: state})
		}
	}
	sort.SliceStable(r.catalog, func(i, j int) bool { return r.catalog[i].id < r.catalog[j].id })
	for i := range r.catalog {
		r.byCatalog[r.catalog[i].id] = &r.catalog[i]
	}
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
// Transcript's loaded list, read before the lock (a LoadedToolSource), so every
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
	var report setReport
	var parked []string
	for _, id := range ids {
		name, _, _ := strings.Cut(id, ".")
		state := r.setNamed(name)
		if state == nil || state.mode == modeActive {
			continue
		}
		switch {
		case state.failure != nil:
			if !slices.Contains(parked, id) {
				parked = append(parked, id)
			}
		case r.byCatalog[id] == nil:
			if !slices.Contains(report.gone, id) {
				report.gone = append(report.gone, id)
			}
		}
	}
	for _, state := range r.sets {
		if state.failure != nil {
			report.unavailable = append(report.unavailable, state.spec.Name)
		}
	}
	r.loaded, r.parked = r.known(ids), parked
	r.rebuild()
	return report
}

func (r *toolRegistry) setNamed(name string) *toolSetState {
	for _, state := range r.sets {
		if state.spec.Name == name {
			return state
		}
	}
	return nil
}

// known keeps the IDs that name a catalog Tool the Model can load: one that an
// installed ToolSet carries and that is not installed active.
func (r *toolRegistry) known(ids []string) []string {
	var out []string
	for _, id := range ids {
		entry, ok := r.byCatalog[id]
		if !ok || entry.set.mode == modeActive || slices.Contains(out, id) {
			continue
		}
		out = append(out, id)
	}
	return out
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

// hidden reports whether the Effect cap keeps a Tool from the Model.
func (r *toolRegistry) hidden(spec ToolSpec) bool {
	return r.maxEffect != ToolEffectUnspecified && !spec.Effect.Within(r.maxEffect)
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

// indexable reports whether the Model may find and load a catalog Tool: the
// Effect cap and the filter decide. No static Tool holds a catalog Tool's ID,
// since none may take a name under a ToolSet's namespace (rootName).
func (r *toolRegistry) indexable(entry *catalogTool) bool {
	return !r.hidden(entry.spec) && !r.refused(entry.id)
}

// rebuild recomputes the visible Tool set. Ordering is deterministic: Tools
// sort by qualified ID. A Tool above the Effect cap is left out entirely, so
// it is neither offered nor resolvable by any name. It cannot fail: intake
// checked every Tool, and no two of them share an ID (rootName, install).
func (r *toolRegistry) rebuild() {
	byQualified := map[string]toolEntry{}
	specs := make([]ToolSpec, 0, len(r.rootTools))
	register := func(qualified string, tool Tool, spec ToolSpec) {
		visible := cloneToolSpec(spec)
		visible.ID = qualified
		if visible.Name == "" {
			visible.Name = spec.ID
		}
		byQualified[qualified] = toolEntry{tool: tool, spec: visible}
		specs = append(specs, visible)
	}
	for _, entry := range r.staticTools() {
		if r.hidden(entry.spec) {
			continue
		}
		register(entry.spec.ID, entry.tool, entry.spec)
	}
	// search_tools stays while a catalog ToolSet is installed, even one that
	// is unavailable now or whose Tools are all hidden: the Model reads the
	// truth from it, and the Tool list stays the same.
	hasCatalog := slices.ContainsFunc(r.sets, func(state *toolSetState) bool { return state.mode != modeActive })
	for i := range r.catalog {
		entry := &r.catalog[i]
		if entry.set.mode == modeActive {
			if r.hidden(entry.spec) || r.refused(entry.id) {
				continue
			}
			register(entry.id, entry.tool, entry.spec)
			continue
		}
		if r.indexable(entry) && slices.Contains(r.loaded, entry.id) {
			register(entry.id, entry.tool, entry.spec)
		}
	}
	if hasCatalog {
		r.search.setDescription(r.catalogText())
		register(r.qualify("", SearchToolName), r.search, r.search.Spec())
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	r.byQualified = byQualified
	r.specs = specs
	// A local Tool name stays reachable when it is unambiguous, so a Model
	// that answers with the bare name still resolves.
	for _, entry := range r.staticTools() {
		if r.hidden(entry.spec) {
			continue
		}
		for _, alias := range []string{entry.spec.ID, entry.spec.Name} {
			if alias == "" {
				continue
			}
			if _, taken := r.byQualified[alias]; !taken {
				r.byQualified[alias] = entry
			}
		}
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

func (r *toolRegistry) hasSources() bool { return len(r.sources) > 0 }

// collectSources asks every ToolSource for its Tools and takes them in. A
// failure, a panic or a bad Tool of any ToolSource fails them all together, as
// one source. It asks them concurrently and takes no lock, as resolveAll does:
// a ToolSource may be slow. The Tools keep the order of the ToolSources.
func (r *toolRegistry) collectSources(ctx context.Context) ([]toolEntry, error) {
	supplied := make([][]Tool, len(r.sources))
	failures := make([]error, len(r.sources))
	var wg sync.WaitGroup
	for i, source := range r.sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			failures[i] = callHost("ToolSource", func() (failure error) {
				supplied[i], failure = source.Tools(ctx)
				return failure
			})
		}()
	}
	wg.Wait()
	for _, sourceErr := range failures {
		if sourceErr == nil {
			continue
		}
		if failure := contextFailure(ctx, "ToolSource"); failure != nil {
			return nil, failure
		}
		return nil, runtimeError(CodeToolResolutionFailure, "ToolSource", PublicText(sourceErr, "ToolSource failed"), sourceErr)
	}
	return intake("ToolSource", slices.Concat(supplied...), r.rootTools, r.rootName)
}

// rootName refuses a name a Tool of the root surface (WithTools, a ToolSource)
// cannot take, by its ID or its Name: search_tools, or a name under the
// namespace of an installed ToolSet. A dotted name of your own, outside every
// namespace, is allowed.
// [CONTRACT] a name the Model calls resolves to one Tool: a root Tool can neither collide with a ToolSet's Tool nor capture a call meant for it.
func (r *toolRegistry) rootName(spec ToolSpec) error {
	for _, name := range []string{spec.ID, spec.Name} {
		if name == SearchToolName {
			return runtimeError(CodeInvalidArgument, "Tool", "Tool name "+SearchToolName+" is reserved", nil)
		}
		for _, state := range r.sets {
			if strings.HasPrefix(name, state.spec.Name+".") {
				return runtimeError(CodeInvalidArgument, "Tool", "Tool name "+name+" is in the namespace of ToolSet "+state.spec.Name, nil)
			}
		}
	}
	return nil
}

// refreshSources swaps in the Tools collectSources took in and the filter's
// answer kept, and rebuilds visibility.
func (r *toolRegistry) refreshSources(entries []toolEntry, kept map[string]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sourceTools, r.kept = entries, kept
	r.rebuild()
}

func (r *toolRegistry) qualify(namespace, local string) string {
	if namespace == "" {
		return local
	}
	return namespace + "." + local
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

func (r *toolRegistry) unavailableSetLocked(id string) string {
	name, _, dotted := strings.Cut(id, ".")
	if !dotted {
		return ""
	}
	if state := r.setNamed(name); state != nil && state.failure != nil {
		return name
	}
	return ""
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

// catalogText is the description of search_tools: each catalog ToolSet by what
// its install mode shows. It lists a loaded Tool like any other, so the text
// does not change while Tools load. The caller holds mu or is constructing.
func (r *toolRegistry) catalogText() string {
	var b strings.Builder
	b.WriteString("Load tools before you call them. Pass names to load tools exactly, queries to search, or toolset to list a folded ToolSet.\n")
	for _, state := range r.sets {
		if state.mode == modeActive {
			continue
		}
		var ids []string
		for i := range r.catalog {
			entry := &r.catalog[i]
			if entry.set == state && r.indexable(entry) {
				ids = append(ids, entry.id)
			}
		}
		unavailable := state.failure != nil
		if len(ids) == 0 && !unavailable {
			continue
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
			continue
		}
		if state.mode == modeFolded {
			fmt.Fprintf(&b, " (%d tools; pass toolset=%q to list them)\n", len(ids), state.spec.Name)
			continue
		}
		b.WriteString("\n  ")
		b.WriteString(strings.Join(ids, ", "))
		b.WriteString("\n")
	}
	return b.String()
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
