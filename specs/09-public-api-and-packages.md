# 09. Public API and Packages

**Status:** Draft

> A short embedded program, a short Routine program, and a short service program are primary architecture tests.

## 1. Embedded usage

```go
agent, err := gotato.New(
    gotato.WithModel(model),
    gotato.WithToolSets(files, search),
)
if err != nil {
    return err
}

unsubscribe := agent.Subscribe(func(event gotato.Event) error {
    return consume(event)
})
defer unsubscribe()

result, err := agent.Prompt(ctx, gotato.Text("Inspect the repository"))
```

## 2. Agent operations

The high-level Agent API MUST provide semantic equivalents of:

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

Exact Go names MAY evolve while preserving these behaviors.

## 3. Model stream

The Model adapter contract SHOULD use an explicit `Recv`-style stream. The Agent API consumes this stream internally and exposes canonical Events through subscribers.

## 4. Tool and Extension composition

```text
WithTool
WithTools
WithToolSet
WithToolSets
WithExtension
WithLimits
```

Construction MUST validate the complete static Agent configuration before the first Run.

## 5. Agent Routine usage

```go
routine, err := routines.Spawn(ctx, childFactory, request)
if err != nil {
    return err
}

result, err := routine.Wait(ctx)
```

A Routine Group SHOULD provide bounded spawn and deterministic result aggregation.

## 6. Service usage

```go
service, err := agentservice.New(
    agentservice.WithAgent("incident", incidentFactory),
)
if err != nil {
    return err
}

gotatogrpc.Register(grpcServer, service)
```

## 7. Package layout

```text
gotato/                 public Agent API and common types
internal/runtime/       canonical loop
model/                  Model contracts and stream Events
tool/                   Tool, ToolSet, Schema and function helpers
extension/              lifecycle Moving Part interfaces
event/                  Event projection and sink helpers
routines/               child Agent spawn, groups, and Results
testkit/                deterministic fakes and assertions
service/                Agent factory, cache, and service preset
transport/grpc/         Protobuf mapping, server, and client
```

Model providers and capability adapters SHOULD live in separate packages or modules.

## 8. Dependency direction

```text
application
  ├──► routines
  ├──► service / transport / adapters
  └──► public Gotato API
              │
              ▼
        internal runtime
```

Core packages remain independent of official adapters and transports. The Routine package composes the public Agent API.

## 9. Metadata

Core types SHOULD use typed fields for behavior. Metadata is reserved for correlation and adapter round-tripping and MUST preserve kernel semantics.
