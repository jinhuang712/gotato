# Boundaries and Moving Parts

**Status:** Draft

> This document maps the Agent Core, the Orchestration that coordinates multiple Agents, the Host boundary, the side adapters, and the existing platform around them.

## 1. One Core, three forms

```text
Embedded, single:
  Existing Go Service → Agent Core

Embedded, multi:
  Existing Go Service → application Orchestration → Agent Core × N

Hosted:
  Client → Protocol Adapter → Host → Orchestration → Agent Core × N
```

These are compositions of the same Agent Core, not separate Agent implementations. The single-Agent form is the direct primitive; multi-Agent and Hosted forms make Orchestration explicit.

## 2. Runtime responsibilities

```text
┌─────────────────────────────────────┐
│ Existing Infrastructure              │
│ process hosting · network · storage │
└──────────────────┬──────────────────┘
                   ▼
┌─────────────────────────────────────┐
│ Host / Protocol Adapter              │
│ remote access · wire mapping        │
│ delivery · readiness · drain        │
└──────────────────┬──────────────────┘
                   ▼
┌─────────────────────────────────────┐
│ Orchestration                        │
│ identity · routing · admission      │
│ lifecycle · coordination            │
└──────────────────┬──────────────────┘
                   ▼ Agent contract(s)
┌─────────────────────────────────────┐
│ Agent Core × N                       │
│ private state · canonical Loop      │
│ Tools · Events · cancellation       │
└─────────────────────────────────────┘
```

Infrastructure hosts and connects processes. Host exposes the service boundary. Orchestration coordinates multiple Agents and callers. Core executes one Agent's work.

A single embedded Agent may connect directly to Core. Once multiple Agents must be found or coordinated, Orchestration responsibilities must exist in application code or in the reusable Orchestration layer. These areas may be packages in one Go process or services across process boundaries; the ownership rules remain the same.

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

The public Core surface stays small. A caller can create one Agent, provide a Model and optional Tools, and call it without selecting a Runner, Orchestration package, or Host.

Internally, Core keeps one canonical Loop, private state mutation, Model stream assembly, Tool validation, transcript commitment, Event order, cancellation, and terminal settlement. Core is the atomic execution layer, not the owner of multi-Agent identity or routing.

## 4. Orchestration and Host moving parts

```text
Agent Factory
Conversation Record / Resolver
Agent Handle / Router
Admission Controller
Request Queue / Scheduler
Dispatch and Preemption Policy
Retirement / Close Policy
Event Projector / Delivery Bridge
Error Mapper
Drain Policy
```

These components create, resolve, coordinate, retire, and close Agent executions. They decide how external requests are handled when an Agent is Busy. They do not edit Agent state or reproduce the Model/Tool Loop.

For one directly held Agent they are unnecessary except for explicit Core close. Once multiple Agents must be found, revisited, scheduled, coordinated, or rehydrated, these responsibilities must exist in application code or in these Orchestration components. The reusable Gotato layer is optional for simple Embedded use and is the coordination runtime for Hosted use.

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

Single Agent:
```text
Application goroutine
  ├── Agent interface
  ├── LLM Adapter
  ├── Tool Adapters
  └── Agent Core
```

Multiple Agents:
```text
Application Orchestration
  ├── identity and handle retention
  ├── routing and lifecycle policy
  └── Agent Core × N
```

No Host or protocol adapter is required for Embedded use. The application may send one Prompt directly, or become the Orchestration owner when it manages multiple Agents.

## 9. Hosted composition

```text
Client
  ↓ protocol adapter
Host
  ↓
Orchestration
  ├── request policy
  ├── Agent identity and routing
  ├── lifecycle and coordination
  └── Event delivery
  ↓ Agent contract(s)
Agent Core × N
```

The Host adds remote access without becoming an Agent. Orchestration coordinates multiple Core Agents without mutating their state or reproducing their Loop. A gRPC call between an Orchestration service and separately deployed Agent services is one possible adapter; a direct Go call is simpler when they share a process.

## 10. Fixed semantics versus policy

Fixed Core semantics:

```text
private Agent state
one canonical Loop
one current Prompt/Continue per Agent
Tool validation and commitment
canonical Event sequence
one terminal result per Run
idempotent Agent close
Context and local limits
```

Caller/Application Orchestration/Host policy:

```text
external queue and queue size
reject / wait / priority / preemption
number of Agent instances
Conversation routing and retention
retirement / eviction policy
Event delivery bounds
process placement
```

A policy must not change the meaning of an Agent, its conversation state, its Event sequence, its terminal result, or the distinction between Run settlement and Agent closure. Retirement may close a live Agent while retaining its Conversation for later rehydration.
