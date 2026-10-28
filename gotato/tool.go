package gotato

import (
	"context"
)

// Tool is one capability a Session can call. Spec describes it; Execute runs it.
//
// Execute must honor ctx: the Session cancels it when the Run is aborted or the
// call passes its deadline, and a Tool that ignores it holds the Run until it
// returns. progress may be called from Execute to report text while the call
// runs; the Session bounds how much it keeps. An error from Execute, or a panic,
// becomes a failed ToolResult; a Tool reports a failure it wants the Model to
// read by returning a ToolResult with a failed Status.
//
// An error's text is not sent on: it may carry a host, a query or a secret. The
// failed ToolResult says "tool failed" with a code, unless the error implements
// PublicError, whose text the Model and every subscriber may read.
type Tool interface {
	Spec() ToolSpec
	Execute(context.Context, ToolUse, ToolProgressFunc) (ToolResult, error)
}

// PublicError is an error whose text is safe to show the Model and every
// subscriber. A Tool returns one to say why it failed in words the Model can
// act on; any other error it returns becomes "tool failed" with a code.
type PublicError interface {
	error
	Public() string
}

// ToolSpec describes a Tool to the Model and to the Session. ID is the Tool's
// unique name; InputSchema is the JSON Schema its arguments are checked against
// before Execute runs. A Sequential Tool never runs beside another Tool of the
// same Turn.
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

// ToolUse is one call as Execute receives it: where in the Run it is, and the
// Tool's ID and arguments.
type ToolUse struct {
	RunID       RunID      `json:"run_id"`
	Turn        TurnNumber `json:"turn"`
	CallID      ToolCallID `json:"call_id"`
	ToolID      string     `json:"tool_id"`
	Arguments   []byte     `json:"arguments"`
	SourceIndex uint32     `json:"source_index"`
	// Effect is the called tool's ToolSpec.Effect, so a PreToolUse extension
	// can gate the call by impact.
	Effect ToolEffect `json:"effect,omitempty"`
}

// ToolProgressFunc reports a piece of text about a running Tool call. It is
// safe to call from the goroutine that runs Execute.
type ToolProgressFunc func(string)
