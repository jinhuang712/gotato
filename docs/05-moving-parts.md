# Boundaries and Moving Parts

**Status:** Draft

> This document maps the small Core, the thin Host, the side adapters, and the existing platform around them.

## 1. One Agent, two forms

```text
Embedded:
  Existing Go Service → Agent Core

Hosted:
  Client → Protocol Adapter → Agent Host / Orchestration → Agent Core
```

These are compositions of the same Agent Core, not separate Agent implementations.

## 2. Three system boundaries

```text
┌─────────────────────────────────────┐
│ Existing Infrastructure              │
│ process hosting · network · storage │
└──────────────────┬──────────────────┘
                   ▼
┌─────────────────────────────────────┐
│ Agent Host / Orchestration           │
│ admission · routing · coordination  │
│ lifecycle · Event delivery          │
└──────────────────┬──────────────────┘
                   ▼ Agent contract
┌─────────────────────────────────────┐
│ Agent Core                           │
│ private state · canonical Loop      │
│ Tools · Events · cancellation       │
└─────────────────────────────────────┘
```

Infrastructure hosts and connects processes. Orchestration coordinates Agents and callers. Core executes one Agent's work.

The three areas may be packages in one Go process or services across process boundaries. The ownership rules remain the same.

## 3. Core moving parts

```text
Agent
Message
Run
Model contract
Tool
ToolSet
Extension
Event
Context and local limits
```

The public Core surface stays small. A caller can create an Agent, provide a Model and optional Tools, and call it without selecting a Runner or starting a Host.

Internally, Core keeps one canonical Loop, private state mutation, Model stream assembly, Tool validation, transcript commitment, Event order, cancellation, and terminal settlement.

## 4. Host / Orchestration moving parts

```text
Agent Factory
Agent Handle / Router
Admission Controller
Request Queue / Scheduler
Dispatch and Preemption Policy
Event Projector / Delivery Bridge
Error Mapper
Drain Policy
```

These components create and coordinate Agent executions. They decide how external requests are handled when an Agent is Busy. They do not edit Agent state or reproduce the Model/Tool Loop.

They are optional for Embedded use and become the service runtime for Hosted use.

## 5. Protocol adapters

```text
HTTP / SSE handler
gRPC server and client
Protobuf or JSON mapper
existing service endpoint
```

A protocol adapter maps wire commands and Events to the Host or Orchestration interface. It is not a semantic layer of Core. An existing service may provide the protocol itself; Gotato only needs an adapter when it owns that boundary.

## 6. LLM adapters

```text
Agent Core
     ▲ provider-neutral Model contract
     │
LLM Adapter
     │ provider protocol · auth · usage · provider errors
     ▼
External Model provider
```

Provider-specific retries may stay in an LLM Adapter. Run-level retry remains a Core decision because it must preserve one Run and one terminal result.

## 7. Tool adapters

Applications expose external systems through explicit Tool and ToolSet adapters:

```text
DB / Redis / HTTP / gRPC / MCP / workflow / sandbox
                         ▼
                    Tool contract
                         ▼
                    Agent Core
```

The adapter owns authentication, external timeout mapping, and private diagnostics. Core owns Tool identity, validation, cancellation, invocation boundaries, Events, and result commitment.

## 8. Embedded composition

```text
Application goroutine
  ├── Agent interface
  ├── LLM Adapter
  ├── Tool Adapters
  └── Agent Core
```

No Host or protocol adapter is required. The application can send one Prompt and wait, or add its own request policy around the Agent.

## 9. Hosted composition

```text
Client
  ↓ protocol adapter
Host / Orchestration
  ├── request policy
  ├── Agent routing
  ├── lifecycle
  └── Event delivery
  ↓ Agent contract
Agent Core
```

The Host adds remote access and coordination without becoming the Agent. A gRPC call between an Orchestration service and a separately deployed Agent service is one possible adapter; a direct Go call is simpler when both share a process.

## 10. Fixed semantics versus policy

Fixed Core semantics:

```text
private Agent state
one canonical Loop
one current Prompt/Continue per Agent
Tool validation and commitment
canonical Event sequence
one terminal result per Run
Context and local limits
```

Caller/Host policy:

```text
external queue and queue size
reject / wait / priority / preemption
number of Agent instances
Conversation routing
Event delivery bounds
process placement
```

A policy must not change the meaning of an Agent, its conversation state, its Event sequence, or its terminal result.
