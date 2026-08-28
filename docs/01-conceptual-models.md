# Conceptual Models

**Status:** Draft

> Separate the Agent that executes from the Host that coordinates and the platform that deploys it.

## 1. Layered system

```text
Application
   │
   ├── embedded call ───────────────┐
   │                                ▼
   └── hosted client → Transport → Host
                                     │
                                     ▼
                                 Agent Core
                                     │
                    ┌────────────────┴───────────────┐
                    ▼                                ▼
             Model Layer                      Capability Adapters
```

Infrastructure may sit around the hosted path:

```text
Client → Gateway / LB → Pod → Transport → Host → Core
```

Gateway, LB, and Pod placement are not Agent concepts.

## 2. Agent Core

Agent Core is the in-process execution boundary:

```text
Core = Agent state + canonical loop + Events + cancellation + limits
```

It accepts operations such as `Prompt`, `Continue`, `Steer`, `FollowUp`, and `Abort`; it returns a settled `RunResult` and exposes canonical Events while the Run is active.

A Core instance can live in a goroutine-oriented actor implementation, but its contract is one serialized state owner. Tool and Routine workers may be bounded child goroutines.

## 3. Agent

An Agent is a stateful Core object containing:

```text
system instructions
Model contract
Messages
individual Tools and registered ToolSets
active ToolSets
Extensions
Steering and Follow-up queues
runtime limits
active Run ownership
```

One Agent has at most one active mutating Run. The application or Host decides how instances are created and retained.

## 4. Run and Turn

A Run is one accepted `Prompt` or `Continue` execution. It may contain multiple Turns and ends with one terminal result and one `agent_end` Event.

A Turn is one Model request and the Tool batch produced by that response:

```text
turn_start → Model response → Tool batch → Tool Results → turn_end
```

## 5. Host and Orchestrator

An Orchestrator is a process-local coordinator for multiple Core instances. It resolves Agent definitions, owns hosted admission, maps conversations to Agents, attaches transport streams, and projects Events.

It does not own a second transcript or Agent loop.

## 6. Transport

Transport converts an external protocol into Host operations and projects canonical Events outward:

```text
RunCommand → Host command → Core operation
Core Event → Host projection → RunEvent
```

Protobuf and gRPC types stop at this boundary.

## 7. Infrastructure

Infrastructure routes and hosts processes:

```text
Gateway · Kubernetes Service · load balancer · Pod · storage
```

It may select a Pod, but it cannot resolve a process-local conversation Agent unless an explicit sticky-routing, ownership, or durable-state contract exists.

## 8. Conversation

A Conversation is a Host-level identity used to select a stateful Agent. The Core knows only the Agent instance and its transcript.

```text
agent name + conversation key
              ↓
Host ownership / routing
              ↓
Core Agent instance
```

Process-local cache coordination is not distributed ownership. Multi-Pod continuity requires keyed routing, a distributed lease, or durable state restoration.

## 9. Model Layer

The Core consumes a provider-neutral Model interface. A Model Router and provider adapters can be shared by embedded and hosted applications:

```text
Model contract → Router → provider adapter → external Model
```

The Model layer owns provider selection and provider-specific policy. The Core owns request construction, stream assembly, and transcript commitment.

## 10. Tools and capabilities

A Tool performs one operation. A ToolSet groups a capability domain and may be activated in stages. Applications can expose database, Redis, HTTP, MCP, workflow, or remote Agent operations through these contracts.

## 11. Two modes

### Embedded

```text
Go Service → Agent Core
```

The surrounding application owns request routing, business state, and concurrency policy. Core Events remain local.

### Hosted

```text
Client → Transport → Host → Agent Core
```

The Host adds remote command ordering, admission, conversation ownership, Event delivery, and lifecycle. Infrastructure remains optional and external.

## 12. Settlement

Execution settlement means the Core owns no further work. Delivery settlement means a Host has drained or abandoned its remote delivery. They are independent.

## 13. Vocabulary map

| Term | Owner |
|---|---|
| Agent | Core stateful execution object |
| Run | one Core invocation |
| Turn | one Model response and Tool batch |
| Agent Core | in-process execution kernel |
| Orchestrator / Host | multi-Agent hosted coordination |
| Transport | wire mapping |
| Infrastructure | process deployment and network routing |
| Conversation | Host-level state identity |
| Model Router | provider selection and provider policy |
| Event | immutable Core fact |
| RunEvent | transport projection |
| Agent Routine | managed child Run |
