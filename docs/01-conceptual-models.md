# Conceptual Models

**Status:** Draft

> This document explains the Agent Core, the Orchestration that coordinates multiple Agents, and the Host that exposes them.

## 1. Agent as a Go interface

The first concept is an Agent that can be called from an existing Go service:

```text
Agent interface
      ↓
Agent Core
  private state · canonical Loop · Tools · Events
```

The interface hides the execution machinery. A local Agent and a Hosted Agent proxy can expose the same semantic operations, so a caller does not need a second application model when the Agent moves behind a service boundary.

## 2. Agent Core

Agent Core is a minimal Go-native runtime:

```text
Agent Core
  ├── one Agent execution unit
  ├── private conversation state
  ├── Model → Tool → Model Loop
  ├── explicit capabilities
  ├── cancellation and local limits
  └── result / Event boundary
```

Core does not require a service framework, a database, a broker, or a deployment platform. A caller can construct it inside an existing process and call it directly.

## 3. The runtime boundaries

```text
Existing Infrastructure
  hosts and connects the process
          │
          ▼
Host / Protocol Adapter (optional)
  remote access · wire mapping · delivery
          │
          ▼
Orchestration (for managed multi-Agent use)
  identity · routing · admission · coordination
          │ Agent contract(s)
          ▼
Agent Core × N
  private state · execution · Tools · Events
```

Infrastructure provides the external environment. Orchestration coordinates multiple Agents and callers. Core executes one Agent's work. A single embedded Agent may bypass both layers when its caller retains the handle. These boundaries may live in one process or across process boundaries.

An LLM adapter and Tool adapter connect Core to external capabilities. A protocol adapter connects a remote client to Host when needed. Adapters are boundaries, not additional ownership layers.

## 4. Agent execution

An Agent has one current execution boundary:

```text
Agent handle
      ↓
one Agent execution unit
      ├── current Prompt or Continue
      ├── private conversation state
      ├── Model and Tools
      └── result and Events
```

The execution unit may be implemented by a goroutine. That detail is important to Core correctness but is not a setup requirement for the caller. A Run's terminal `agent_end` does not close the Agent; an explicit close releases the execution unit. External request queues, routing, retirement, and rehydration remain application Orchestration or Host policy. Core does not discover a handle from an AgentID.

## 5. Conversation state and Work

Core keeps the current conversation state required for a basic multi-turn Agent:

```text
system instructions
committed Messages
Tools and active capabilities
current Run state
local execution limits
```

This is not a separate Memory product. Long-term memory, retrieval, compaction, artifacts, and cross-session persistence are optional application or extension concerns.

An Agent owns its private state and its accepted current work. It does not own a user's Conversation registry, an external request queue, or shared application resources.

A Conversation is an Orchestration-owned addressable thread, not another Core state owner:

```text
ConversationID / ConversationKey
          ↓
live Agent handle, if present
          ↓
Agent Core private state
```

A retained Conversation may become `Dormant` when its live Agent is retired. Rehydration creates a new AgentID from the Agent definition and persisted Core state. A Conversation may be closed independently of a Run or Agent lifecycle.

## 6. Run and Turn

A Run is one accepted Prompt or Continue. A Turn is one Model request and the Tool work produced by that response:

```text
Run
  └── Turn → Model response → Tool batch → Turn → ...
```

These identities support result and Event correlation. They are runtime concepts; a basic caller only needs to submit a Prompt and receive a result.

## 7. The canonical Loop

Every Agent uses one Core Loop:

```text
Prompt / Continue
       ↓
Model → Tool → Model → ...
       ↓
Result / canonical Events
```

Core applies state transitions, assembles Model streams, invokes Tools, commits results, and settles the Run. Neither Orchestration nor Host reproduces this Loop.

## 8. Orchestration

Orchestration is the required coordination responsibility once multiple Agents must be revisited, routed, scheduled, or combined. It may be ordinary application code for a fixed set of handles, or a reusable Gotato layer for dynamic and Hosted use:

```text
incoming request
       ↓
Orchestration
  identity · route · admit · queue · control · coordinate
       ↓ Agent contract(s)
Agent Core × N
```

Queueing, priority, preemption, Agent creation, Conversation routing, result aggregation, and retirement are policies around Core. They can be omitted for one directly held Agent, but a multi-Agent application must own them at the application or Host boundary. Orchestration holds, routes, and retires handles; it does not mutate Agent state or reproduce the Loop. A retained Conversation may outlive the handle that currently serves it.

## 9. Adapters

```text
Model provider → LLM Adapter → Model contract → Core
Go service     → Tool Adapter → Tool contract  → Core
Remote client  → Protocol Adapter → Host contract
```

An adapter translates an external representation and owns external integration details. Core remains provider-neutral, service-neutral, and protocol-neutral.

## 10. Embedded and Hosted

### Embedded, single Agent

```text
Existing Go Service → Agent handle → Agent Core
```

The application can call one Agent directly and retain its handle. Its existing HTTP, gRPC, or RPC boundary remains its own concern.

### Embedded, multiple Agents

```text
Existing Go Service → application Orchestration → Agent Core × N
```

The application must retain handles or provide a key-to-handle mapping and must own the required coordination policy.

### Hosted

```text
Client → Protocol Adapter → Host → Orchestration → Agent Core × N
```

The Host exposes Orchestration through a service boundary and adds remote delivery, cancellation mapping, readiness, and drain. Hosting does not create another Agent implementation.

## 11. Boundary table

| Concern | Owner |
|---|---|
| Agent state and canonical Loop | Agent Core |
| Model provider protocol | LLM Adapter |
| Business-system capability | Tool Adapter |
| Prompt admission and queue policy | Application or Orchestration |
| Agent creation and routing | Application or Orchestration |
| Remote command and Event mapping | Protocol Adapter attached to Host |
| Process hosting and network | Existing Infrastructure |

The point of the model is not to make every deployment use every box. It is to keep the single-Agent path short while making the multi-Agent coordination boundary explicit and preserving a clean path to service hosting.
