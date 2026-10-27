package gotato

import (
	"context"
)

type Tool interface {
	Spec() ToolSpec
	Execute(context.Context, ToolUse, ToolProgressFunc) (ToolResult, error)
}

type ToolSpec struct {
	ID           string            `json:"id"`
	Name         string            `json:"name,omitempty"`
	Description  string            `json:"description,omitempty"`
	InputSchema  []byte            `json:"input_schema,omitempty"`
	OutputSchema []byte            `json:"output_schema,omitempty"`
	Sequential   bool              `json:"sequential,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	// Effect classifies what the tool changes. Core copies it into ToolUse,
	// and a Session built WithMaxEffect hides every tool above its cap.
	Effect ToolEffect `json:"effect,omitempty"`
}

// ToolEffect classifies a tool's side effects, ordered by impact:
// ToolEffectRead < ToolEffectWriteLocal < ToolEffectWriteExternal < ToolEffectDestructive.
// An application uses it to choose the tools an agent loads, or to gate a
// call in a PreToolUse extension.
type ToolEffect string

const (
	// ToolEffectUnspecified ranks as ToolEffectDestructive, so filters fail closed.
	ToolEffectUnspecified ToolEffect = ""
	// ToolEffectRead observes state and changes nothing.
	ToolEffectRead ToolEffect = "read"
	// ToolEffectWriteLocal changes local, recoverable state such as files in a
	// working directory.
	ToolEffectWriteLocal ToolEffect = "write_local"
	// ToolEffectWriteExternal changes state outside the host, such as a remote
	// API, a message, or a push.
	ToolEffectWriteExternal ToolEffect = "write_external"
	// ToolEffectDestructive deletes or overwrites state that is hard to recover.
	ToolEffectDestructive ToolEffect = "destructive"
)

// rank orders effects by impact, from 1 for ToolEffectRead to 4 for
// ToolEffectDestructive. ToolEffectUnspecified and unknown values rank 4.
func (e ToolEffect) rank() int {
	switch e {
	case ToolEffectRead:
		return 1
	case ToolEffectWriteLocal:
		return 2
	case ToolEffectWriteExternal:
		return 3
	default:
		return 4
	}
}

// Within reports whether e is at or below ceiling.
func (e ToolEffect) Within(ceiling ToolEffect) bool { return e.rank() <= ceiling.rank() }

// Valid reports whether e is ToolEffectUnspecified or one of the named effects.
func (e ToolEffect) Valid() bool {
	switch e {
	case ToolEffectUnspecified, ToolEffectRead, ToolEffectWriteLocal, ToolEffectWriteExternal, ToolEffectDestructive:
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
}

type ToolProgressFunc func(string)
