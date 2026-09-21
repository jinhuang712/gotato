// Package toolregistry is the Gotato Tool Registry: it owns Tool identity,
// visibility, and activation for a static or dynamic Tool surface.
//
// A Registry implements gotato.ToolSource, so an Agent picks up changes at
// each Turn boundary:
//
//	reg, _ := toolregistry.New()
//	reg.Register(fsRead)
//	reg.Register(shell)
//	reg.Deactivate("shell")           // registered but hidden from the Model
//	agent, _ := gotato.NewAgent(gotato.WithModel(m), gotato.WithToolSource(reg))
//
// The Registry normalizes a Tool's ID by trimming surrounding whitespace and
// captures its Spec at Register. That captured Spec is the single source of
// truth: Describe, List, Active, Tools, and Lookup all report the same
// canonical ID and Spec, so the views can never disagree. A caller may address
// a Tool by either the raw or the trimmed ID.
//
// Discovery systems, MCP catalogs, and authorization policies are built above
// or beside the Registry; it does not schedule or orchestrate anything.
package toolregistry

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"

	gotato "github.com/jinhuang712/gotato"
)

// ErrNotFound is returned for an unknown Tool ID. It wraps the ID.
var ErrNotFound = errors.New("toolregistry: tool not found")

// ErrDuplicate is returned when registering an ID that already exists.
var ErrDuplicate = errors.New("toolregistry: duplicate tool id")

// ChangeKind names a Registry mutation.
type ChangeKind string

const (
	Registered   ChangeKind = "registered"
	Unregistered ChangeKind = "unregistered"
	Activated    ChangeKind = "activated"
	Deactivated  ChangeKind = "deactivated"
)

// Change is delivered to OnChange hooks.
type Change struct {
	Kind ChangeKind `json:"kind"`
	ID   string     `json:"id"`
}

// Entry is the described view of one registered Tool.
type Entry struct {
	Spec   gotato.ToolSpec `json:"spec"`
	Active bool            `json:"active"`
}

type entry struct {
	tool   gotato.Tool
	active bool
}

// canonicalTool is the Tool the Registry stores and hands out. Spec reports the
// normalized, cloned snapshot captured at Register, so every view agrees;
// Execute delegates to the registered Tool.
type canonicalTool struct {
	tool gotato.Tool
	spec gotato.ToolSpec
}

func (t canonicalTool) Spec() gotato.ToolSpec { return cloneSpec(t.spec) }

func (t canonicalTool) Execute(ctx context.Context, use gotato.ToolUse, progress gotato.ToolProgress) (gotato.ToolResult, error) {
	return t.tool.Execute(ctx, use, progress)
}

// Registry is safe for concurrent use.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]*entry
	hooks   []func(Change)
}

// New creates a Registry and registers the given Tools in the active state.
// It returns the first registration failure rather than silently dropping a
// Tool. A caller with already-validated Tools may use MustNew.
func New(tools ...gotato.Tool) (*Registry, error) {
	r := &Registry{}
	for _, tool := range tools {
		if err := r.Register(tool); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// MustNew is New for Tools known to be valid; it panics on a registration
// failure.
func MustNew(tools ...gotato.Tool) *Registry {
	r, err := New(tools...)
	if err != nil {
		panic(err)
	}
	return r
}

// Register adds a Tool in the active state. The ID is ToolSpec.ID.
func (r *Registry) Register(tool gotato.Tool) error {
	if tool == nil {
		return errors.New("toolregistry: tool is nil")
	}
	spec := tool.Spec()
	id := normalizeID(spec.ID)
	if id == "" {
		return errors.New("toolregistry: tool has an empty ID")
	}
	spec.ID = id
	r.mu.Lock()
	if r.entries == nil {
		r.entries = map[string]*entry{}
	}
	if _, exists := r.entries[id]; exists {
		r.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrDuplicate, id)
	}
	// Store a canonical wrapper with a private copy of the Spec, so the
	// Registry has one source of truth and a caller cannot mutate it through a
	// slice or map it still holds. Execute still reaches the original Tool.
	r.entries[id] = &entry{tool: canonicalTool{tool: tool, spec: cloneSpec(spec)}, active: true}
	hooks := r.hooks
	r.mu.Unlock()
	notify(hooks, Change{Kind: Registered, ID: id})
	return nil
}

// Unregister removes a Tool.
func (r *Registry) Unregister(id string) error {
	id = normalizeID(id)
	r.mu.Lock()
	if _, exists := r.entries[id]; !exists {
		r.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	delete(r.entries, id)
	hooks := r.hooks
	r.mu.Unlock()
	notify(hooks, Change{Kind: Unregistered, ID: id})
	return nil
}

// Activate makes a registered Tool visible to the Model.
func (r *Registry) Activate(id string) error { return r.setActive(id, true) }

// Deactivate hides a registered Tool from the Model without removing it.
func (r *Registry) Deactivate(id string) error { return r.setActive(id, false) }

func (r *Registry) setActive(id string, active bool) error {
	id = normalizeID(id)
	r.mu.Lock()
	e, exists := r.entries[id]
	if !exists {
		r.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	changed := e.active != active
	e.active = active
	hooks := r.hooks
	r.mu.Unlock()
	if changed {
		kind := Activated
		if !active {
			kind = Deactivated
		}
		notify(hooks, Change{Kind: kind, ID: id})
	}
	return nil
}

// Lookup returns a registered Tool (active or not).
func (r *Registry) Lookup(id string) (gotato.Tool, bool) {
	id = normalizeID(id)
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[id]
	if !ok {
		return nil, false
	}
	return e.tool, true
}

// Describe returns the Entry for one Tool.
func (r *Registry) Describe(id string) (Entry, bool) {
	id = normalizeID(id)
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[id]
	if !ok {
		return Entry{}, false
	}
	return Entry{Spec: e.tool.Spec(), Active: e.active}, true
}

// List returns every Entry sorted by ID.
func (r *Registry) List() []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Entry, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, Entry{Spec: e.tool.Spec(), Active: e.active})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Spec.ID < out[j].Spec.ID })
	return out
}

// Active returns the specs of active Tools sorted by ID.
func (r *Registry) Active() []gotato.ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]gotato.ToolSpec, 0, len(r.entries))
	for _, e := range r.entries {
		if e.active {
			out = append(out, e.tool.Spec())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Tools implements gotato.ToolSource: the active Tools sorted by ID.
func (r *Registry) Tools() []gotato.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.entries))
	for id, e := range r.entries {
		if e.active {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]gotato.Tool, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.entries[id].tool)
	}
	return out
}

// OnChange registers a hook called synchronously after every mutation. Hooks
// must be fast and must not call back into the Registry.
func (r *Registry) OnChange(hook func(Change)) {
	if hook == nil {
		return
	}
	r.mu.Lock()
	r.hooks = append(r.hooks, hook)
	r.mu.Unlock()
}

func notify(hooks []func(Change), change Change) {
	for _, hook := range hooks {
		hook(change)
	}
}

// normalizeID is the one place a Tool ID is canonicalized: Register trims
// before storing, and every lookup entry point trims the same way, so a caller
// can round-trip the raw ToolSpec.ID and still address the Tool.
func normalizeID(id string) string { return strings.TrimSpace(id) }

// cloneSpec deep-copies the slice and map fields so a spec handed out by the
// Registry cannot be mutated into the Tool's own state.
func cloneSpec(spec gotato.ToolSpec) gotato.ToolSpec {
	out := spec
	out.InputSchema = slices.Clone(spec.InputSchema)
	out.OutputSchema = slices.Clone(spec.OutputSchema)
	out.Metadata = maps.Clone(spec.Metadata)
	return out
}
