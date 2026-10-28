package gotato

import (
	"context"
	"sort"
)

// ToolSource supplies Tools whose set may change between Turns. The Session
// asks every installed ToolSource for its Tools at the start of each Turn and
// keeps that set for the whole Turn, so a Tool the Model was shown can always
// be resolved when the Model calls it.
//
// Tools returned by a ToolSource are qualified under the root namespace like
// Tools installed with WithTools.
//
// Tools takes the ctx of the Run (or of NewSession, or of the inspection) and
// must honor it. When it fails or panics, the Session keeps the last set it
// had and stores an ExtensionFailed Event with Stage "toolsource", as it does
// for a ToolSet that fails; the Run goes on. NewSession, which has no earlier
// set, fails instead. A ctx that ended is the caller's, and ends the Run.
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
// during a Run it reflects the set of the most recent Turn.
func (a *Session) Tools() []ToolSpec {
	a.registryMu.Lock()
	defer a.registryMu.Unlock()
	specs := a.registry.visibleSpecs()
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
	a.registryMu.Lock()
	defer a.registryMu.Unlock()
	return a.registry.toolSetStatus()
}
