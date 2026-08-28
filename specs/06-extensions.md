# 06. Extensions and Moving Parts

**Status:** Draft

> Extensions customize named Core stages; they do not own Agent state or Hosted coordination.

## 1. Interfaces

```go
type ContextTransformer interface {
    Transform(context.Context, ContextSnapshot) ([]Message, error)
}

type MessageConverter interface {
    Convert(context.Context, []Message) ([]ModelMessage, error)
}

type PreToolUse interface {
    Before(context.Context, ToolUse) (PreToolDecision, error)
}

type PostToolUse interface {
    After(context.Context, ToolResult) (ToolResult, error)
}

type EventObserver interface {
    Observe(context.Context, Event) error
}

type TurnStopper interface {
    Stop(context.Context, TurnSnapshot) (StopDecision, error)
}
```

Exact package names may evolve, but each interface has one responsibility and receives the owning Run Context. Extensions do not receive mutable Agent pointers or transport streams.

## 2. Installation and order

Extensions are installed explicitly during Agent construction. The resulting order is immutable for the Agent lifetime. Invocation order is deterministic in Embedded and Hosted modes.

## 3. Context and conversion

Transformers receive read-only snapshots and return new values. They may select, add, prune, or compact context but cannot mutate committed history. Converters run in order and preserve roles and Tool identity unless a provider representation change is explicit. Neither output is stored as the Core transcript.

## 4. Tool stages

Pre-Tool-Use runs after complete assembly, resolution, and Schema validation. Components run in installation order until block or failure. A block creates a blocked result and skips execution.

Post-Tool-Use receives executed, blocked, failed, and cancelled outcomes and runs in reverse installation order. It may normalize safe content, redact, add bounded metadata, or add a termination hint. It must preserve identity and `Executed` truth.

## 5. Observers

Observers receive canonical Events in production order and are awaited before Core continues. They must be local, fast, Context-aware, and bounded. Failure mode is explicitly blocking or advisory. Panics are recovered.

## 6. Turn stopping

A TurnStopper runs after `turn_end` and before continuation selection. A stop preserves the Turn and settles the Run. A stopper error is blocking by default.

## 7. Failure and reentrancy

Transformer, converter, Pre, Post, and stopper errors block by default and settle the owning Run according to the failure contract. Tool executor errors use Tool Result semantics. An Extension cannot synchronously call Prompt, Continue, or Reset on the same Agent from an awaited stage.

Extensions may schedule application work only with an explicit Context and result channel. Unbounded or fire-and-forget goroutines are forbidden.

## 8. Host components are separate

The following are Host components, not Core Extensions:

```text
AgentFactory · ConversationResolver · AdmissionController
AgentCache · EventProjector · EventBridge · ErrorMapper · DrainPolicy
```

They may wrap Core operations but may not mutate Core transcript state or create another loop.
