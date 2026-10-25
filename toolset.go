package gotato

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
)

// ToolSetSpec identifies a ToolSet. The name must be stable and unique.
type ToolSetSpec struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// ToolSet is a named group of Tools from one source, such as an MCP server. It
// is a grouping, not the unit of loading: the Model loads its Tools one by one
// with search_tools, unless the ToolSet is installed active. Resolution must
// be deterministic, and a failure must not partially register the returned
// collection. A Tool's ID in the Session is "<set>.<tool>".
type ToolSet interface {
	Spec() ToolSetSpec
	Tools(context.Context) ([]Tool, error)
}

// installMode says how much of a ToolSet the Model sees.
type installMode int

const (
	// modeListed: the catalog shows the ToolSet's description and all its
	// Tool names. The default.
	modeListed installMode = iota
	// modeFolded: the catalog shows the description and a count of Tools.
	modeFolded
	// modeActive: every Tool is visible, as if it were a base Tool.
	modeActive
)

// WithToolSet installs a ToolSet whose catalog entry lists its description and
// all its Tool names. The Model loads the Tools it needs with search_tools.
func WithToolSet(set ToolSet) Option {
	return withToolSet(set, modeListed)
}

// WithFoldedToolSet installs a ToolSet whose catalog entry shows only its
// description and a count of its Tools. The Model lists them with the toolset
// parameter of search_tools, then loads by name.
func WithFoldedToolSet(set ToolSet) Option {
	return withToolSet(set, modeFolded)
}

// WithActiveToolSet installs a ToolSet whose Tools are visible always, as if
// they were base Tools. They are never in the catalog and never unloaded.
func WithActiveToolSet(set ToolSet) Option {
	return withToolSet(set, modeActive)
}

func withToolSet(set ToolSet, mode installMode) Option {
	return func(c *sessionConfig) error {
		if set == nil {
			return runtimeError(ErrInvalidArgument, "WithToolSet", "ToolSet is nil", nil)
		}
		c.toolSets = append(c.toolSets, toolSetConfig{set: set, mode: mode})
		return nil
	}
}

// WithToolFilter has the Session ask keep for the qualified ID of every Tool of
// a ToolSet before it indexes, lists or offers it. A Tool keep refuses is never
// in the catalog, never found by a search and never loaded, so a Session can
// switch a Tool off and the Model cannot search its way past it. keep runs on
// every rebuild of the Tool list, so it must be quick and safe to call from
// several goroutines.
func WithToolFilter(keep func(toolID string) bool) Option {
	return func(c *sessionConfig) error {
		c.toolFilter = keep
		return nil
	}
}

// WithRootNamespace qualifies individual root Tools under one namespace. The
// default is an empty namespace, where a root Tool keeps its declared ID.
func WithRootNamespace(namespace string) Option {
	return func(c *sessionConfig) error {
		namespace = strings.TrimSpace(namespace)
		if strings.Contains(namespace, ".") {
			return runtimeError(ErrInvalidArgument, "WithRootNamespace", "namespace cannot contain a dot", nil)
		}
		c.rootNamespace = namespace
		return nil
	}
}

// LoadedToolSource is an optional method set of a Transcript: the qualified IDs
// of the catalog Tools the Model loaded in earlier Runs of the Session, in the
// order it loaded them, with a compaction's unloads applied. NewSession loads
// those Tools again, so every instance that loads the Session from the same
// Record offers the same Tools; a session.Record implements it. An ID that no
// installed ToolSet carries any more stays unloaded.
type LoadedToolSource interface {
	LoadedTools() []string
}

type toolSetConfig struct {
	set  ToolSet
	mode installMode
}

// toolSetState tracks one installed ToolSet. Its Tools are resolved once, at
// NewSession, so the catalog is the same on every request of the Session.
type toolSetState struct {
	set      ToolSet
	spec     ToolSetSpec
	mode     installMode
	resolved []Tool
}

// catalogTool is one Tool of an installed ToolSet under its qualified ID.
type catalogTool struct {
	id   string
	tool Tool
	// spec is the visible spec: the qualified ID, and a Name that is never empty.
	spec ToolSpec
	set  *toolSetState
}

// pendingLoad is a load staged during the current batch.
type pendingLoad struct {
	id  string
	why ToolLoadedWhy
}

// toolRegistry owns Tool identity and visibility for one Session. The Session
// goroutine commits loads at batch boundaries; search_tools stages them from
// its own goroutine, so mu guards every mutation of that state.
type toolRegistry struct {
	rootNamespace string
	rootTools     []Tool
	sources       []ToolSource
	sourceTools   []Tool
	sets          []*toolSetState
	catalog       []catalogTool
	byCatalog     map[string]*catalogTool
	byQualified   map[string]Tool
	specs         []ToolSpec
	// loaded are the qualified IDs of the loaded catalog Tools, in load order.
	loaded []string
	// pending holds loads staged during the current batch. Visibility commits
	// at the batch boundary, never inside it.
	pending  []pendingLoad
	search   *searchTool
	searcher ToolSearcher
	filter   func(string) bool
	// maxEffect hides every Tool above it; EffectUnspecified means no cap.
	maxEffect ToolEffect
	// mu guards sets, pending, loaded, specs, and byQualified. Callers that
	// run on the Session goroutine hold the Session's registryMu, which is the
	// lock ToolInspector.Tools reads under; mu is the inner lock.
	mu sync.Mutex
}

func newToolRegistry(ctx context.Context, cfg *sessionConfig) (*toolRegistry, error) {
	registry := &toolRegistry{
		rootNamespace: cfg.rootNamespace,
		rootTools:     cfg.tools,
		sources:       cfg.toolSources,
		maxEffect:     cfg.maxEffect,
		filter:        cfg.toolFilter,
		searcher:      cfg.toolSearcher,
		byCatalog:     map[string]*catalogTool{},
	}
	if registry.searcher == nil {
		registry.searcher = bm25Searcher{}
	}
	registry.search = &searchTool{registry: registry}
	if err := registry.collectSources(); err != nil {
		return nil, err
	}
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
			return nil, runtimeError(ErrInvalidArgument, "ToolSet", "ToolSet name is empty", nil)
		}
		if strings.Contains(spec.Name, ".") {
			return nil, runtimeError(ErrInvalidArgument, "ToolSet", "ToolSet name cannot contain a dot: "+spec.Name, nil)
		}
		if names[spec.Name] {
			return nil, runtimeError(ErrInvalidArgument, "ToolSet", "duplicate ToolSet name: "+spec.Name, nil)
		}
		names[spec.Name] = true
		registry.sets = append(registry.sets, &toolSetState{set: entry.set, spec: spec, mode: entry.mode})
	}
	// Resolving every ToolSet during construction keeps the catalog and the
	// first Model request deterministic, and surfaces a broken ToolSet before
	// the Session accepts any command.
	for _, state := range registry.sets {
		tools, err := resolveToolSet(ctx, state)
		if ctxErr := ctx.Err(); ctxErr != nil {
			// A cancelled caller gets no half-resolved Session.
			return nil, runtimeError(codeForContext(ctxErr), "NewSession", "context ended while resolving ToolSets", ctxErr)
		}
		if err != nil {
			return nil, err
		}
		state.resolved = tools
		for _, tool := range tools {
			spec := tool.Spec()
			id := registry.qualify(state.spec.Name, spec.ID)
			visible := cloneToolSpec(spec)
			visible.ID = id
			if visible.Name == "" {
				visible.Name = spec.ID
			}
			registry.catalog = append(registry.catalog, catalogTool{id: id, tool: tool, spec: visible, set: state})
		}
	}
	sort.SliceStable(registry.catalog, func(i, j int) bool { return registry.catalog[i].id < registry.catalog[j].id })
	for i := range registry.catalog {
		entry := &registry.catalog[i]
		if _, dup := registry.byCatalog[entry.id]; dup {
			return nil, runtimeError(ErrInvalidArgument, "Tool", "duplicate qualified Tool ID: "+entry.id, nil)
		}
		registry.byCatalog[entry.id] = entry
	}
	if source, ok := cfg.transcript.(LoadedToolSource); ok {
		registry.loaded = registry.known(source.LoadedTools())
	}
	if err := registry.rebuild(); err != nil {
		return nil, err
	}
	return registry, nil
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
func (r *toolRegistry) syncLoaded(ids []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := r.known(ids)
	if slices.Equal(next, r.loaded) {
		return nil
	}
	previous := r.loaded
	r.loaded = next
	if err := r.rebuild(); err != nil {
		r.loaded = previous
		_ = r.rebuild()
		return err
	}
	return nil
}

// toolSetSpec reads a ToolSet's Spec, converting an application panic into a
// RuntimeError so a broken ToolSet fails NewSession instead of crashing the process.
func toolSetSpec(set ToolSet) (spec ToolSetSpec, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = runtimeError(ErrExtensionFailure, "ToolSet", fmt.Sprintf("ToolSet.Spec panicked: %v", recovered), nil)
		}
	}()
	return set.Spec(), nil
}

func resolveToolSet(ctx context.Context, state *toolSetState) ([]Tool, error) {
	var tools []Tool
	err := func() (err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("ToolSet %s panicked: %v", state.spec.Name, recovered)
			}
		}()
		resolved, failure := state.set.Tools(ctx)
		tools = resolved
		return failure
	}()
	if err != nil {
		return nil, runtimeError(ErrToolResolutionFailure, "ToolSet", "cannot resolve ToolSet "+state.spec.Name+": "+err.Error(), err)
	}
	local := map[string]bool{}
	for _, tool := range tools {
		if tool == nil {
			return nil, runtimeError(ErrInvalidArgument, "ToolSet", "ToolSet "+state.spec.Name+" returned a nil Tool", nil)
		}
		spec := tool.Spec()
		if err := validateToolSpec(spec); err != nil {
			return nil, err
		}
		if local[spec.ID] {
			return nil, runtimeError(ErrInvalidArgument, "ToolSet", "duplicate local Tool name in ToolSet "+state.spec.Name+": "+spec.ID, nil)
		}
		local[spec.ID] = true
	}
	return tools, nil
}

// hidden reports whether the Effect cap keeps a Tool from the Model.
func (r *toolRegistry) hidden(spec ToolSpec) bool {
	return r.maxEffect != EffectUnspecified && !spec.Effect.Within(r.maxEffect)
}

// indexable reports whether the Model may find and load a catalog Tool: the
// Effect cap and the filter come first, and a Tool whose ID a static Tool
// already holds cannot be offered twice.
func (r *toolRegistry) indexable(entry *catalogTool) bool {
	if r.hidden(entry.spec) {
		return false
	}
	if r.filter != nil && !r.filter(entry.id) {
		return false
	}
	return !r.staticHolds(entry.id)
}

func (r *toolRegistry) staticHolds(id string) bool {
	for _, tool := range r.staticTools() {
		if r.qualify("", tool.Spec().ID) == id {
			return true
		}
	}
	return false
}

// rebuild recomputes the visible Tool set. Ordering is deterministic: Tools
// sort by qualified ID. A Tool above the Effect cap is left out entirely, so
// it is neither offered nor resolvable by any name.
func (r *toolRegistry) rebuild() error {
	byQualified := map[string]Tool{}
	specs := make([]ToolSpec, 0, len(r.rootTools))
	register := func(qualified string, tool Tool, spec ToolSpec) error {
		if _, exists := byQualified[qualified]; exists {
			return runtimeError(ErrInvalidArgument, "Tool", "duplicate qualified Tool ID: "+qualified, nil)
		}
		byQualified[qualified] = tool
		visible := cloneToolSpec(spec)
		visible.ID = qualified
		if visible.Name == "" {
			visible.Name = spec.ID
		}
		specs = append(specs, visible)
		return nil
	}
	for _, tool := range r.staticTools() {
		spec := tool.Spec()
		if err := validateToolSpec(spec); err != nil {
			return err
		}
		if r.hidden(spec) {
			continue
		}
		if err := register(r.qualify("", spec.ID), tool, spec); err != nil {
			return err
		}
	}
	hasCatalog := false
	for i := range r.catalog {
		entry := &r.catalog[i]
		if entry.set.mode == modeActive {
			if r.hidden(entry.spec) || (r.filter != nil && !r.filter(entry.id)) {
				continue
			}
			if err := register(entry.id, entry.tool, entry.spec); err != nil {
				return err
			}
			continue
		}
		if !r.indexable(entry) {
			continue
		}
		hasCatalog = true
		if slices.Contains(r.loaded, entry.id) {
			if err := register(entry.id, entry.tool, entry.spec); err != nil {
				return err
			}
		}
	}
	if hasCatalog {
		r.search.setDescription(r.catalogText())
		if err := register(r.qualify("", SearchToolName), r.search, r.search.Spec()); err != nil {
			return err
		}
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	r.byQualified = byQualified
	r.specs = specs
	// A local Tool name stays reachable when it is unambiguous, so a Model
	// that answers with the bare name still resolves.
	for _, tool := range r.staticTools() {
		spec := tool.Spec()
		if r.hidden(spec) {
			continue
		}
		for _, alias := range []string{spec.ID, spec.Name} {
			if alias == "" {
				continue
			}
			if _, taken := r.byQualified[alias]; !taken {
				r.byQualified[alias] = tool
			}
		}
	}
	return nil
}

// staticTools is the root surface: Tools installed with WithTool(s) followed
// by the current Tools of every ToolSource.
func (r *toolRegistry) staticTools() []Tool {
	if len(r.sourceTools) == 0 {
		return r.rootTools
	}
	out := make([]Tool, 0, len(r.rootTools)+len(r.sourceTools))
	out = append(out, r.rootTools...)
	out = append(out, r.sourceTools...)
	return out
}

func (r *toolRegistry) hasSources() bool { return len(r.sources) > 0 }

// collectSources asks every ToolSource for its Tools. A nil Tool or a panic
// is a resolution failure, exactly as for a ToolSet.
func (r *toolRegistry) collectSources() (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = runtimeError(ErrToolResolutionFailure, "ToolSource", fmt.Sprintf("ToolSource panicked: %v", recovered), nil)
		}
	}()
	var tools []Tool
	for _, source := range r.sources {
		for _, tool := range source.Tools() {
			if tool == nil {
				return runtimeError(ErrInvalidArgument, "ToolSource", "ToolSource returned a nil Tool", nil)
			}
			tools = append(tools, tool)
		}
	}
	r.sourceTools = tools
	return nil
}

// refreshSources re-collects the dynamic Tools and rebuilds visibility. On
// failure the previous surface stays in place.
func (r *toolRegistry) refreshSources() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	previous := r.sourceTools
	if err := r.collectSources(); err != nil {
		r.sourceTools = previous
		return err
	}
	if err := r.rebuild(); err != nil {
		r.sourceTools = previous
		_ = r.rebuild()
		return err
	}
	return nil
}

// abortPending discards loads staged by a Run that never reached a commit
// boundary (a failed or cancelled Run), so a later Run cannot load a Tool the
// Model never got.
func (r *toolRegistry) abortPending() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending = nil
}

func (r *toolRegistry) qualify(namespace, local string) string {
	if namespace == "" {
		namespace = r.rootNamespace
	}
	if namespace == "" {
		return local
	}
	return namespace + "." + local
}

func (r *toolRegistry) lookup(id string) Tool { return r.byQualified[id] }

func (r *toolRegistry) visibleSpecs() []ToolSpec { return cloneToolSpecs(r.specs) }

// loadStatus says what staging a load did.
type loadStatus int

const (
	// loadStaged: the Tool will be visible after the batch.
	loadStaged loadStatus = iota
	// loadAlready: the Tool is visible, loaded, or staged already.
	loadAlready
	// loadNotFound: no Tool the Session may load carries that ID.
	loadNotFound
)

// stageLoad queues a Tool for loading. Visibility changes only at the batch
// boundary (commitPending). A Tool the Effect cap or the filter refuses, or
// that no ToolSet carries, is not found.
func (r *toolRegistry) stageLoad(id string, why ToolLoadedWhy) loadStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.byCatalog[id]
	if !ok || !r.indexable(entry) {
		return loadNotFound
	}
	if entry.set.mode == modeActive || slices.Contains(r.loaded, id) || slices.ContainsFunc(r.pending, func(p pendingLoad) bool { return p.id == id }) {
		return loadAlready
	}
	r.pending = append(r.pending, pendingLoad{id: id, why: why})
	return loadStaged
}

// stageCall handles a call to a Tool that is not visible. It stages the load
// and reports true when the Tool is a catalog Tool the Session may load.
func (r *toolRegistry) stageCall(id string) bool {
	return r.stageLoad(id, ToolLoadedCall) != loadNotFound
}

// candidates lists, in ID order, the specs of the catalog Tools the Model may
// still load: not loaded, not staged, in the Session's index.
func (r *toolRegistry) candidates() []ToolSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []ToolSpec
	for i := range r.catalog {
		entry := &r.catalog[i]
		if entry.set.mode == modeActive || !r.indexable(entry) || slices.Contains(r.loaded, entry.id) {
			continue
		}
		if slices.ContainsFunc(r.pending, func(p pendingLoad) bool { return p.id == entry.id }) {
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
// whether each is loaded. ok is false when no catalog ToolSet has that name.
func (r *toolRegistry) setTools(name string) (specs []ToolSpec, loaded []bool, ok bool) {
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
		return specs, loaded, true
	}
	return nil, nil, false
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
		if len(ids) == 0 {
			continue
		}
		description := strings.TrimSpace(state.spec.Description)
		b.WriteString("\n" + state.spec.Name)
		if description != "" {
			b.WriteString(" — " + description)
		}
		if state.mode == modeFolded {
			fmt.Fprintf(&b, " (%d tools; pass toolset=%q to list them)\n", len(ids), state.spec.Name)
			continue
		}
		b.WriteString("\n  " + strings.Join(ids, ", ") + "\n")
	}
	return b.String()
}

// commitPending makes staged loads visible. It reports what it loaded, in the
// order staged, so the Loop can emit one Event per load.
func (r *toolRegistry) commitPending() ([]pendingLoad, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pending) == 0 {
		return nil, nil
	}
	staged := r.pending
	r.pending = nil
	previous := r.loaded
	r.loaded = slices.Clone(previous)
	for _, p := range staged {
		r.loaded = append(r.loaded, p.id)
	}
	if err := r.rebuild(); err != nil {
		r.loaded = previous
		_ = r.rebuild()
		return nil, err
	}
	return staged, nil
}
