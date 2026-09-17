// Package toolregistry is the Gotato Tool Registry: it owns Tool identity,
// visibility, and activation for a static or dynamic Tool surface.
//
// A Registry implements gotato.ToolSource, so an Agent picks up changes at
// each Turn boundary:
//
//	reg := toolregistry.New()
//	reg.Register(fsRead)
//	reg.Register(shell)
//	reg.Deactivate("shell")           // registered but hidden from the Model
//	agent, _ := gotato.NewAgent(gotato.WithModel(m), gotato.WithToolSource(reg))
//
// Discovery systems, MCP catalogs, and authorization policies are built above
// or beside the Registry; it does not schedule or orchestrate anything.
package toolregistry

import (
	"errors"
	"sort"
	"strings"
	"sync"

	gotato "github.com/jinhuang712/gotato"
)

// ErrNotFound is returned for an unknown Tool ID.
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
	spec   gotato.ToolSpec
	active bool
}

// Registry is safe for concurrent use.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]*entry
	hooks   []func(Change)
}

// New creates an empty Registry. Tools passed here are registered active.
func New(tools ...gotato.Tool) *Registry {
	r := &Registry{entries: map[string]*entry{}}
	for _, tool := range tools {
		_ = r.Register(tool)
	}
	return r
}

// Register adds a Tool in the active state. The ID is ToolSpec.ID.
func (r *Registry) Register(tool gotato.Tool) error {
	if tool == nil {
		return errors.New("toolregistry: tool is nil")
	}
	spec := tool.Spec()
	id := strings.TrimSpace(spec.ID)
	if id == "" {
		return errors.New("toolregistry: tool has an empty ID")
	}
	r.mu.Lock()
	if _, exists := r.entries[id]; exists {
		r.mu.Unlock()
		return ErrDuplicate
	}
	r.entries[id] = &entry{tool: tool, spec: spec, active: true}
	hooks := r.hooks
	r.mu.Unlock()
	notify(hooks, Change{Kind: Registered, ID: id})
	return nil
}

// Unregister removes a Tool.
func (r *Registry) Unregister(id string) error {
	r.mu.Lock()
	if _, exists := r.entries[id]; !exists {
		r.mu.Unlock()
		return ErrNotFound
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
	r.mu.Lock()
	e, exists := r.entries[id]
	if !exists {
		r.mu.Unlock()
		return ErrNotFound
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
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[id]
	if !ok {
		return Entry{}, false
	}
	return Entry{Spec: e.spec, Active: e.active}, true
}

// List returns every Entry sorted by ID.
func (r *Registry) List() []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Entry, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, Entry{Spec: e.spec, Active: e.active})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Spec.ID < out[j].Spec.ID })
	return out
}

// Active returns the specs of active Tools sorted by ID.
func (r *Registry) Active() []gotato.ToolSpec {
	tools := r.Tools()
	out := make([]gotato.ToolSpec, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Spec())
	}
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
