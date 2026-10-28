package gotato

import (
	"context"
	"fmt"
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
func WithToolSet(set ToolSet) SessionOption {
	return withToolSet(set, modeListed)
}

// WithFoldedToolSet installs a ToolSet whose catalog entry shows only its
// description and a count of its Tools. The Model lists them with the toolset
// parameter of search_tools, then loads by name.
func WithFoldedToolSet(set ToolSet) SessionOption {
	return withToolSet(set, modeFolded)
}

// WithActiveToolSet installs a ToolSet whose Tools are visible always, as if
// they were base Tools. They are never in the catalog and never unloaded.
func WithActiveToolSet(set ToolSet) SessionOption {
	return withToolSet(set, modeActive)
}

func withToolSet(set ToolSet, mode installMode) SessionOption {
	return func(c *sessionConfig) error {
		if set == nil {
			return runtimeError(CodeInvalidArgument, "WithToolSet", "ToolSet is nil", nil)
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
func WithToolFilter(keep func(toolID string) bool) SessionOption {
	return func(c *sessionConfig) error {
		c.toolFilter = keep
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

// toolSetSpec reads a ToolSet's Spec, converting an application panic into a
// RuntimeError so a broken ToolSet fails NewSession instead of crashing the process.
func toolSetSpec(set ToolSet) (spec ToolSetSpec, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = runtimeError(CodeExtensionFailure, "ToolSet", fmt.Sprintf("ToolSet.Spec panicked: %v", recovered), nil)
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
		return nil, runtimeError(CodeToolResolutionFailure, "ToolSet", "cannot resolve ToolSet "+state.spec.Name+": "+err.Error(), err)
	}
	local := map[string]bool{}
	for _, tool := range tools {
		if tool == nil {
			return nil, runtimeError(CodeInvalidArgument, "ToolSet", "ToolSet "+state.spec.Name+" returned a nil Tool", nil)
		}
		spec := tool.Spec()
		if err := validateToolSpec(spec); err != nil {
			return nil, err
		}
		if local[spec.ID] {
			return nil, runtimeError(CodeInvalidArgument, "ToolSet", "duplicate local Tool name in ToolSet "+state.spec.Name+": "+spec.ID, nil)
		}
		local[spec.ID] = true
	}
	return tools, nil
}
