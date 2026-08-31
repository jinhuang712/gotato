# 10. Core and Host API

**Status:** Draft

> **The Core API executes one Agent; the Host API coordinates many.**

## 1. Core API

The draft Core surface is a channel-backed handle to one Agent routine:

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

The public methods may block waiting on result channels. Agent Core does not expose raw channels as the only API, but its runtime model is a goroutine and channel protocol.

Agent Core contains no transport envelopes, provider types, mutable internal slices, or Host objects.

## 2. Core command behavior

`Prompt` and `Continue` submit one execution command to an Agent goroutine. The Agent accepts only one current execution. A direct call while Busy may return a typed busy/not-available result; Core does not enqueue a general external request.

`Steer`, `FollowUp`, and `Abort` are control messages for current or next Agent execution. They do not turn Core into a user-facing request scheduler.

The caller or Host owns any policy for:

```text
reject while Busy
FIFO queue
priority queue
safe-boundary Steer
immediate Abort
creating another Agent routine
```

## 3. Core construction

Construction validates Models, Tools, ToolSets, Extensions, Schemas, namespaces, limits, and ordering before the Agent goroutine accepts its first execution. Options may include:

```text
WithModel
WithTool / WithTools
WithToolSet / WithToolSets
WithExtension
WithLimits
```

Each Agent receives an explicit private capability set. Mutable transcript state is never shared between Agents without an explicit application protocol.

## 4. Core subscription

Handlers receive Events from the Agent Event boundary in registration order. They are local, Context-aware, and bounded. Blocking or advisory failure mode is explicit, and panics are recovered. Unsubscribe is idempotent; after its synchronization barrier returns, no new handler invocation may begin.

Remote transport delivery is a Host channel/stream concern, not a Core subscriber.

## 5. Agent availability

The Agent routine reports execution availability through its handle or result protocol:

```text
Free ── accepted Prompt/Continue ──► Busy
Free ◄──── terminal result/Event ─── Busy
```

This status does not define external queueing. A caller can wait, queue elsewhere, reject, steer, or abort according to its own policy.

## 6. Host API

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

A Host may additionally define Agent registries, routing tables, request queues, Event Projectors, Event Bridges, and Drain Policies. These coordinate channel endpoints; they do not own Agent state.

## 7. Host responsibilities

The Host:

```text
receives remote commands
adopts an admission and queue policy
creates or locates an Agent routine
waits for Free or chooses a control action
sends commands through channels
projects Events
maps cancellation
manages readiness and drain
```

It does not edit Core state or reproduce the Agent Loop.

## 8. Conversation routing

For the single-Pod PoC, a process-local registry may map a conversation key to an Agent handle:

```text
agent name + conversation key
              ↓
process-local routing table
              ↓
Agent channel endpoint
```

This is a routing decision, not an Agent ownership hierarchy. Multi-Pod routing remains a future contract.

## 9. Cache and queue

A Host may retain Agent handles in a bounded process-local cache. The Host may also maintain request queues separate from Agent state. Cache eviction must not interrupt a Busy Agent unless the configured lifecycle policy explicitly sends cancellation.

Queue entries contain request identity, command payload, response channel/stream correlation, and cancellation policy. Queueing does not create a new Agent Run until the Host dispatches the request.

## 10. Direct and hosted equivalence

For equivalent initial Agent state, Model stream, Tool outcomes, options, and cancellation timing, direct Core and Hosted execution produce identical canonical Events, transcript commitment, and terminal Core status. Queue policy, dispatch timing, transport acknowledgement, and delivery timing belong to the caller or Host and are not Core equivalence facts.

## 11. Package direction

A possible package layout is:

```text
core/              Agent goroutine, Loop, state, Events
model/             Model values, Router, provider adapters
tool/              Tool, ToolSet, Schema helpers
routine/           Agent routine handles and spawn protocol
orchestration/     scheduling, admission, routing, queues, delivery
transport/grpc/    Protobuf mapping and server/client
infra/             deployment and platform integration
```

Exact names may evolve. Orchestration, Transport, Model, and capability adapters depend on Core contracts; Core does not depend on them.
