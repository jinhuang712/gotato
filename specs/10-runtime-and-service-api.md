# 10. Core and Host API

**Status:** Draft

> The Core API is stable and in-process. The Host API coordinates many Core instances and may expose them through Transport.

## 1. Core API

The draft Core surface is:

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

The Core API contains no channels, transport envelopes, provider types, mutable internal slices, or Host objects. `Prompt` and `Continue` wait for Core execution settlement. `Subscribe` receives canonical Events locally.

## 2. Core construction

Construction validates Models, Tools, ToolSets, Extensions, Schemas, namespaces, limits, and ordering before the first Run. Options may include:

```text
WithModel
WithTool / WithTools
WithToolSet / WithToolSets
WithExtension
WithLimits
```

The Agent owns only dependencies documented as owned. Mutable transcript and queues are never shared between Agents without an explicit restoration contract.

## 3. Core subscription

Handlers run in registration order and are awaited. They are local, Context-aware, and bounded. Blocking or advisory failure mode is explicit, and panics are recovered. Unsubscribe is idempotent; after its synchronization barrier returns, no new handler invocation may begin.

## 4. Core queues

Steer and FollowUp validate and append atomically in acceptance order. They do not block on Model, Tool, Routine, network, or observer work. Queue overflow, terminal state, and invalid input are returned at acceptance.

## 5. Host API

The Host may expose boundaries equivalent to:

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

type AdmissionController interface {
    Admit(context.Context, AgentRequest) (AdmissionLease, error)
}

type AdmissionLease interface { Release() }
```

A Host may additionally define Agent Cache/Lease, Conversation Owner, Event Projector, Event Bridge, and Drain Policy contracts. These are not Core API.

## 6. Host responsibilities

The Host validates remote commands, admits capacity, resolves a Core Agent, maps cancellation, projects Events, delivers them with bounds, and manages readiness/drain. It does not edit Core state or reproduce the Agent loop.

## 7. Conversation and cache

A Host cache may retain live Core Agents with maximum entries, idle TTL, per-key creation coordination, active-Run pinning, idle-only eviction, reset, metrics, and fake-clock tests. Durable continuity requires a separate state provider.

## 8. Direct and hosted equivalence

For equivalent initial Agent state, Model stream, Tool outcomes, options, and cancellation timing, direct Core and Hosted execution produce identical canonical Events, transcript commitment, and terminal Core status. Differences are wire mapping, remote acceptance, Host admission, projection, and process lifecycle.

## 9. Package direction

A possible package layout is:

```text
core/              stable Agent execution kernel
model/             Model values, Router, provider adapters
tool/              Tool, ToolSet, Schema helpers
routines/          child Runs and Groups
orchestration/     Host, routing, admission, cache, delivery
transport/grpc/    Protobuf mapping and server/client
infra/             deployment and platform integration
```

Exact names may evolve. Host, Transport, Model, and capability adapters depend on Core contracts; Core does not depend on them.
