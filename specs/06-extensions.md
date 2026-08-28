# 06. Extensions and Moving Parts

**Status:** Draft

> Extensions customize named Runtime stages. They may transform a value, decide whether work proceeds, observe a fact, or stop a Turn; they do not own Core state transitions.

## 1. Capability interfaces

The draft Go shapes are:

```go
type ContextSnapshot struct {
    Messages []Message
    ActiveToolSets []ToolSetName
}

type TurnSnapshot struct {
    RunID RunID
    Number TurnNumber
    Assistant Message
}

type PreToolAction string
const (
    PreToolProceed PreToolAction = "proceed"
    PreToolBlock   PreToolAction = "block"
)

type ToolOutcome = ToolResult


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
    After(context.Context, ToolOutcome) (ToolOutcome, error)
}

type EventObserver interface {
    Observe(context.Context, Event) error
}

type TurnStopper interface {
    Stop(context.Context, TurnSnapshot) (StopDecision, error)
}
```

Exact package names MAY evolve, but each capability MUST have one narrow responsibility and MUST receive the Run Context. An Extension MUST NOT receive a mutable Agent pointer or a transport stream.

## 2. Installation

Extensions are installed explicitly as part of Agent construction:

```text
WithExtension(A)
WithExtension(B)
WithExtension(C)
```

The resulting order is immutable for the Agent lifetime. Installation MUST reject nil values and invalid duplicate registrations according to the selected composition policy. Runtime invocation order MUST be deterministic across direct and service callers.

## 3. Context transformation

Before each Model request:

```text
read-only Agent snapshot
        ↓
ContextTransformer chain
        ↓
new runtime Message sequence
        ↓
MessageConverter chain
        ↓
Model request
```

A Transformer receives a snapshot, not a live mutable transcript. It MAY select, add, prune, compact, or annotate context. It MUST return a new value, honor cancellation, and leave committed Agent history unchanged.

A transformer that returns an error blocks the Model call and terminates the Run unless an explicit application policy converts it into a safe continuation. The default is blocking.

## 4. Message conversion

Converters run in installation order. Each converter receives the prior converter's output and returns a new value. A converter MUST preserve Message role and Tool identity unless its contract explicitly describes a provider-compatible representation change.

The final converter output is consumed only by the Model adapter. It MUST NOT be stored as the Runtime transcript.

## 5. Pre-Tool-Use

Pre-Tool-Use executes after complete argument assembly, Tool resolution, and Schema validation:

```go
type PreToolDecision struct {
    Action          PreToolAction
    Result          *ToolResult
    TerminationHint string
}
```

`Action` is one of:

```text
proceed
block
```

All installed Pre-Tool-Use components run in installation order until one blocks or fails. A block skips the executor, creates a blocked Tool Result, and still passes through Post-Tool-Use. Validated ToolUse arguments are immutable during this chain.

Typical policies include authorization, approval, audit preparation, and rate limits. They MUST NOT execute the Tool as a side effect of inspection.

## 6. Tool execution boundary

The Runtime invokes one resolved Tool executor at most once after every Pre-Tool-Use component proceeds. Context and bounded progress reporting are passed unchanged through the boundary.

An Extension MUST NOT replay an arbitrary Tool Use. Retry belongs to an idempotency-aware Tool or explicit policy that creates a new Tool Use identity.

## 7. Post-Tool-Use

Post-Tool-Use receives every finalized outcome, including:

```text
executed success
executed failure
blocked by policy
cancelled before executor
limit-rejected use
```

The chain runs in reverse installation order. Each component MAY normalize safe content, attach typed metadata, redact fields, enforce result bounds, or add a termination hint. It MUST preserve `Executed` truth and Tool Call identity.

A blocking Post-Tool-Use error terminates the Run after prior committed state remains intact. It MUST NOT cause the Tool executor to run again.

## 8. Event observers

An EventObserver receives canonical Events in production order and is awaited before the Runtime continues:

```text
create Event
  ↓
observer A
  ↓
observer B
  ↓
loop continues
```

Observers run in registration order. An observer MUST be in-process, Context-aware, and bounded by its own work. It MUST NOT wait on a network peer, an unbounded queue, or a remote lock.

Observer failures use an explicit mode:

```text
blocking  return failure and terminate the current operation
advisory  record/report failure and preserve the Run outcome
```

Panic recovery occurs at the observer boundary. A recovered panic follows the configured blocking or advisory mode and MUST NOT escape the Run goroutine.

## 9. Turn stopping

A TurnStopper runs after `turn_end` and before queue polling or another Model request:

```text
turn_end
  ↓
TurnStopper
  ↓
Steering
  ↓
Tool continuation
  ↓
Follow-up
  ↓
completion
```

A stop decision settles the Run while preserving the completed Turn. A stopper error is blocking by default.

## 10. Ordering and reentrancy

Extensions MUST NOT synchronously call `Prompt`, `Continue`, or `Reset` on the same Agent from inside an awaited stage. Such reentrancy would violate the one-writer rule and MUST return a typed invalid-state error or be rejected by construction.

Extensions MAY enqueue application work outside the Runtime only when that work has an explicit Context and settlement owner. They MUST NOT create detached goroutines.

## 11. Failure semantics

| Capability | Default failure | State effect |
|---|---|---|
| ContextTransformer | blocking | no Model request |
| MessageConverter | blocking | no Model request |
| PreToolUse | blocking | no executor for current use |
| PostToolUse | blocking | outcome not committed as final |
| EventObserver | explicit mode | blocking or advisory |
| TurnStopper | blocking | terminal Run |

A Tool execution error is not an Extension failure and follows Tool Result semantics. Service projection, logging, and telemetry are not allowed to reclassify an Extension failure.

## 12. Service-side Moving Parts

These contracts remain outside the Runtime:

```text
AgentFactory
AgentCache
AdmissionController
EventProjector
EventBridge
ErrorMapper
DrainPolicy
```

They may observe or wrap Runtime operations, but they MUST NOT duplicate the canonical loop or mutate Runtime transcript state behind the Agent API.
