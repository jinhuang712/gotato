package gotato

import (
	"context"
)

type Tool interface {
	Spec() ToolSpec
	Execute(context.Context, ToolUse, ToolProgress) (ToolResult, error)
}

type ToolSpec struct {
	ID           string            `json:"id"`
	Name         string            `json:"name,omitempty"`
	Description  string            `json:"description,omitempty"`
	InputSchema  []byte            `json:"input_schema,omitempty"`
	OutputSchema []byte            `json:"output_schema,omitempty"`
	Sequential   bool              `json:"sequential,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	// Effect classifies what the tool changes. Core records it, copies it
	// into ToolUse, and leaves enforcement to the application.
	Effect ToolEffect `json:"effect,omitempty"`
}

// ToolEffect classifies a tool's side effects, ordered by impact:
// EffectRead < EffectWriteLocal < EffectWriteExternal < EffectDestructive.
// An application uses it to choose the tools an agent loads, or to gate a
// call in a PreToolUse extension.
type ToolEffect string

const (
	// EffectUnspecified ranks as EffectDestructive, so filters fail closed.
	EffectUnspecified ToolEffect = ""
	// EffectRead observes state and changes nothing.
	EffectRead ToolEffect = "read"
	// EffectWriteLocal changes local, recoverable state such as files in a
	// working directory.
	EffectWriteLocal ToolEffect = "write_local"
	// EffectWriteExternal changes state outside the host, such as a remote
	// API, a message, or a push.
	EffectWriteExternal ToolEffect = "write_external"
	// EffectDestructive deletes or overwrites state that is hard to recover.
	EffectDestructive ToolEffect = "destructive"
)

// Rank orders effects by impact, from 1 for EffectRead to 4 for
// EffectDestructive. EffectUnspecified and unknown values rank 4.
func (e ToolEffect) Rank() int {
	switch e {
	case EffectRead:
		return 1
	case EffectWriteLocal:
		return 2
	case EffectWriteExternal:
		return 3
	default:
		return 4
	}
}

// Within reports whether e is at or below ceiling.
func (e ToolEffect) Within(ceiling ToolEffect) bool { return e.Rank() <= ceiling.Rank() }

// Valid reports whether e is EffectUnspecified or one of the named effects.
func (e ToolEffect) Valid() bool {
	switch e {
	case EffectUnspecified, EffectRead, EffectWriteLocal, EffectWriteExternal, EffectDestructive:
		return true
	}
	return false
}

// ToolWithEffect returns tool with its spec's Effect set to effect. It suits
// tools built by NewFuncTool and tools from sources that leave Effect unset.
func ToolWithEffect(tool Tool, effect ToolEffect) Tool {
	return effectTool{Tool: tool, effect: effect}
}

type effectTool struct {
	Tool
	effect ToolEffect
}

func (t effectTool) Spec() ToolSpec {
	spec := t.Tool.Spec()
	spec.Effect = t.effect
	return spec
}

type ToolUse struct {
	RunID         RunID      `json:"run_id"`
	Turn          TurnNumber `json:"turn"`
	CallID        ToolCallID `json:"call_id"`
	QualifiedID   string     `json:"qualified_id"`
	ArgumentsJSON []byte     `json:"arguments_json"`
	SourceIndex   uint32     `json:"source_index"`
	// Effect is the called tool's ToolSpec.Effect, so a PreToolUse extension
	// can gate the call by impact.
	Effect ToolEffect `json:"effect,omitempty"`
	// Executed and Result are reserved. The Core Loop does not assign them, so
	// a Tool or PreToolUse extension always sees Executed=false and Result=nil.
	// They are kept for a future PostToolUse view; removing them changes the
	// JSON shape and is tracked in TODO.md.
	Executed bool        `json:"executed"`
	Result   *ToolResult `json:"result,omitempty"`
}

type ToolProgress func(string)
