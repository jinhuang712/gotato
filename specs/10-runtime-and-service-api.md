# 10. Core and Host API

**Status:** Draft

> The Core API is the simple Agent interface. The Host API is the optional service composition around it.

## 1. Core API

The minimum public surface is a handle to one Agent:

```go
type Agent interface {
    Prompt(context.Context, Message) (RunResult, error)
}
```

Construction SHOULD use a small constructor and progressive options:

```go
agent, err := gotato.NewAgent(
    gotato.WithModel(model),
    gotato.WithInstruction("You are a helpful assistant."),
    gotato.WithTools(tools...),
)
```

The public API MUST NOT require a Runner, Host, SessionService, Registry, Broker, or protocol server for a direct call.

Streaming and control MAY be exposed as additive capabilities:

```go
type StreamingAgent interface {
    Agent
    Stream(context.Context, Message) (EventStream, error)
}

type ControllableAgent interface {
    Agent
    Continue(context.Context) (RunResult, error)
    Steer(Message) error
    FollowUp(Message) error
    Abort()
}
```

Exact names may evolve, but the basic Agent path must remain small. Advanced capabilities use the same Core Loop rather than a second execution API.

## 2. Core command behavior

`Prompt` submits one execution command to an Agent. The Agent accepts one current execution. A direct call while Busy returns a typed busy/not-available result unless the selected Core policy defines another bounded behavior. Core does not enqueue an unbounded external request queue.

`Continue`, `Steer`, `FollowUp`, and `Abort` are additive control operations. They do not turn Core into a user-facing scheduler.

The caller or Host owns any policy for:

```text
reject while Busy
bounded queue
priority
safe-boundary Steer
immediate Abort
creating another Agent
```

## 3. Core construction

Construction validates the Model, Tools, optional ToolSets, Extensions, Schemas, namespaces, and local limits before the Agent accepts its first execution.

A basic Agent requires only a Model and may have no Tools. Typed function helpers SHOULD make a normal Go function easy to expose as a Tool.

Each Agent receives private conversation state. Core may keep the current transcript in memory for multi-turn behavior. Long-term Memory, retrieval, compaction, artifacts, and cross-session persistence are not Core requirements.

## 4. Core observation

A Core MAY expose local Events through an in-process, Context-aware subscription boundary. Subscribers are local and bounded. Remote delivery is a Host and protocol-adapter concern.

Unsubscribe is idempotent. After its synchronization barrier returns, no new handler invocation may begin.

## 5. Agent availability

```text
Free ── accepted Prompt/Continue ──► Busy
Free ◄──── terminal result/Event ─── Busy
```

Availability describes one Agent's execution state. It does not define external queueing, routing, or process placement.

## 6. Host API

The optional Host coordinates Agents through semantic interfaces:

```go
type AgentFactory interface {
    NewAgent(context.Context, AgentRequest) (Agent, error)
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

type AdmissionLease interface {
    Release()
}
```

A Host MAY additionally define routing tables, request queues, Event projectors, delivery bridges, and drain policies. These coordinate Core handles; they do not own Agent state.

## 7. Protocol adapters

A protocol adapter implements the Host boundary for a wire protocol:

```text
HTTP / gRPC / SSE / existing RPC
          ↓
Host semantic interface
```

The adapter maps commands and Events, owns connection lifetime, and reports protocol errors. It is optional for Embedded use. It MUST NOT introduce wire types into Core.

## 8. Host responsibilities

The Host:

```text
receives or accepts external commands
adopts admission and queue policy
creates or locates an Agent
waits for Free or chooses a control action
sends commands through the Agent interface
projects Events
maps cancellation
manages readiness and drain
```

It does not edit Core state or reproduce the Agent Loop.

## 9. Conversation routing

A Host MAY map an application ConversationKey to an Agent handle:

```text
Agent name + ConversationKey
              ↓
Host routing table
              ↓
Agent handle
```

This is a routing decision, not Agent ownership. Multi-process routing and durable restoration are future contracts.

## 10. Cache and queue

A Host MAY retain Agent handles in a bounded process-local cache. It MAY maintain request queues separate from Agent state. Cache eviction must not interrupt a Busy Agent unless the lifecycle policy explicitly sends cancellation.

Queueing does not create a Core Run until the Host dispatches the request.

## 11. Local and Hosted equivalence

For equivalent initial Agent state, Model stream, Tool outcomes, options, and cancellation timing, direct Core and Hosted execution produce identical canonical Events, transcript commitment, and terminal Core status.

Queue policy, dispatch timing, protocol acknowledgement, and delivery timing are Host concerns, not Core equivalence facts.

## 12. Package direction

A possible package layout is:

```text
agent/             public Agent interface and Core implementation
model/             provider-neutral Model values and contract
adapter/llm/       LLM provider integrations
adapter/tool/      application capability integrations
host/              optional Orchestration and lifecycle
adapter/protocol/   optional protocol adapters used by Host
```

Exact names may evolve. The dependency direction must not:

```text
LLM / Tool adapters → Core contracts
Host / protocol adapters → Host / Core contracts
Infrastructure hosts everything from outside
```
