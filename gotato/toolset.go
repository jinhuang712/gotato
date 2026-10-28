package gotato

import (
	"context"
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
// [LIVENESS] a ToolSet whose Spec panics fails NewSession, not the process.
func toolSetSpec(set ToolSet) (spec ToolSetSpec, err error) {
	if failure := callHost("ToolSet.Spec", func() error {
		spec = set.Spec()
		return nil
	}); failure != nil {
		return ToolSetSpec{}, runtimeError(CodeExtensionFailure, "ToolSet", PublicText(failure, "ToolSet.Spec failed"), failure)
	}
	return spec, nil
}

func resolveToolSet(ctx context.Context, state *toolSetState) ([]Tool, error) {
	var tools []Tool
	err := callHost("ToolSet", func() (failure error) {
		tools, failure = state.set.Tools(ctx)
		return failure
	})
	if err != nil {
		return nil, runtimeError(CodeToolResolutionFailure, "ToolSet", PublicText(err, "cannot resolve ToolSet "+state.spec.Name), err)
	}
	local := map[string]bool{}
	for _, tool := range tools {
		// [LIVENESS] a nil Tool would panic the process at its first Spec call, outside the host-panic recovery.
		if tool == nil {
			return nil, runtimeError(CodeInvalidArgument, "ToolSet", "ToolSet "+state.spec.Name+" returned a nil Tool", nil)
		}
		spec := tool.Spec()
		if err := validateToolSpec(spec); err != nil {
			return nil, err
		}
		// [CONTRACT] a Tool ID is unique within its ToolSet.
		if local[spec.ID] {
			return nil, runtimeError(CodeInvalidArgument, "ToolSet", "duplicate local Tool name in ToolSet "+state.spec.Name+": "+spec.ID, nil)
		}
		local[spec.ID] = true
	}
	return tools, nil
}

// toolSetState tracks one installed ToolSet. Its Tools are resolved at
// NewSession and again at the start of every Run, never inside a Run, so the
// catalog is the same on every request of a Run.
type toolSetState struct {
	set      ToolSet
	spec     ToolSetSpec
	mode     installMode
	resolved []Tool
	// failure is why the last resolution failed; nil when the ToolSet is
	// available. An unavailable ToolSet holds no Tools.
	failure error
}
