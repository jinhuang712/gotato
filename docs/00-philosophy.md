# Gotato Philosophy

**Status:** Draft

**Purpose:** Project constitution

> **Agent as a Service.**

> Gotato is a minimal, Go-native runtime for self-contained, stateful Agents.

Self-contained means that an Agent owns its private state and current work; it does not mean that it has no Model or Tool adapters.

## 1. Mission

Gotato gives an existing Go service a stateful, tool-using Agent through a small, ordinary Go interface. The caller provides a Model and optional Tools, calls the Agent, and receives a result or stream. Gotato owns the execution details instead of asking every service to assemble its own harness.

```text
Go service
    ↓ Agent interface
Agent Core
    ├── conversation state for the current Agent
    ├── canonical Model → Tool → Model Loop
    ├── bounded local work
    └── result / Event boundary
```

The same Core semantics can remain embedded or be exposed through a Hosted Agent Service. Hosted access adds a Host and Orchestration around Core; it does not replace Core or create a second Loop. “As a Service” describes the callable boundary, not a requirement for network deployment.

## 2. Agents are self-contained goroutines: each owns its state and work

Each Agent has one Go-native execution unit. The public handle hides its goroutine and provides the safe way to submit work, observe results, and cancel it.

```text
Agent handle
    ↓ command boundary
Agent goroutine
    ├── private state
    ├── current Run
    ├── Model and Tools
    └── result / Event boundary
```

The goroutine is an internal semantic guarantee, not a setup step for the caller. A caller can use ordinary Go methods while Core preserves serialized state transitions and bounded local work.

The Agent owns its private conversation state and the Run it has accepted. Its goroutine is the only authority that changes that state or advances that Run. A caller or Host owns the surrounding policy:

```text
admission · queueing · routing · priority · preemption · lifecycle
```

This division keeps the minimal Agent path simple while leaving service-level policy where it belongs. Conversation routing and long-term persistence can be added around Core; they are not prerequisites for the first Agent.

## 3. Infrastructure hosts. Orchestration coordinates. Agent Core executes.

These are the three system boundaries. Infrastructure is external; Core is the execution kernel; and Host / Orchestration is the optional Gotato service composition:

```text
Infrastructure
  hosts and connects processes

Agent Host / Orchestration
  creates, routes, coordinates, and serves Agents

Agent Core
  executes one Agent's work
```

Infrastructure is selected by the application and remains external. Orchestration is the coordination responsibility within the optional Host composition. Infrastructure hosts processes; Host exposes and operates Agents; Agent Core is the only component that executes the canonical Agent Loop.

A protocol adapter may connect a remote client to Orchestration. It maps wire messages to the fixed Host contract and does not become part of Core semantics.

## 4. Tight Core, Open Extensions

Core contains only the semantics that make an Agent an Agent:

```text
conversation state
canonical Model → Tool → Model Loop
Model and Tool contracts
cancellation and local limits
canonical Events and result settlement
```

Provider integrations, business capabilities, protocol adapters, and orchestration policies attach through explicit boundaries. They can evolve independently without enlarging the Core.

The initial product does not include a separate Memory platform. Core may keep the current conversation state required to continue an Agent; long-term memory, retrieval, compaction, and artifact storage are application or extension concerns.

## 5. LLM and Tool adapters

Core consumes provider-neutral contracts:

```text
Model provider → LLM Adapter → Core Model contract
Go service     → Tool Adapter → Core Tool contract
```

Adapters own external protocols, authentication, provider or service errors, and integration policy. Core owns when a Model is called, when a Tool is invoked, and how the result becomes part of the Agent conversation.

## 6. Two forms of the same Core semantics

### Embedded

```text
Existing Go Service → Agent interface → Agent Core
```

The service can call Core directly. No Host, protocol adapter, or new deployment platform is required.

### Hosted

```text
Client → protocol adapter → Agent Host / Orchestration → Agent Core
```

The Host adds access, routing, admission, Event delivery, cancellation mapping, readiness, and drain. The Agent remains the same Core runtime.

The Hosted form may use an existing HTTP or gRPC server and existing infrastructure. Gotato does not need to own the process platform around it.

## 7. Minimalism

Minimalism means one useful path, not an absence of capability:

```text
Model + optional Tools → Agent → Prompt → Result / Events
```

A first caller should not need to choose a Runner, build a SessionService, configure a Registry, or understand the Host. Advanced controls remain available through progressive disclosure, but the default path stays minimal.

## 8. Review questions

1. Is this behavior required to execute one Agent, or is it Host policy?
2. Can the caller use the Agent through a small Go interface?
3. Does this add a second Loop or another state owner?
4. Is this provider, business-system, or protocol knowledge leaking into Core?
5. Can Embedded and Hosted use the same Agent semantics?
6. Does this feature reduce integration cost, or only add platform surface?

## 9. Declaration

> Gotato makes a stateful Agent as easy to call as a Go interface. Agent Core keeps the execution path small and Go-native; Host and Orchestration can expose the same Core semantics as a Service; existing Infrastructure remains the environment, not another Gotato product.
