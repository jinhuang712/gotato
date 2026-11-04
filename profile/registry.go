package profile

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/store"
)

// Registry holds the definitions a host runs, by name, and answers one lookup
// for each. A name is registered once and a later registration replaces an
// earlier one, as a service that edits a definition expects, and a lookup sees
// what is registered when it is made. There are four registrations, two for each
// kind of definition:
//
//   - inline: the definition is a Go value (RegisterAgentProfileInline,
//     RegisterTaskSpecInline);
//   - from a store: the name is registered and its document is read from a
//     store.Definitions at every lookup (RegisterAgentProfileWithStore,
//     RegisterTaskSpecWithStore). The store is the service's own: its query, its
//     cache and its connection are not Gotato's, and a row that is not
//     registered by name is not a definition.
//
// There is no fallback store, no chain of them and no lookup that tries code
// and then a store: each name says where it comes from. A Registry is safe for
// concurrent use.
type Registry struct {
	catalog *Catalog

	mu     sync.RWMutex
	agents map[string]source[AgentProfile]
	tasks  map[string]source[TaskSpec]
}

// source is where a registered name comes from: a value, or a store.
type source[T any] struct {
	value *T
	defs  store.Definitions
}

// NewRegistry returns an empty Registry whose stored definitions resolve their
// names through catalog. A nil catalog holds nothing, so a stored AgentProfile
// that names a model fails to resolve.
func NewRegistry(catalog *Catalog) *Registry {
	return &Registry{catalog: catalog, agents: map[string]source[AgentProfile]{}, tasks: map[string]source[TaskSpec]{}}
}

// RegisterAgentProfileInline registers p under its Name. It refuses a profile
// that cannot work (no Model, an unknown MaxEffect, a nil ToolSet or Extension).
func (r *Registry) RegisterAgentProfileInline(p AgentProfile) error {
	p.Name = strings.TrimSpace(p.Name)
	if err := p.validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[p.Name] = source[AgentProfile]{value: &p}
	return nil
}

// RegisterTaskSpecInline registers t under its Name. It refuses a TaskSpec that
// cannot work on its own; that it only narrows its AgentProfile is checked when
// it is looked up with the AgentProfile (Narrowed), since the profile may be
// registered later or replaced.
func (r *Registry) RegisterTaskSpecInline(t TaskSpec) error {
	t.Name = strings.TrimSpace(t.Name)
	if err := t.validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks[t.Name] = source[TaskSpec]{value: &t}
	return nil
}

// RegisterAgentProfileWithStore registers name as an AgentProfile read from
// defs, under kind KindAgentProfile, at every lookup. The document is not read
// here: a row may be written after the name is registered.
func (r *Registry) RegisterAgentProfileWithStore(name string, defs store.Definitions) error {
	name, err := storeName(name, defs)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[name] = source[AgentProfile]{defs: defs}
	return nil
}

// RegisterTaskSpecWithStore registers name as a TaskSpec read from defs, under
// kind KindTaskSpec, at every lookup.
func (r *Registry) RegisterTaskSpecWithStore(name string, defs store.Definitions) error {
	name, err := storeName(name, defs)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks[name] = source[TaskSpec]{defs: defs}
	return nil
}

func storeName(name string, defs store.Definitions) (string, error) {
	name = strings.TrimSpace(name)
	if err := store.CheckDefinitionKey(KindAgentProfile, name); err != nil {
		return "", invalid(fmt.Sprintf("name %q: %v", name, err))
	}
	if defs == nil {
		return "", invalid(fmt.Sprintf("%q is registered with a nil store", name))
	}
	return name, nil
}

// AgentProfile returns the AgentProfile registered as name: the value, or the
// document read from its store, resolved through the Catalog. A name that is
// registered nowhere, and a stored one whose row is gone, fail with
// ErrNotRegistered; a name the Catalog lacks fails with an *UnknownNameError.
func (r *Registry) AgentProfile(ctx context.Context, name string) (AgentProfile, error) {
	r.mu.RLock()
	src, ok := r.agents[name]
	r.mu.RUnlock()
	switch {
	case !ok:
		return AgentProfile{}, notRegistered("AgentProfile", name)
	case src.value != nil:
		return *src.value, nil
	}
	doc, err := src.defs.LoadDefinition(ctx, KindAgentProfile, name)
	if err != nil {
		return AgentProfile{}, loadFailed("AgentProfile", name, err)
	}
	return DecodeAgentProfile(name, doc, r.catalog)
}

// TaskSpec returns the TaskSpec registered as name, as AgentProfile does. It
// looks up no AgentProfile: Narrowed does.
func (r *Registry) TaskSpec(ctx context.Context, name string) (TaskSpec, error) {
	r.mu.RLock()
	src, ok := r.tasks[name]
	r.mu.RUnlock()
	switch {
	case !ok:
		return TaskSpec{}, notRegistered("TaskSpec", name)
	case src.value != nil:
		return *src.value, nil
	}
	doc, err := src.defs.LoadDefinition(ctx, KindTaskSpec, name)
	if err != nil {
		return TaskSpec{}, loadFailed("TaskSpec", name, err)
	}
	return DecodeTaskSpec(name, doc)
}

// Narrowed returns the AgentProfile t names, as t runs it: the check that t
// only narrows it, and that every name t keeps is known, is made here, once the
// profile is resolved whether it came inline or from a store, and then
// TaskSpec.Apply. It is the one way a TaskSpec reaches a Session, for a
// registered TaskSpec and for one a request carries alike.
func (r *Registry) Narrowed(ctx context.Context, t TaskSpec) (AgentProfile, error) {
	if err := t.validate(); err != nil {
		return AgentProfile{}, err
	}
	p, err := r.AgentProfile(ctx, t.Profile)
	if err != nil {
		return AgentProfile{}, fmt.Errorf("TaskSpec %q: %w", t.Name, err)
	}
	if err := t.Narrow(p, r.catalog); err != nil {
		return AgentProfile{}, err
	}
	return t.Apply(p), nil
}

// AgentProfiles lists the registered AgentProfile names, sorted.
func (r *Registry) AgentProfiles() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return sortedNames(r.agents)
}

// TaskSpecs lists the registered TaskSpec names, sorted.
func (r *Registry) TaskSpecs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return sortedNames(r.tasks)
}

// loadFailed says a registered name has no document, or the store failed.
func loadFailed(kind, name string, err error) error {
	if errors.Is(err, record.ErrNotFound) {
		return fmt.Errorf("%w: %s %q has no document in its store", ErrNotRegistered, kind, name)
	}
	return fmt.Errorf("profile: load %s %q: %w", kind, name, err)
}

// sortedNames lists the keys of m, sorted; an empty m gives an empty list, which
// a JSON answer writes as [] and not null.
func sortedNames[V any](m map[string]V) []string {
	names := slices.AppendSeq(make([]string, 0, len(m)), maps.Keys(m))
	slices.Sort(names)
	return names
}
