# Conceptual Models

**Status:** Draft

> This document explains the Agent, the Core, and the optional Host around them.

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

Agent Core is a small Go-native runtime:

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

## 3. The three system boundaries

```text
Existing Infrastructure
  hosts and connects the process
          │
          ▼
Agent Host / Orchestration
  admission · routing · coordination · lifecycle
          │ Agent contract
          ▼
Agent Core
  state · execution · Tools · Events
```

Infrastructure provides the external environment. Orchestration coordinates Agents and callers. Core executes one Agent's work. These boundaries may live in one process or across process boundaries.

A protocol adapter connects a remote client to the Host when needed. An LLM adapter and Tool adapter connect Core to external capabilities. Adapters are boundaries, not additional ownership layers.

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

The execution unit may be implemented by a goroutine. That detail is important to Core correctness but is not a setup requirement for the caller. External request queues and routing remain Host or application policy.

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

Core applies state transitions, assembles Model streams, invokes Tools, commits results, and settles the Run. Neither a Host nor an application scheduler reproduces this Loop.

## 8. Orchestration

Orchestration coordinates Agent access:

```text
incoming request
       ↓
Agent Host / Orchestration
  route · admit · queue · control · deliver
       ↓ Agent contract
Agent Core
```

Queueing, priority, preemption, Agent creation, and Conversation routing are policies around Core. They can be omitted in a direct embedded call or supplied by a Hosted composition.

## 9. Adapters

```text
Model provider → LLM Adapter → Model contract → Core
Go service     → Tool Adapter → Tool contract  → Core
Remote client  → Protocol Adapter → Host contract
```

An adapter translates an external representation and owns external integration details. Core remains provider-neutral, service-neutral, and protocol-neutral.

## 10. Embedded and Hosted

### Embedded

```text
Existing Go Service → Agent interface → Agent Core
```

The application can call one Agent directly. Its existing HTTP, gRPC, or RPC boundary remains its own concern.

### Hosted

```text
Client → Protocol Adapter → Host / Orchestration → Agent Core
```

The Host exposes the same Agent through a service boundary and adds routing, admission, lifecycle, and Event delivery. Hosting does not create another Agent implementation.

## 11. Boundary table

| Concern | Owner |
|---|---|
| Agent state and canonical Loop | Agent Core |
| Model provider protocol | LLM Adapter |
| Business-system capability | Tool Adapter |
| Prompt admission and queue policy | Application or Orchestration |
| Agent creation and routing | Orchestration |
| Remote command and Event mapping | Protocol Adapter attached to Host |
| Process hosting and network | Existing Infrastructure |

The point of the model is not to make every deployment use every box. It is to keep the simple path short while preserving a clean path to service hosting.
