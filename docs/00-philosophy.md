# Gotato Philosophy

**Status:** Draft

**Purpose:** Project constitution

> **Go-native Agent Runtime and Orchestration.**

> Gotato turns a self-contained Agent into an embeddable execution unit and, when needed, an addressable multi-Agent service.

Self-contained means that an Agent owns its private state and current work; it does not mean that it has no Model or Tool adapters.

## 1. Mission

Gotato provides one Agent semantics in multiple composition forms. Agent Core is the atomic runtime for one stateful, tool-using Agent. The same Core can be embedded directly, coordinated with other Cores in-process, or exposed remotely through Orchestration and a Host.

```text
Embedded, single:
  Go service → Agent handle → Agent Core

Embedded, multi:
  Go service → application / Gotato Orchestration → Agent Core × N

Agent as a Service:
  Client → Protocol Adapter → Host → Orchestration → Agent Core × N
```

Embedded Gotato and Agent as a Service are complementary, not competing products. Embedded is the smallest useful entry point and the semantic baseline. Agent as a Service is a first-class composition of the same runtime for callers that need remote access, addressability, admission, lifecycle management, and delivery. It must not introduce a second Agent implementation, transcript, Loop, or terminal meaning.

The caller provides a Model and optional Tools, then uses the same Core contract whether the Agent handle is held directly or reached through Orchestration. “As a Service” describes the addressable, coordinated boundary; it does not require every Agent call to cross a network, and Embedded-first does not mean Embedded-only.

The product may deliver these forms at different maturity levels. A process-local reference Host proves semantic equivalence and protocol composition; it is not, by itself, a claim of durable or distributed hosting. Cross-process restoration, multi-Pod continuity, resumable delivery, and durable Runs require separate explicit contracts.

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

## 6. Embedded and Agent as a Service

Gotato supports three compositions of one runtime rather than a migration from one product to another.

### Embedded, single Agent

```text
Existing Go Service → Agent handle → Agent Core
```

The service calls Core directly. No Host or Gotato Orchestration package is required when the application holds one Agent handle and owns its surrounding request policy.

### Embedded, multiple Agents

```text
Existing Go Service → application / Gotato Orchestration → Agent Core × N
```

The application or reusable Gotato Orchestration retains handles, maps Conversation keys, and owns routing, admission, retirement, lifecycle, and coordination. This is Orchestration even when implemented as ordinary application code.

### Agent as a Service

```text
Client → Protocol Adapter → Host → Orchestration → Agent Core × N
```

The Host adds remote access, Event delivery, cancellation mapping, readiness, and drain. Orchestration adds addressability and multi-Agent coordination. Neither changes Core execution semantics.

These forms may coexist in one product or process. An application may call some Agents directly while exposing others through a Host. The invariant is semantic equivalence after a command reaches Core:

```text
same initial Core state
+ same Model and Tool outcomes
+ same options and cancellation timing
→ same committed conversation, canonical Events, and terminal Core result
```

Protocol acknowledgements, queueing, dispatch timing, identity scope, and delivery settlement may differ because they belong outside Core. Hosted-specific operational guarantees—such as authentication, tenant isolation, durable routing, restart recovery, or multi-Pod continuity—must be stated and tested separately; they are not implied merely by attaching a protocol adapter.

## 7. Minimalism

Minimalism means one useful entry path, not pretending that one Agent is the whole service:

```text
Model + optional Tools → Agent Core → Prompt → Result / Events
```

A first single-Agent caller should not need to choose a Runner, build Orchestration, configure a Registry, or understand the Host. When the use case grows to multiple Agents, Orchestration becomes the explicit next layer rather than hidden global state or accidental application plumbing.

## 8. Review questions

1. Is this behavior required to execute one Agent, or is it Orchestration policy?
2. Can the caller use one Agent through a small Go interface, while multi-Agent access remains explicit?
3. Does this add a second Loop, transcript, state owner, or terminal meaning?
4. Is this provider, business-system, or protocol knowledge leaking into Core?
5. Do Embedded and Agent-as-a-Service paths preserve the same Core semantics?
6. Is a Hosted capability a semantic contract, a reference adapter, or an operational guarantee—and is its maturity stated accurately?
7. Does this feature reduce integration cost, or only add platform surface?

## 9. Declaration

> Gotato makes one stateful Agent as easy to call as a Go interface and makes multiple Agents explicitly addressable through Orchestration. Embedded use and Agent as a Service are complementary compositions of the same Core: Agent Core executes; Orchestration coordinates; Host exposes; existing Infrastructure remains the environment.
