package gotato

import (
	"context"
	"slices"
	"sort"
	"strings"
	"sync"
)

// ToolSource supplies Tools whose set may change between Turns. The Session
// asks every installed ToolSource for its Tools at the start of each Turn and
// keeps that set for the whole Turn, so a Tool the Model was shown can always
// be resolved when the Model calls it.
//
// Tools returned by a ToolSource are qualified under the root namespace like
// Tools installed with WithTools, and cannot take a name WithTools refuses.
//
// Tools takes the ctx of the Run (or of NewSession, or of the inspection) and
// must honor it. When it fails, panics or returns a bad Tool (see Tool), the
// Session keeps the last set it had from every ToolSource, as one source, and
// stores an ExtensionFailed Event with Stage "toolsource", as it does for a
// ToolSet that fails; the Run goes on. NewSession, which has no earlier set,
// fails instead. A ctx that ended is the caller's, and ends the Run.
type ToolSource interface {
	Tools(ctx context.Context) ([]Tool, error)
}

// WithToolSource installs a dynamic Tool source.
func WithToolSource(source ToolSource) SessionOption {
	return func(c *sessionConfig) error {
		if source == nil {
			return runtimeError(CodeInvalidArgument, "WithToolSource", "source is nil", nil)
		}
		c.toolSources = append(c.toolSources, source)
		return nil
	}
}

// Tools reports the Tools the Session exposes to its Model, as a snapshot
// sorted by ID. While the Session is idle it is the set its next Turn offers;
// during a Run it reflects the set of the most recent Turn and the Tools loaded
// since, each from the moment the Tool Result that loads it commits.
func (a *Session) Tools() []ToolSpec {
	a.tools.mu.Lock()
	defer a.tools.mu.Unlock()
	specs := a.tools.registry.visibleSpecs()
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	return specs
}

// ToolSetStatus is what a Session knows of one installed ToolSet.
type ToolSetStatus struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Unavailable is true when the ToolSet failed its last resolution; it then
	// holds no Tools.
	Unavailable bool `json:"unavailable,omitempty"`
	// Tools are the ToolSet's Tools in ID order, under their qualified IDs.
	Tools []ToolSetTool `json:"tools,omitempty"`
}

// ToolSetTool is one Tool of an installed ToolSet.
type ToolSetTool struct {
	Spec ToolSpec `json:"spec"`
	// Loaded is true when the Tool is loaded (a fact of the Transcript) or its
	// ToolSet is installed active. A Tool the filter refuses is still Loaded;
	// it is just not offered.
	Loaded bool `json:"loaded"`
}

// ToolSets reports every installed ToolSet as the last resolution left it, in
// install order. A Tool above the Effect cap is left out, as it is never
// offered. A Tool the WithToolFilter keep refuses is still listed, so a
// caller can name it to switch it back on.
func (a *Session) ToolSets() []ToolSetStatus {
	a.tools.mu.Lock()
	defer a.tools.mu.Unlock()
	return a.tools.registry.toolSetStatus()
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
