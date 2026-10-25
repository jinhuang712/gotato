package gotato

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
)

// ActivationToolName is the local name of the built-in Tool that activates an
// inactive ToolSet. It is visible only while an inactive ToolSet remains.
const ActivationToolName = "activate_toolset"

// ToolSetSpec identifies a ToolSet. The name must be stable and unique.
type ToolSetSpec struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// ToolSet stages capability discovery: its Tools stay hidden from the Model
// until the ToolSet is activated. Resolution must be deterministic, and a
// failure must not partially register the returned collection.
type ToolSet interface {
	Spec() ToolSetSpec
	Tools(context.Context) ([]Tool, error)
}

// WithToolSet installs a ToolSet that starts inactive. The Model reaches its
// Tools by calling the activation Tool first.
func WithToolSet(set ToolSet) Option {
	return withToolSet(set, false)
}

// WithActiveToolSet installs a ToolSet whose Tools are visible immediately.
func WithActiveToolSet(set ToolSet) Option {
	return withToolSet(set, true)
}

func withToolSet(set ToolSet, active bool) Option {
	return func(c *sessionConfig) error {
		if set == nil {
			return runtimeError(ErrInvalidArgument, "WithToolSet", "ToolSet is nil", nil)
		}
		c.toolSets = append(c.toolSets, toolSetConfig{set: set, active: active})
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

// ActiveToolSetSource is an optional method set of a Transcript: the names of
// the ToolSets the model switched on in earlier Runs of the Session, in the
// order it switched them on. NewSession switches those ToolSets on again, so
// a ToolSet the model activated stays active for the Session; a session.Record
// implements it. A name that no installed ToolSet carries, or whose ToolSet no
// longer resolves, or that would pass MaxActiveToolSets, is left off.
type ActiveToolSetSource interface {
	ActiveToolSets() []string
}

type toolSetConfig struct {
	set    ToolSet
	active bool
}

// toolSetState tracks one installed ToolSet. resolved holds the Tools of an
// active ToolSet; it is filled in one atomic step so a failed resolution
// exposes nothing.
type toolSetState struct {
	set    ToolSet
	spec   ToolSetSpec
	active bool
	// configured marks a ToolSet installed with WithActiveToolSet: the
	// Agent's choice, never switched off by the Session's own state.
	configured bool
	resolved   []Tool
}

// toolRegistry owns Tool identity, visibility, and activation for one Session.
// The Session goroutine commits visibility changes at batch boundaries; the
// activation Tool resolves and stages a ToolSet from its own goroutine, so mu
// guards every mutation of that state.
type toolRegistry struct {
	rootNamespace string
	rootTools     []Tool
	sources       []ToolSource
	sourceTools   []Tool
	sets          []*toolSetState
	byQualified   map[string]Tool
	specs         []ToolSpec
	activation    Tool
	maxActive     uint32
	explicit      bool
	// maxEffect hides every Tool above it; EffectUnspecified means no cap.
	maxEffect ToolEffect
	// pending holds ToolSets activated during the current batch. Visibility
	// commits at the batch boundary, never inside it.
	pending []*toolSetState
	// mu guards sets, pending, resolved, specs, and byQualified. Callers that
	// run on the Session goroutine hold the Session's registryMu, which is the
	// lock ToolInspector.Tools reads under; mu is the inner lock.
	mu sync.Mutex
}

func newToolRegistry(ctx context.Context, cfg *sessionConfig) (*toolRegistry, error) {
	registry := &toolRegistry{
		rootNamespace: cfg.rootNamespace,
		rootTools:     cfg.tools,
		sources:       cfg.toolSources,
		maxActive:     cfg.limits.MaxActiveToolSets,
		explicit:      cfg.limitsSet,
		maxEffect:     cfg.maxEffect,
	}
	if err := registry.collectSources(); err != nil {
		return nil, err
	}
	names := map[string]bool{}
	activeCount := uint32(0)
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
		if entry.active {
			activeCount++
			// Enforce the same ceiling here that stage enforces at activation,
			// so WithActiveToolSet cannot install more active ToolSets than the
			// limit admits.
			if limitExceededUint32(registry.explicit, registry.maxActive, activeCount) {
				return nil, runtimeError(ErrLimitExceeded, "ToolSet", "maximum active ToolSets exceeded", nil)
			}
		}
		registry.sets = append(registry.sets, &toolSetState{set: entry.set, spec: spec, active: entry.active, configured: entry.active})
	}
	// Resolving the active ToolSets during construction keeps the first
	// Model request deterministic and surfaces a broken ToolSet before the
	// Session accepts any command.
	for _, state := range registry.sets {
		if !state.active {
			continue
		}
		tools, err := resolveToolSet(ctx, state)
		if err != nil {
			return nil, err
		}
		state.resolved = tools
	}
	if source, ok := cfg.transcript.(ActiveToolSetSource); ok {
		if err := registry.restore(ctx, source.ActiveToolSets()); err != nil {
			return nil, err
		}
	}
	if err := registry.rebuild(); err != nil {
		return nil, err
	}
	return registry, nil
}

// restore switches on again the ToolSets a Session's earlier Runs activated.
// It never fails the Session over a restored name: a name nothing installed
// carries, a ToolSet that no longer resolves, a ToolSet whose Tools collide with
// Tools already visible, and a ToolSet past MaxActiveToolSets all stay off,
// each on its own, so the model can activate them again. A context that ends
// during the restore fails it instead: the caller no longer wants the Session.
func (r *toolRegistry) restore(ctx context.Context, names []string) error {
	for _, name := range names {
		for _, state := range r.sets {
			if state.spec.Name != name || state.active {
				continue
			}
			if limitExceededUint32(r.explicit, r.maxActive, r.activeCount()+1) {
				return nil
			}
			tools, err := resolveToolSet(ctx, state)
			if ctxErr := ctx.Err(); ctxErr != nil {
				// A cancelled caller gets no half-restored Session.
				return runtimeError(codeForContext(ctxErr), "NewSession", "context ended while restoring ToolSets", ctxErr)
			}
			if err != nil {
				break
			}
			state.resolved = tools
			state.active = true
			if err := r.rebuild(); err != nil {
				// Only this ToolSet stays off; the rest of the Session starts.
				state.active = false
				state.resolved = nil
				_ = r.rebuild()
			}
			break
		}
	}
	return nil
}

// narrow switches off every ToolSet the Session switched on that names does
// not list. A ToolSet installed active stays on.
func (r *toolRegistry) narrow(names []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	changed := false
	for _, state := range r.sets {
		if state.active && !state.configured && !slices.Contains(names, state.spec.Name) {
			state.active = false
			state.resolved = nil
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return r.rebuild()
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

// rebuild recomputes the visible Tool set. Ordering is deterministic: Tools
// sort by qualified ID. A Tool above the Effect cap is left out entirely, so
// it is neither offered nor resolvable by any name.
func (r *toolRegistry) rebuild() error {
	byQualified := map[string]Tool{}
	specs := make([]ToolSpec, 0, len(r.rootTools))
	register := func(qualified string, tool Tool, spec ToolSpec) error {
		if r.hidden(spec) {
			return nil
		}
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
		if err := register(r.qualify("", spec.ID), tool, spec); err != nil {
			return err
		}
	}
	for _, state := range r.sets {
		if !state.active {
			continue
		}
		for _, tool := range state.resolved {
			spec := tool.Spec()
			if err := register(r.qualify(state.spec.Name, spec.ID), tool, spec); err != nil {
				return err
			}
		}
	}
	if inactive := r.inactiveNames(); len(inactive) > 0 {
		activation := &activationTool{registry: r, names: inactive}
		r.activation = activation
		if err := register(r.qualify("", ActivationToolName), activation, activation.Spec()); err != nil {
			return err
		}
	} else {
		r.activation = nil
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

// abortPending discards ToolSets staged by a Run that never reached a commit
// boundary (a failed or cancelled Run), so a later Run cannot activate a
// ToolSet the Model never asked for.
func (r *toolRegistry) abortPending() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, state := range r.pending {
		state.resolved = nil
	}
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

func (r *toolRegistry) inactiveNames() []string {
	names := make([]string, 0, len(r.sets))
	for _, state := range r.sets {
		if !state.active {
			names = append(names, state.spec.Name)
		}
	}
	sort.Strings(names)
	return names
}

func (r *toolRegistry) activeCount() uint32 {
	var count uint32
	for _, state := range r.sets {
		if state.active {
			count++
		}
	}
	return count
}

func (r *toolRegistry) lookup(id string) Tool { return r.byQualified[id] }

func (r *toolRegistry) visibleSpecs() []ToolSpec { return cloneToolSpecs(r.specs) }

// stage resolves a ToolSet and queues it for activation. Resolution runs here
// so a failure reaches the Model as a failed Tool Result; visibility changes
// only at the batch boundary.
func (r *toolRegistry) stage(ctx context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, state := range r.sets {
		if state.spec.Name != name {
			continue
		}
		if state.active {
			return nil
		}
		if slices.Contains(r.pending, state) {
			return nil
		}
		if limitExceededUint32(r.explicit, r.maxActive, r.activeCount()+uint32(len(r.pending))+1) {
			return runtimeError(ErrLimitExceeded, "ToolSet", "maximum active ToolSets exceeded", nil)
		}
		tools, err := resolveToolSet(ctx, state)
		if err != nil {
			return err
		}
		state.resolved = tools
		r.pending = append(r.pending, state)
		return nil
	}
	return runtimeError(ErrToolResolutionFailure, "ToolSet", "unknown ToolSet: "+name, nil)
}

// commitPending makes staged ToolSets visible. It reports the names it
// activated so the Loop can emit one Event per activation.
func (r *toolRegistry) commitPending() ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pending) == 0 {
		return nil, nil
	}
	staged := r.pending
	r.pending = nil
	for _, state := range staged {
		state.active = true
	}
	if err := r.rebuild(); err != nil {
		for _, state := range staged {
			state.active = false
			state.resolved = nil
		}
		_ = r.rebuild()
		return nil, err
	}
	names := make([]string, 0, len(staged))
	for _, state := range staged {
		names = append(names, state.spec.Name)
	}
	sort.Strings(names)
	return names, nil
}

// activationTool is the ordinary Tool the Model calls to activate a ToolSet.
type activationTool struct {
	registry *toolRegistry
	names    []string
}

func (t *activationTool) Spec() ToolSpec {
	enum := make([]any, 0, len(t.names))
	for _, name := range t.names {
		enum = append(enum, name)
	}
	schema, _ := json.Marshal(map[string]any{
		"type":                 "object",
		"required":             []any{"name"},
		"additionalProperties": false,
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "enum": enum},
		},
	})
	return ToolSpec{
		ID:          ActivationToolName,
		Name:        ActivationToolName,
		Description: "Activate one inactive ToolSet. Its Tools become visible on the next request.",
		Sequential:  true,
		InputSchema: schema,
		// It changes only which Tools the Model sees, and those Tools pass
		// the Effect cap like any other, so it stays visible under every cap.
		Effect: EffectRead,
	}
}

func (t *activationTool) Execute(ctx context.Context, use ToolUse, progress ToolProgressFunc) (ToolResult, error) {
	var input struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(use.ArgumentsJSON, &input); err != nil {
		return ToolResult{}, runtimeError(ErrToolArgumentFailure, ActivationToolName, "cannot decode activation arguments", err)
	}
	if err := t.registry.stage(ctx, input.Name); err != nil {
		return ToolResult{}, err
	}
	return ToolResult{
		Status:  ToolResultOK,
		Content: []ContentPart{{Kind: ContentText, Text: "ToolSet " + input.Name + " is active on the next request."}},
	}, nil
}
