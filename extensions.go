package gotato

import (
	"context"
	"fmt"
	"runtime/debug"
	"slices"
)

// ContextSnapshot is the read-only view a ContextTransformer receives. It
// carries copies: an Extension cannot mutate committed history through it.
type ContextSnapshot struct {
	AgentID            AgentID
	RunID              RunID
	Turn               TurnNumber
	SystemInstructions string
	Messages           []Message
}

// TurnSnapshot is the read-only view a TurnStopper receives after turn_end.
type TurnSnapshot struct {
	AgentID     AgentID
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
// failed, and cancelled outcomes. It must preserve identity and Executed
// truth.
type PostToolUse interface {
	After(context.Context, ToolResult) (ToolResult, error)
}

// EventObserver receives canonical Events in production order and is awaited
// before Core continues. It must be local, fast, Context-aware, and bounded.
type EventObserver interface {
	Observe(context.Context, Event) error
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
// order is immutable for the Agent lifetime.
func WithExtension(extension any) Option {
	return func(c *agentConfig) error {
		if extension == nil {
			return runtimeError(ErrInvalidArgument, "WithExtension", "extension is nil", nil)
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
		if typed, ok := extension.(EventObserver); ok {
			c.extensions.observers = append(c.extensions.observers, typed)
			installed = true
		}
		if typed, ok := extension.(TurnStopper); ok {
			c.extensions.stoppers = append(c.extensions.stoppers, typed)
			installed = true
		}
		if !installed {
			return runtimeError(ErrInvalidArgument, "WithExtension", "value implements no Extension interface", nil)
		}
		return nil
	}
}

// WithExtensions installs several Extensions in the given order.
func WithExtensions(extensions ...any) Option {
	return func(c *agentConfig) error {
		for _, extension := range extensions {
			if err := WithExtension(extension)(c); err != nil {
				return err
			}
		}
		return nil
	}
}

type extensionSet struct {
	transformers []ContextTransformer
	converters   []MessageConverter
	pre          []PreToolUse
	post         []PostToolUse
	observers    []EventObserver
	stoppers     []TurnStopper
}

func (s extensionSet) empty() bool {
	return len(s.transformers) == 0 && len(s.converters) == 0 && len(s.pre) == 0 &&
		len(s.post) == 0 && len(s.observers) == 0 && len(s.stoppers) == 0
}

// advisoryFailure reports whether a failure from this Extension is advisory.
// Blocking is the default.
func advisoryFailure(extension any) bool {
	if typed, ok := extension.(AdvisoryExtension); ok {
		return typed.Advisory()
	}
	return false
}

// guard runs one Extension stage, converting a panic into an ordinary error so
// a misbehaving Extension cannot take down the Agent goroutine.
func guard(stage string, call func() error) (err *RuntimeError) {
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = debug.Stack()
			err = runtimeError(ErrExtensionFailure, stage, fmt.Sprintf("extension panic: %v", recovered), nil)
		}
	}()
	if failure := call(); failure != nil {
		return runtimeError(ErrExtensionFailure, stage, failure.Error(), failure)
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
			if advisoryFailure(current) {
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
			if advisoryFailure(current) {
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
		view.ArgumentsJSON = slices.Clone(use.ArgumentsJSON)
		if err := guard("PreToolUse", func() error {
			result, failure := current.Before(ctx, view)
			decision = result
			return failure
		}); err != nil {
			if advisoryFailure(current) {
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
	for i := len(s.post) - 1; i >= 0; i-- {
		var produced ToolResult
		current := s.post[i]
		input := result.Clone()
		if err := guard("PostToolUse", func() error {
			out, failure := current.After(ctx, input)
			produced = out
			return failure
		}); err != nil {
			if advisoryFailure(current) {
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

// observe awaits every observer at the Event boundary.
func (s extensionSet) observe(ctx context.Context, event Event) *RuntimeError {
	for _, observer := range s.observers {
		current := observer
		if err := guard("EventObserver", func() error { return current.Observe(ctx, event) }); err != nil {
			if advisoryFailure(current) {
				continue
			}
			return err
		}
	}
	return nil
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
			if advisoryFailure(current) {
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
