# Gotato Philosophy

**Status:** Draft

**Purpose:** Project constitution

> **Go-native Agent Runtime and Orchestration.**

> Gotato turns a self-contained Agent into an embeddable execution unit and, when needed, an addressable multi-Agent service.

Self-contained means that an Agent owns its private state and current work; it does not mean that it has no Model or Tool adapters.

## 1. Mission

Gotato provides one Agent semantics at two scales. Agent Core is the atomic runtime for one stateful, tool-using Agent. Orchestration is the layer that makes multiple Core Agents addressable, routable, schedulable, and coordinatable. A Host exposes that Orchestration through a protocol and an existing process platform.

```text
Single Agent:
  Go service → Agent handle → Agent Core

Multiple Agents:
  Application / Orchestration → Agent Core × N

Hosted Service:
  Client → Protocol Adapter → Host → Orchestration → Agent Core × N
```

The caller provides a Model and optional Tools, then uses the same Core contract whether the handle is direct or reached through Orchestration. The direct call is the smallest entry point; it is not a complete multi-Agent service model. “As a Service” describes the addressable, coordinated boundary, not a requirement that every single-Agent call use a network.

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

The Agent owns its private conversation state and the Run it has accepted. Its goroutine is the only authority that changes that state or advances that Run. A Run's terminal `agent_end` does not close the Agent; explicit close releases the execution unit. Application Orchestration or Host owns the surrounding policy:

```text
admission · queueing · routing · priority · preemption · lifecycle
```

This division keeps the minimal Agent path simple while leaving service-level policy where it belongs. Conversation routing, retirement, and long-term persistence can be added around Core; they are not prerequisites for the first Agent. A retained Conversation may outlive a retired Agent and later rehydrate it with a new AgentID.

A single Agent needs only its handle. Once an application has multiple Agents that it must revisit or coordinate, an external coordination owner is unavoidable: fixed application code may hold the handles, while dynamic or remote use needs routing, admission, scheduling, and lifecycle policy in application Orchestration or Host. Core has no global Agent lookup, and an AgentID alone cannot recover a lost in-memory handle.

## 3. Infrastructure hosts. Orchestration coordinates. Host exposes. Agent Core executes.

These are distinct responsibilities:

```text
Infrastructure
  hosts and connects processes

Host / Protocol Adapter
  exposes remote access and delivery

Orchestration
  creates, addresses, routes, schedules, and coordinates Agents

Agent Core × N
  executes each Agent's private work and canonical Loop
```

Infrastructure remains external. Orchestration is optional for one directly held Agent, but its responsibilities are unavoidable once multiple Agents must be found or coordinated. Host is the service-facing composition around Orchestration; a protocol adapter maps wire messages to it. Neither Host nor Orchestration may mutate Core state or reproduce the canonical Agent Loop.

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

## 6. From Embedded Core to Orchestrated Service

### Embedded, single Agent

```text
Existing Go Service → Agent handle → Agent Core
```

The service can call Core directly. No Host or Gotato Orchestration package is required if the service holds the handle and only needs one Agent.

### Embedded, multiple Agents

```text
Existing Go Service → application Orchestration → Agent Core × N
```

The application must retain handles or map Conversation keys to them, then own whatever routing, admission, retirement, lifecycle, and coordination the use case needs. This is Orchestration even when it is implemented as ordinary application code. A retained Conversation can resolve to a live handle or to persisted state for rehydration.

### Hosted

```text
Client → protocol adapter → Host → Orchestration → Agent Core × N
```

The Host adds remote access, Event delivery, cancellation mapping, readiness, and drain. Orchestration adds addressability and multi-Agent coordination. The Agent Core remains the same runtime.

## 7. Minimalism

Minimalism means one useful entry path, not pretending that one Agent is the whole service:

```text
Model + optional Tools → Agent Core → Prompt → Result / Events
```

A first single-Agent caller should not need to choose a Runner, build Orchestration, configure a Registry, or understand the Host. When the use case grows to multiple Agents, Orchestration becomes the explicit next layer rather than hidden global state or accidental application plumbing.

## 8. Review questions

1. Is this behavior required to execute one Agent, or is it Orchestration policy?
2. Can the caller use one Agent through a small Go interface, while multi-Agent access remains explicit?
3. Does this add a second Loop or another state owner?
4. Is this provider, business-system, or protocol knowledge leaking into Core?
5. Can Embedded and Hosted use the same Agent semantics?
6. Does this feature reduce integration cost, or only add platform surface?

## 9. Declaration

> Gotato makes one stateful Agent as easy to call as a Go interface, then makes the transition to an addressable multi-Agent service explicit. Agent Core executes; Orchestration coordinates; Host exposes; existing Infrastructure remains the environment.
