# Gotato Documentation

> **Go-native Agent Runtime and Orchestration.**

> Gotato turns a self-contained Agent into an embeddable execution unit and, when needed, an addressable multi-Agent service.

Gotato has two deliberately separated layers. Agent Core is the atomic execution unit for one stateful Agent. Orchestration is the coordination layer that retains, addresses, routes, and combines multiple Core Agents. A Hosted Agent Service exposes that Orchestration through a protocol and an existing process platform.

```text
Embedded, single:
  Application ── Agent handle ──► Agent Core

Embedded, multi:
  Application Orchestration ──► Agent Core × N

Hosted:
  Client ── protocol adapter ──► Host ──► Orchestration ──► Agent Core × N
```

The same Agent contract and Core Loop apply at every scale. A direct handle is the smallest entry point, not a substitute for multi-Agent coordination. Once Agents must be revisited, routed, scheduled, or combined, Orchestration is required somewhere: application code may provide it for a fixed set of handles, while Gotato Orchestration provides the reusable coordination form and Host exposes it as a service.

## The architecture in one picture

```text
                         Existing Infrastructure
                    hosts and connects the process
                                   │
                                   ▼
                         Host / Protocol Adapter
                    remote access · delivery · mapping
                                   │
                                   ▼
                            Orchestration
             identity · routing · admission · retirement · coordination
                                   │ Agent contract(s)
                                   ▼
                            Agent Core × N
              private state · canonical Loop · Tools · Events
                         │                         │
                         ▼                         ▼
                   LLM adapters                Tool adapters
                         │                         │
                   Model providers             Go services / systems
```

A protocol adapter may connect a remote client to the Host. It is a replaceable boundary adapter, not a separate Agent layer. Infrastructure is outside the Gotato implementation.

## Reading order

1. [Philosophy](00-philosophy.md) — the product thesis and three principles
2. [Glossary](glossary.md) — shared vocabulary
3. [Conceptual models](01-conceptual-models.md) — Agent, Core, Host, and adapters
4. [Agent Core](03-core-runtime.md) — the minimal execution runtime
5. [Tools and ToolSets](06-tools-and-toolsets.md) — business capabilities
6. [Extension model](07-extension-model.md) — optional Core hooks
7. [Agent Routines](08-agent-routines.md) — advanced concurrency and spawning
8. [Events and delivery](04-events-and-delivery.md) — facts and observation
9. [Orchestration and Hosted Agent](02-agents-as-a-service.md) — multi-Agent coordination and service form
10. [Boundaries and moving parts](05-moving-parts.md) — Core, Orchestration, Host, and adapters
11. [Agent lifecycle](10-agent-lifecycle.md) — closing, retirement, and Conversation retention
12. [Technology stack](09-technology-stack.md) — implementation and integration
13. [Technical specifications](../specs/README.md) — normative contracts

## The system shape

### Agent Core

Core owns the semantics of one Agent: conversation state, the canonical Model → Tool → Model Loop, Tool invocation, cancellation, local limits, Events, and result settlement. Its public handle is the atomic integration surface.

### Orchestration

Orchestration owns the coordination semantics of multiple Agents: Conversation identity, handle retention, routing, admission, scheduling, lifecycle, retirement, inter-Agent communication, and result/Event coordination. It does not mutate Core state or reproduce the Core Loop. A retained Conversation may outlive its current Agent handle and rehydrate a new AgentID. For one directly held Agent it may be ordinary application code; for dynamic or Hosted use it is a first-class Gotato layer.

### Host and existing infrastructure

A Host exposes Orchestration through a protocol adapter and manages remote delivery, readiness, and drain. Infrastructure is the environment chosen by the application: a Go process, an existing RPC server, a Gateway, Kubernetes, storage, secrets, or a load balancer. Both are outside Core; Gotato integrates with them and does not reimplement the platform.

## Progressive disclosure

A first single-Agent integration should need only:

```text
Agent · Model · Tool · Prompt · Result · Close
```

A multi-Agent or Hosted integration additionally opens:

```text
Orchestration · identity · routing · lifecycle · coordination
Stream · Event · cancellation · protocol adapters
```

The internal goroutine, Loop, state confinement, and delivery mechanics remain implementation details unless a caller needs the corresponding contract.
