package gotato

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
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
	return func(c *agentConfig) error {
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
	return func(c *agentConfig) error {
		namespace = strings.TrimSpace(namespace)
		if strings.Contains(namespace, ".") {
			return runtimeError(ErrInvalidArgument, "WithRootNamespace", "namespace cannot contain a dot", nil)
		}
		c.rootNamespace = namespace
		return nil
	}
}

type toolSetConfig struct {
	set    ToolSet
	active bool
}

// toolSetState tracks one installed ToolSet. resolved holds the Tools of an
// active ToolSet; it is filled in one atomic step so a failed resolution
// exposes nothing.
type toolSetState struct {
	set      ToolSet
	spec     ToolSetSpec
	active   bool
	resolved []Tool
}

// toolRegistry owns Tool identity, visibility, and activation for one Agent.
// The Agent goroutine is its only mutation authority.
type toolRegistry struct {
	rootNamespace string
	rootTools     []Tool
	sets          []*toolSetState
	byQualified   map[string]Tool
	specs         []ToolSpec
	activation    Tool
	maxActive     uint32
	explicit      bool
	// pending holds ToolSets activated during the current batch. Visibility
	// commits at the batch boundary, never inside it.
	pending []*toolSetState
}

func newToolRegistry(cfg *agentConfig) (*toolRegistry, error) {
	registry := &toolRegistry{
		rootNamespace: cfg.rootNamespace,
		rootTools:     cfg.tools,
		maxActive:     cfg.limits.MaxActiveToolSets,
		explicit:      cfg.limitsSet,
	}
	names := map[string]bool{}
	for _, entry := range cfg.toolSets {
		spec := entry.set.Spec()
		if strings.TrimSpace(spec.Name) == "" {
			return nil, runtimeError(ErrInvalidArgument, "ToolSet", "ToolSet name is empty", nil)
		}
		if strings.Contains(spec.Name, ".") {
			return nil, runtimeError(ErrInvalidArgument, "ToolSet", "ToolSet name cannot contain a dot: "+spec.Name, nil)
		}
		if names[spec.Name] {
			return nil, runtimeError(ErrInvalidArgument, "ToolSet", "duplicate ToolSet name: "+spec.Name, nil)
		}
		names[spec.Name] = true
		registry.sets = append(registry.sets, &toolSetState{set: entry.set, spec: spec, active: entry.active})
	}
	// Resolving the active ToolSets during construction keeps the first
	// Model request deterministic and surfaces a broken ToolSet before the
	// Agent accepts any command.
	for _, state := range registry.sets {
		if !state.active {
			continue
		}
		tools, err := resolveToolSet(context.Background(), state)
		if err != nil {
			return nil, err
		}
		state.resolved = tools
	}
	if err := registry.rebuild(); err != nil {
		return nil, err
	}
	return registry, nil
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

// rebuild recomputes the visible Tool set. Ordering is deterministic: Tools
// sort by qualified ID.
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
	for _, tool := range r.rootTools {
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
	for _, tool := range r.rootTools {
		spec := tool.Spec()
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
	for _, state := range r.sets {
		if state.spec.Name != name {
			continue
		}
		if state.active {
			return nil
		}
		for _, queued := range r.pending {
			if queued == state {
				return nil
			}
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
	}
}

func (t *activationTool) Execute(ctx context.Context, use ToolUse, progress ToolProgress) (ToolResult, error) {
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
