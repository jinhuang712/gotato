# 10. Runtime and Service API

**Status:** Draft

> One runtime boundary serves two consumers: the hosted service first, a direct Go caller second. Both compile against the same contracts or the contracts are wrong.

```text
direct Go caller ──────┐
                       ├──► runtime API ──► canonical loop
service / transport ───┘
```

The runtime API MAY begin as an internal package boundary. Public status is earned by [delivery slice 7](12-delivery-roadmap.md): promotion adds compatibility commitment without changing what the contracts are.

## 1. Runtime API

The runtime supports semantic equivalents of:

```text
Prompt(context.Context, Message) (RunResult, error)
Continue(context.Context) (RunResult, error)
Steer(Message) error
FollowUp(Message) error
Abort()
WaitForIdle(context.Context) error
Subscribe(EventHandler) Unsubscribe
StateSnapshot() AgentSnapshot
Reset() error
```

`Prompt` and `Continue` return a terminal `RunResult`. Events arrive through `Subscribe` during execution. `WaitForIdle` observes execution settlement only.

Construction validates the complete static configuration before the first Run:

```text
WithModel
WithTool
WithTools
WithToolSet
WithToolSets
WithExtension
WithLimits
```

An `EventHandler` MUST be treated as an observer under the [event contract](04-events-and-delivery.md): in-process, fast, Context-aware, never blocking on a network peer.

Exact Go names MAY evolve while preserving these behaviors.

## 2. Service registration

```go
service, err := agentservice.New(
    agentservice.WithAgent("incident", incidentFactory),
    agentservice.WithAgent("repository", repositoryFactory),
)
if err != nil {
    return err
}

gotatogrpc.Register(grpcServer, service)
```

Registration MUST be deterministic and validated eagerly: unknown options, duplicate Agent names, and nil factories fail at construction, not on first request.

The preset assembles Agent resolution, admission, Event delivery, error mapping, readiness, and drain with focused replacement interfaces for each.

## 3. Agent Routine usage

```go
routine, err := routines.Spawn(ctx, childFactory, request)
if err != nil {
    return err
}

result, err := routine.Wait(ctx)
```

A Routine Group SHOULD provide bounded spawn and deterministic, spawn-ordered result aggregation. The Routine package composes the runtime API; it MUST NOT introduce a second execution loop.

## 4. Package layout

```text
cmd/gotatod           executable Agent service
service/              Agent factories, cache, admission, delivery, lifecycle
transport/grpc/       Protobuf mapping, server adapter, Go client
routines/             child Agent spawn, groups, and Results
extension/            lifecycle Moving Part interfaces
tool/                 Tool, ToolSet, Schema, and function helpers
event/                Event projection and sink helpers
testkit/              deterministic fakes and assertions
internal/runtime/     canonical loop and Agent state
model/                Model contracts and stream Events
```

Model providers and capability adapters live in separate packages or modules.

## 5. Dependency direction

```text
application
  ├──► service / transport / adapters
  ├──► routines
  └──► runtime API
             │
             ▼
      internal runtime
```

Core packages depend on the Go standard library and deliberately chosen small foundations. `internal/runtime` MUST NOT import generated Protobuf, gRPC, cache, or hosting packages.

## 6. Metadata

Core types SHOULD use typed fields for behavior. Metadata is reserved for correlation and adapter round-tripping and MUST preserve kernel semantics.

## 7. Acceptance

Three short programs are primary architecture tests:

- a service program registers a factory and serves a Run over in-process gRPC;
- a direct program drives the same loop in-process with the same fake Model;
- a Routine program spawns bounded children and collects spawn-ordered Results.

All three MUST compile against the same contracts, and the first two MUST produce identical canonical Events for equivalent input.
