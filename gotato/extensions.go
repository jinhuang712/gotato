package gotato

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
)

// ContextSnapshot is the read-only view a ContextTransformer receives. It
// carries copies: an Extension cannot mutate committed history through it.
type ContextSnapshot struct {
	SessionID          SessionID
	RunID              RunID
	Turn               TurnNumber
	SystemInstructions string
	Messages           []Message
}

// TurnSnapshot is the read-only view a TurnStopper receives after turn_end.
// Assistant.StopReason is why the Model stopped, StopReasonMaxTokens for a reply
// the output limit cut off. StopReason is why the Turn stopped.
type TurnSnapshot struct {
	SessionID   SessionID
	RunID       RunID
	Turn        TurnNumber
	Assistant   Message
	ToolResults []ToolResult
	StopReason  StopReason
}

// PreToolDecision blocks a Tool before its executor runs. A blocked use keeps
// its identity and settles with Executed false.
type PreToolDecision struct {
	Block  bool
	Reason string
	// Result optionally replaces the blocked outcome. Core forces its
	// identity, status, and Executed truth.
	Result *ToolResult
}

// StopDecision ends the Run after the current Turn. The Turn is preserved.
type StopDecision struct {
	Stop   bool
	Reason string
}

// ContextTransformer selects, adds, prunes, or compacts the context sent to
// the Model. Its output is not stored as the Core transcript.
type ContextTransformer interface {
	Transform(context.Context, ContextSnapshot) ([]Message, error)
}

// MessageConverter adapts committed Messages into the shape one Model
// expects. Converters run in installation order and preserve roles and Tool
// identity unless a provider representation change is explicit.
type MessageConverter interface {
	Convert(context.Context, []Message) ([]Message, error)
}

// PreToolUse runs after complete assembly, resolution, and Schema validation.
// Components run in installation order until one blocks or fails.
type PreToolUse interface {
	Before(context.Context, ToolUse) (PreToolDecision, error)
}

// PostToolUse runs in reverse installation order over executed, blocked,
// failed, and canceled outcomes. It must preserve identity and Executed
// truth.
type PostToolUse interface {
	After(context.Context, ToolResult) (ToolResult, error)
}

// TurnStopper runs after turn_end and before continuation selection.
type TurnStopper interface {
	Stop(context.Context, TurnSnapshot) (StopDecision, error)
}

// AdvisoryExtension marks an Extension whose failure is recorded rather than
// settling the owning Run. Extensions are blocking by default.
type AdvisoryExtension interface {
	Advisory() bool
}

// WithExtension installs one Extension. A value may implement several
// Extension interfaces; it is installed into each of them. The resulting
// order is immutable for the Session lifetime. It fails with
// CodeInvalidArgument when the value implements no Extension interface, or has
// a method named like a hook (Transform, Convert, Before, After, Stop,
// PrepareTurn, Advisory) whose signature does not match it.
func WithExtension(extension any) SessionOption {
	return func(c *sessionConfig) error {
		if extension == nil {
			return runtimeError(CodeInvalidArgument, "WithExtension", "extension is nil", nil)
		}
		if err := checkExtensionMethods(extension); err != nil {
			return err
		}
		installed := false
		if typed, ok := extension.(ContextTransformer); ok {
			c.extensions.transformers = append(c.extensions.transformers, typed)
			installed = true
		}
		if typed, ok := extension.(MessageConverter); ok {
			c.extensions.converters = append(c.extensions.converters, typed)
			installed = true
		}
		if typed, ok := extension.(PreToolUse); ok {
			c.extensions.pre = append(c.extensions.pre, typed)
			installed = true
		}
		if typed, ok := extension.(PostToolUse); ok {
			c.extensions.post = append(c.extensions.post, typed)
			installed = true
		}
		if typed, ok := extension.(TurnStopper); ok {
			c.extensions.stoppers = append(c.extensions.stoppers, typed)
			installed = true
		}
		if typed, ok := extension.(TurnPreparer); ok {
			c.extensions.preparers = append(c.extensions.preparers, typed)
			installed = true
		}
		if !installed {
			return runtimeError(CodeInvalidArgument, "WithExtension", "value implements no Extension interface", nil)
		}
		return nil
	}
}

// hookMethods maps the name of each hook method to the interface it belongs
// to.
var hookMethods = map[string]reflect.Type{
	"Transform":   reflect.TypeFor[ContextTransformer](),
	"Convert":     reflect.TypeFor[MessageConverter](),
	"Before":      reflect.TypeFor[PreToolUse](),
	"After":       reflect.TypeFor[PostToolUse](),
	"Stop":        reflect.TypeFor[TurnStopper](),
	"PrepareTurn": reflect.TypeFor[TurnPreparer](),
	"Advisory":    reflect.TypeFor[AdvisoryExtension](),
}

// checkExtensionMethods fails when the value has a method named like a hook
// but the value does not satisfy that hook's interface, such as a Before with
// the wrong signature. Without the check the value installs for the other
// stages it does satisfy, and a safety hook fails open on a spelling mistake.
func checkExtensionMethods(extension any) *RuntimeError {
	value := reflect.TypeOf(extension)
	for name, iface := range hookMethods {
		if _, has := value.MethodByName(name); has && !value.Implements(iface) {
			return runtimeError(CodeInvalidArgument, "WithExtension",
				fmt.Sprintf("%s has a method %s that does not match %s, so the hook would not run", value, name, iface.Name()), nil)
		}
	}
	return nil
}

type extensionSet struct {
	transformers []ContextTransformer
	converters   []MessageConverter
	pre          []PreToolUse
	post         []PostToolUse
	stoppers     []TurnStopper
	preparers    []TurnPreparer
	// report tells the Session that an advisory extension failed, so it
	// stores an ExtensionFailed Event. It is set by NewSession.
	report func(stage string, err *RuntimeError)
}

func (s extensionSet) empty() bool {
	return len(s.transformers) == 0 && len(s.converters) == 0 && len(s.pre) == 0 &&
		len(s.post) == 0 && len(s.stoppers) == 0 && len(s.preparers) == 0
}

// prepareTurn runs every TurnPreparer in installation order before a model
// call, and reports whether any of them rewrote the Transcript. A failure that
// is already a RuntimeError keeps its code, so a preparer can end the Run with
// CodeLimitExceeded or CodeContextOverflow.
func (s extensionSet) prepareTurn(ctx context.Context, pending PendingTurn) (bool, *RuntimeError) {
	changed := false
	for _, preparer := range s.preparers {
		current := preparer
		var prepared TurnPrepared
		err := guard("TurnPreparer", func() error {
			var failure error
			prepared, failure = current.PrepareTurn(ctx, pending)
			return failure
		})
		if err != nil {
			if s.advisory(current, err) {
				continue
			}
			var own *RuntimeError
			if errors.As(err.Cause, &own) && own != nil {
				return changed, own
			}
			// A preparer that failed while the Run was being stopped reports
			// the stop, not a failure of its own.
			if stopped := contextFailure(ctx, "TurnPreparer"); stopped != nil {
				return changed, stopped
			}
			return changed, err
		}
		changed = changed || prepared.Changed
	}
	return changed, nil
}

// advisory reports whether a failure from this Extension is advisory, and
// reports it as an ExtensionFailed Event when it is. Blocking is the default.
func (s extensionSet) advisory(extension any, failure *RuntimeError) bool {
	typed, ok := extension.(AdvisoryExtension)
	if !ok || !typed.Advisory() {
		return false
	}
	if s.report != nil && failure != nil {
		s.report(failure.Operation, failure)
	}
	return true
}

// guard runs one Extension stage, converting a panic into an ordinary error so
// a misbehaving Extension cannot take down the Session goroutine.
// [LIVENESS] a panicking Extension would end the Session goroutine; its text stays on the Cause, not the Message a client reads.
func guard(stage string, call func() error) *RuntimeError {
	if failure := callHost("extension", call); failure != nil {
		// The extension's own text, or what it panicked with, stays on the
		// Cause: Message reaches HTTP clients, and a foreign error may carry
		// what they must not read.
		return runtimeError(CodeExtensionFailure, stage, PublicText(failure, "extension failed"), failure)
	}
	return nil
}

// transformContext applies the transformers and then the converters. Neither
// output is committed: this is the value handed to the Model for one Turn.
func (s extensionSet) transformContext(ctx context.Context, snapshot ContextSnapshot) ([]Message, *RuntimeError) {
	messages := snapshot.Messages
	for _, transformer := range s.transformers {
		var produced []Message
		current := transformer
		view := snapshot
		view.Messages = cloneMessages(messages)
		if err := guard("ContextTransformer", func() error {
			result, failure := current.Transform(ctx, view)
			produced = result
			return failure
		}); err != nil {
			if s.advisory(current, err) {
				continue
			}
			return nil, err
		}
		messages = produced
	}
	for _, converter := range s.converters {
		var produced []Message
		current := converter
		input := cloneMessages(messages)
		if err := guard("MessageConverter", func() error {
			result, failure := current.Convert(ctx, input)
			produced = result
			return failure
		}); err != nil {
			if s.advisory(current, err) {
				continue
			}
			return nil, err
		}
		messages = produced
	}
	return messages, nil
}

// beforeTool runs the Pre chain in installation order. The first block or
// blocking failure ends the chain.
func (s extensionSet) beforeTool(ctx context.Context, use ToolUse) (PreToolDecision, *RuntimeError) {
	for _, component := range s.pre {
		var decision PreToolDecision
		current := component
		view := use
		view.Arguments = slices.Clone(use.Arguments)
		if err := guard("PreToolUse", func() error {
			result, failure := current.Before(ctx, view)
			decision = result
			return failure
		}); err != nil {
			if s.advisory(current, err) {
				continue
			}
			return PreToolDecision{}, err
		}
		if decision.Block {
			return decision, nil
		}
	}
	return PreToolDecision{}, nil
}

// afterTool runs the Post chain in reverse installation order over every
// outcome, then restores the identity and Executed truth Core owns.
func (s extensionSet) afterTool(ctx context.Context, result ToolResult) (ToolResult, *RuntimeError) {
	identity := result.CallID
	executed := result.Executed
	for _, current := range slices.Backward(s.post) {
		var produced ToolResult

		input := result.Clone()
		if err := guard("PostToolUse", func() error {
			out, failure := current.After(ctx, input)
			produced = out
			return failure
		}); err != nil {
			if s.advisory(current, err) {
				continue
			}
			return result, err
		}
		produced.CallID = identity
		produced.Executed = executed
		result = produced
	}
	return result, nil
}

// stopTurn asks every stopper whether the Run ends after this Turn.
func (s extensionSet) stopTurn(ctx context.Context, snapshot TurnSnapshot) (StopDecision, *RuntimeError) {
	for _, stopper := range s.stoppers {
		var decision StopDecision
		current := stopper
		if err := guard("TurnStopper", func() error {
			result, failure := current.Stop(ctx, snapshot)
			decision = result
			return failure
		}); err != nil {
			if s.advisory(current, err) {
				continue
			}
			return StopDecision{}, err
		}
		if decision.Stop {
			return decision, nil
		}
	}
	return StopDecision{}, nil
}
