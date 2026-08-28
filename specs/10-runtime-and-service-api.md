# 10. Runtime and Service API

**Status:** Draft

> One Runtime boundary serves the hosted service and a direct Go caller. The API exposes operations and immutable values; internal channels, locks, provider types, and transport envelopes remain hidden.

## 1. Runtime interface

The draft Go surface is:

```go
type Agent interface {
    Prompt(context.Context, Message) (RunResult, error)
    Continue(context.Context) (RunResult, error)
    Steer(Message) error
    FollowUp(Message) error
    Abort()
    WaitForIdle(context.Context) error
    Subscribe(EventHandler) (unsubscribe func())
    StateSnapshot() AgentSnapshot
    Reset() error
}

type EventHandler interface {
    Observe(context.Context, Event) error
}
```

`Prompt` and `Continue` return after execution settlement. `Subscribe` receives canonical Events while the Run is active. `WaitForIdle` waits for the active Run, owned child Routines, and terminal observers; it MUST NOT wait for remote delivery.

Exact package names and method receivers MAY evolve, but the following behaviors MUST remain:

- one Agent has at most one active mutating Run;
- `Abort` is idempotent;
- queue operations preserve acceptance order;
- snapshots are immutable from the caller's perspective;
- `Reset` is rejected while a Run is active;
- an unsubscribe prevents future handler calls after its own synchronization barrier returns.

## 2. Runtime construction

Construction validates all static configuration before the first Run:

```go
New(
    WithModel(model),
    WithTool(tool),
    WithTools(tools...),
    WithToolSet(toolSet),
    WithToolSets(toolSets...),
    WithExtension(extension),
    WithLimits(limits),
)
```

Construction MUST reject nil dependencies, duplicate names, invalid Schemas, duplicate qualified Tool IDs, invalid limits, and incompatible options. It MUST NOT defer static errors until the first Prompt.

The constructed Agent owns the lifecycle of dependencies it is explicitly documented to own. Provider clients and application services may be shared immutable dependencies; mutable transcript and queue state is never shared between Agents unless an explicit state provider restores it.

## 3. Event subscription

A subscription is an in-process observer, not a remote stream:

```go
type EventHandler interface {
    Observe(context.Context, Event) error
}
```

The Runtime invokes handlers in registration order and awaits each handler before progressing. A handler MUST be Context-aware and bounded. The Runtime MUST recover handler panics and apply the selected blocking or advisory policy.

Unsubscribe MUST be safe to call more than once. After it returns, no new handler invocation may begin for that subscription. An invocation already in progress is settled by its Event Context before the unsubscribe call returns or returns its own documented cancellation error.

## 4. Queue operations

`Steer` and `FollowUp` are synchronous acceptance operations:

```text
validate Message
  ↓
lock Agent queue state
  ↓
check Run state and queue bound
  ↓
append in acceptance order
  ↓
unlock
```

They MUST NOT block on Model, Tool, Routine, network, or observer work. A queue-full, terminal, invalid-message, or Context-independent policy error is returned at acceptance time.

## 5. Service registration

The service preset assembles focused hosted-access components:

```go
service, err := agentservice.New(
    agentservice.WithAgent("incident", incidentFactory),
    agentservice.WithAgent("repository", repositoryFactory),
)
```

Registration MUST be deterministic and eager. Unknown options, duplicate Agent names, nil factories, invalid service bounds, and conflicting lifecycle policies fail construction.

The preset owns request validation, Agent resolution, admission, cache leases, Event projection, Event bridge, error mapping, readiness, and drain. It invokes the Runtime API rather than reproducing the Agent loop.

## 6. Agent factory

```go
type AgentFactory interface {
    NewAgent(context.Context, AgentRequest) (*Agent, error)
}

type AgentRequest struct {
    AgentName       AgentName
    ConversationKey ConversationKey
    RequestID       string
    Metadata        map[string]string
}
```

A factory MUST return an isolated Agent or restore one through an explicit state capability. It MUST NOT return the same mutable Agent to two concurrent conversation keys. Factory Context cancellation prevents construction from becoming an unowned background task.

## 7. Package boundary

A coherent implementation MAY use:

```text
cmd/gotatod           executable service
service/              factories, cache, admission, delivery, lifecycle
transport/grpc/       Protobuf mapping and server/client
routines/             child Runs, groups, Results
extension/            Runtime stage contracts
tool/                 Tool, ToolSet, Schema helpers
event/                Event projection and sinks
testkit/              deterministic fakes
internal/runtime/     canonical loop and Agent state
model/                Model contracts and stream Events
```

The runtime package MUST NOT import generated Protobuf, gRPC, Kubernetes, cache, database, or process-hosting packages. Transport types MUST be mapped at the boundary.

## 8. Metadata

Core behavior uses typed fields. Metadata is bounded, copied at boundaries, and reserved for correlation or adapter round-tripping. Metadata MUST NOT override Run ID, Event kind, Event order, Tool identity, or terminal status.

Request metadata, trace identifiers, provider call IDs, and application labels are additive. Secrets and raw prompts are not default metadata.

## 9. Routine usage

The Routine package composes the Runtime API:

```go
routine, err := routines.Spawn(ctx, childFactory, request)
if err != nil {
    return err
}
result, err := routine.Wait(ctx)
```

It MUST NOT introduce another Model/Tool loop. Group Results are immutable and, by default, ordered by spawn position even when completion Events arrive out of order.

## 10. Direct/service equivalence

The direct caller and service adapter MUST use the same Agent, Run, Message, Tool, Event, Context, and settlement contracts. Differences are limited to:

```text
wire mapping
remote command acceptance
conversation resolution
admission
projection and delivery
process lifecycle
```

Equivalent inputs and scripted dependencies MUST produce identical canonical Event sequences and terminal Runtime outcomes. A service-specific workaround that changes those facts is a contract violation.

## 11. Internal implementation rules

Internal channels, mutexes, atomics, worker pools, provider clients, and goroutines are implementation details. They MUST have an owner, Context, and settlement boundary, but they MUST NOT become accidental public API through returned channels or mutable callbacks.

No API method may return a channel that callers are responsible for closing. Channel close ownership stays with its creating package.
