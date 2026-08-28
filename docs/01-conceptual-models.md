# Conceptual Models

**Status:** Draft

> Use the fewest concepts that explain a hosted Agent from network command to settled runtime result.

## 1. System picture

```text
Remote Client
     │ RunCommand
     ▼
Agent Service
  resolve · admit · own · stream
     │
     ▼
Agent Runtime
  state · Model/Tool loop · Events
     │
     ├──► Model
     ├──► Tools and ToolSets
     └──► Agent Routines
     │
     ▼
Canonical Events
     │ projection
     ▼
Remote Client
```

The service owns remote access and hosting. The runtime owns Agent execution. The transport maps between them.

## 2. Agent definition

An Agent definition describes how the service constructs an Agent:

```text
Agent definition
  system instructions
  Model
  Tools and ToolSets
  Extensions
  limits and policies
```

Definitions are registered under stable service-facing names. A factory turns a definition and request context into an Agent instance.

## 3. Agent

An Agent is a stateful runtime object:

```text
Agent = state + configuration + loop coordinator
```

Its state contains Messages, active ToolSets, queues, runtime options, and current execution state. One Agent coordinates at most one active mutating Run.

The service may create an ephemeral Agent for one request or retain a conversation-scoped Agent according to its state policy.

## 4. Conversation

A conversation is the service-level identity used to associate requests with stateful Agent continuity.

```text
Agent name + conversation key
              ↓
       Agent resolution
              ↓
     conversation Agent
```

Conversation identity belongs to the service layer. The runtime only owns the Agent instance and its transcript.

## 5. Run

A Run is one accepted `Prompt` or `Continue` execution, from `agent_start` through terminal `agent_end` settlement.

```text
Run = one execution containing one or more Turns
```

A Run has a canonical runtime identity. The service and transport may add request, stream, and external correlation identifiers without replacing the runtime Run ID.

## 6. Attached Run stream

An attached Run stream is the gRPC connection that controls and observes one active Run:

```text
client → Start · Steer · FollowUp · Cancel
server → ordered Run Events · terminal outcome
```

The stream owns remote command acceptance and network cancellation. The Run retains its own runtime lifecycle semantics.

## 7. Turn

A Turn is one Model response plus the Tool batch requested by that response.

```text
turn_start
  Model response
  zero or more Tool Uses
turn_end
```

Tool Results can lead to another Turn in the same Run.

## 8. Message

Messages form the Agent transcript. Core model-facing roles are:

```text
user
assistant
tool_result
```

Runtime Messages are Go domain values. Protobuf Messages are portable projections at the transport boundary. A deliberate conversion stage creates provider-specific Model input.

## 9. Model

A Model is a provider-neutral streaming reasoning endpoint. It receives system instructions, converted Messages, and visible Tool specifications, then streams an assistant response.

Provider SDKs implement this contract through adapters.

## 10. Tool Call and Tool Use

A Tool Call is a request produced by the Model. A Tool Use is the Runtime's resolved and validated execution attempt.

```text
Tool Call
   ↓ assemble, resolve, validate
Tool Use
   ↓ execute at most once
Tool Outcome
   ↓ commit
Tool Result Message
```

This distinction creates precise Pre-Tool-Use and Post-Tool-Use boundaries.

## 11. Tool

A Tool is one model-callable operation:

```text
Tool = specification + execution
```

Examples:

```text
read_file
grafana.view_dashboard
grafana.edit_panel
search_documents
```

## 12. ToolSet

A ToolSet is a named capability domain containing related Tools:

```text
grafana
  ├── view_dashboard
  ├── edit_panel
  ├── probe
  ├── refresh
  └── sync
```

ToolSets provide composition, model-facing capability discovery, and staged activation of concrete Tools.

## 13. Event

An Event is an immutable structured runtime fact:

```text
Agent and Turn lifecycle
Message start, update, and end
Tool execution and progress
ToolSet activation
Agent Routine lifecycle
terminal Run settlement
```

Canonical Event kind, production point, order, and correlation belong to the runtime. Enrichment, projection, redaction, coalescing, delivery, and sinks belong to focused consumers and adapters.

Events fall into two classes, and the distinction governs what a consumer may drop:

```text
Protected      lifecycle transitions and settled outcomes
               every consumer receives each one, in order

Coalescable    optional progress such as Message and Tool updates
               a consumer may merge or thin these under load
```

A Run produces exactly one terminal Event. Nothing restarts a Run after it.

## 14. Projection, delivery, and backpressure

An Event projection converts a canonical runtime Event into a consumer-specific representation. Projection changes representation, not runtime history.

```text
Canonical Event
      ↓ project / redact
Portable RunEvent
      ↓ bounded bridge
Remote client
```

Backpressure is what a system does when a producer outruns a consumer. A Run can emit thousands of Events while a remote client accepts a few, and only three answers exist: buffer without limit, discard, or slow the producer. An Event bridge is the bounded queue that makes this choice explicit instead of accidental.

Two distinct points are called settlement, and they have different owners:

```text
Execution settlement   the Run is over and owns no further work
                       owned by the Runtime

Delivery settlement    the consumer has received everything it will receive
                       owned by the Service
```

A client that disconnects mid-stream changes delivery settlement and nothing else. Execution settlement already happened, or happens on its own schedule.

## 15. Extension

An Extension observes or changes execution at a named runtime boundary. Examples include context transformation, Pre-Tool-Use, Post-Tool-Use, Event observation, and turn termination.

Extensions are explicitly installed Go components.

## 16. Adapter

An Adapter connects a runtime or service contract to an external technology:

```text
Model provider adapter
HTTP or gRPC Tool adapter
MCP ToolSet adapter
gRPC Agent transport adapter
state adapter
observability adapter
```

Adapters translate protocols without redefining Gotato semantics.

## 17. Agent Routine

An Agent Routine is a managed child Agent Run:

```text
Agent Routine
  = child Agent
  + child Run
  + parent Context
  + bounded execution
  + settled Routine Result
```

A Routine may be spawned by runtime composition or by a parent Model through an ordinary `spawn_agent` Tool. Each child owns an isolated transcript and preserves parent/child Event correlation.

## 18. Agent factory

An Agent factory creates or restores isolated Agent instances for a service request:

```text
Agent request
      ↓
Agent factory
      ├── Model
      ├── ToolSets
      ├── Extensions
      └── limits
      ↓
Agent
```

The factory is the bridge between application-owned definitions and service-owned lifecycle.

## 19. Agent cache

An Agent cache retains conversation-scoped runtime objects under explicit bounds. It coordinates creation, pins active Agents, evicts only eligible idle entries, and never becomes durable truth by implication.

## 20. Admission and drain

Admission decides whether the service accepts new Runs under current resource and lifecycle conditions. Drain stops new admission and settles active Runs according to an explicit shutdown policy.

These are service concepts, not runtime loop concepts.

## 21. Moving Part

A Moving Part is a stable replacement or composition boundary:

```text
Runtime
  Model · ContextTransformer · MessageConverter
  Tool · ToolSet · PreToolUse · PostToolUse
  TurnStopper · Event observer

Service
  AgentFactory · AgentCache · AdmissionController
  EventProjector · EventBridge · ErrorMapper · DrainPolicy
```

Fixed rails retain state transitions, validation, ordering, commitment, cancellation, and terminal settlement.

## 22. Vocabulary map

| Pair | Distinction |
|---|---|
| Agent definition / Agent | Definition constructs; Agent owns runtime state |
| Conversation / Agent | Conversation is service identity; Agent is runtime state |
| Agent / Run | Agent owns state; Run is one active invocation |
| Run / stream | Run is runtime lifecycle; stream is remote attachment |
| Run / Turn | A Run contains one or more Model Turns |
| Runtime Event / RunEvent | Runtime fact versus transport projection |
| Protected Event / Coalescable Event | Lifecycle and outcome facts versus optional progress |
| Execution settlement / Delivery settlement | The Run is over versus the consumer has everything |
| Tool Call / Tool Use | Model requests; Runtime executes |
| Tool / ToolSet | Tool performs one operation; ToolSet groups and discovers Tools |
| ToolSet / Extension | ToolSet adds capability; Extension changes behavior |
| Agent Routine / goroutine | Routine is managed Agent work; goroutine schedules it locally |
| Runtime / Service | Runtime executes Agents; Service hosts and exposes them |
| Go Context / Model context | Go Context cancels work; Model context carries reasoning material |
