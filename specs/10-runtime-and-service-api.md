# 10. Core, Orchestration, and Host API

**Status:** Draft

> The Core API executes one Agent. The Orchestration API makes multiple Agents addressable and coordinated. Host exposes that coordination through a service boundary.

## 1. Core API

The minimum public surface is a handle to one Agent:

```go
type Agent interface {
    Prompt(context.Context, Message) (RunResult, error)
    Close(context.Context) error
}

type AgentStatus string

const (
    AgentCreated AgentStatus = "created"
    AgentIdle    AgentStatus = "idle"
    AgentBusy    AgentStatus = "busy"
    AgentClosing AgentStatus = "closing"
    AgentClosed  AgentStatus = "closed"
)

type AgentLifecycle interface {
    Agent
    Status() AgentStatus
    Done() <-chan struct{}
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

The public Core API MUST NOT require a Runner, Orchestration, Host, SessionService, Registry, Broker, or protocol server for a direct single-Agent call. This is the atomic path; a multi-Agent caller requires an Orchestration owner outside this Core interface. `Close` is the one lifecycle operation required to release a Core execution unit; it does not require a Host or persistence service.

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

Exact names may evolve, but the minimal Agent path must remain small. Advanced capabilities use the same Core Loop rather than a second execution API.

## 2. Core command behavior

`Prompt` submits one execution command to an Agent. The Agent accepts one current execution. A direct call while Busy returns a typed busy/not-available result unless the selected Core policy defines another bounded behavior. A call after `Closing` or `Closed` returns a typed closed/not-available result and creates no Run. Core does not enqueue an unbounded external request queue.

`Continue`, `Steer`, `FollowUp`, and `Abort` are additive control operations. They do not turn Core into a user-facing scheduler.

The application Orchestration or Host owns any policy for:

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

The minimal Agent path requires only a Model and may have no Tools. Typed function helpers SHOULD make a normal Go function easy to expose as a Tool.

Each Agent receives private conversation state. Core may keep the current transcript in memory for multi-turn behavior. Long-term Memory, retrieval, compaction, artifacts, and cross-session persistence are not Core requirements.

## 4. Core observation

A Core MAY expose local Events through an in-process, Context-aware subscription boundary. Subscribers are local and bounded. Remote delivery is a Host and protocol-adapter concern.

Unsubscribe is idempotent. After its synchronization barrier returns, no new handler invocation may begin.

## 5. Agent availability

```text
Idle ── accepted Prompt/Continue ──► Busy
Idle ◄──── terminal result/Event ─── Busy
```

Availability describes one Agent's execution state. It does not define external queueing, routing, or process placement.

## 6. Orchestration API

Orchestration coordinates multiple Agents through semantic interfaces. It may be application code in Embedded use or a reusable Gotato component in Hosted use:

```go
type AgentFactory interface {
    NewAgent(context.Context, AgentRequest) (Agent, error)
}

type AgentResolver interface {
    Resolve(context.Context, AgentRequest) (Agent, error)
}

type ConversationStatus string

const (
    ConversationActive   ConversationStatus = "active"
    ConversationDormant  ConversationStatus = "dormant"
    ConversationRetiring ConversationStatus = "retiring"
    ConversationClosed   ConversationStatus = "closed"
    ConversationArchived ConversationStatus = "archived"
)

type ConversationRecord struct {
    ID           ConversationID
    Key          ConversationKey
    AgentName    AgentName
    LiveAgentID  AgentID
    Generation   AgentGeneration
    Status       ConversationStatus
    StateVersion uint64
}

type RetirementPolicy string

const (
    Retain     RetirementPolicy = "retain"
    AfterRun   RetirementPolicy = "after_run"
    AfterIdle  RetirementPolicy = "after_idle"
    Ephemeral  RetirementPolicy = "ephemeral"
)

type AgentRequest struct {
    AgentName       AgentName
    ConversationID  ConversationID // resolve an existing Conversation when set
    ConversationKey ConversationKey // create/resolve within the caller namespace
    RequestID       string
    Metadata        map[string]string
    Retirement      RetirementPolicy
}

type AdmissionController interface {
    Admit(context.Context, AgentRequest) (AdmissionLease, error)
}

type AdmissionLease interface {
    Release()
}

type ConversationOrchestration interface {
    Resolve(context.Context, AgentRequest) (Agent, error)
    Retire(context.Context, ConversationID, RetirementPolicy) error
    CloseConversation(context.Context, ConversationID) error
}
```

Orchestration MUST provide the relevant Conversation identity, handle-retention, admission, queue, retirement, and lifecycle behavior for a managed multi-Agent system. `Retire` closes the live Agent according to policy; `CloseConversation` closes the business Conversation and may discard its retained state. `AgentFactory` creates a Core Agent; `AgentResolver` returns the retained handle for an application request or reports that it is unavailable. A resolver MUST reject an ambiguous request that supplies conflicting ConversationID and ConversationKey values. It MAY rehydrate a dormant Conversation through the factory, in which case the ConversationID remains stable and the new AgentID is different. An AgentID is not a replacement for this resolution step. A Host MAY additionally define protocol attachment, Event projectors, delivery bridges, readiness, and drain policies. These components coordinate Core handles; they do not own or mutate Agent state.

## 7. Protocol adapters

A protocol adapter implements the Host boundary for a wire protocol:

```text
HTTP / gRPC / SSE / existing RPC
          ↓
Host semantic interface
```

The adapter maps commands and Events, owns connection lifetime, and reports protocol errors. It is optional for Embedded use. It MUST NOT introduce wire types into Core.

## 8. Orchestration and Host responsibilities

Orchestration:

```text
receives or accepts application commands
retains or resolves Agent handles
adopts admission and queue policy
creates or locates an Agent
waits for Idle or chooses a control action
sends commands through Agent interfaces
coordinates multiple Agent results and Events
maps cancellation and manages Agent lifecycle
```

Host adds:

```text
remote command and Event access
remote Agent retirement / close operations
protocol connection lifetime
readiness and drain
```

Neither layer edits Core state or reproduces the Agent Loop.

## 9. Conversation routing

Application Orchestration or a Host MAY map an application ConversationKey to an Agent handle. A retained Conversation is an addressable record, not merely the current handle:

```text
Agent name + ConversationKey
              ↓
Conversation record / routing table
              ↓
Agent handle, or Agent definition + Core state
```

This is a routing decision, not Agent ownership. A single directly held Agent needs no routing layer; multiple Agents that must be revisited or coordinated do need application Orchestration or Host routing. When a live handle is retired with retention enabled, the record becomes Dormant and a later resolve may create a new AgentID. Multi-process routing and durable restoration require the lifecycle and persistence contract in [spec 16](16-agent-lifecycle-and-retirement.md); an AgentID alone cannot restore a lost in-memory handle.

## 10. Cache, queue, and retirement

Orchestration MAY retain Agent handles in a bounded process-local cache. It MAY maintain request queues separate from Agent state. Cache eviction must not interrupt a Busy Agent unless the lifecycle policy explicitly sends cancellation. A retirement workflow must stop new admission before closing a handle and must persist retained Conversation state before removing the live route.

Queueing does not create a Core Run until Orchestration dispatches the request. Retiring an Agent does not necessarily close its Conversation.

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
orchestration/     multi-Agent identity, routing, and lifecycle
host/              service-facing composition around Orchestration
adapter/protocol/  optional protocol adapters used by Host
```

Exact names may evolve. The dependency direction must not:

```text
LLM / Tool adapters → Core contracts
Orchestration / Host / protocol adapters → Orchestration / Core contracts
Infrastructure hosts everything from outside
```
