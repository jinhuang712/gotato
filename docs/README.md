# Gotato Documentation

> **Agent as a Service.**

> Gotato is a minimal, Go-native runtime for self-contained, stateful Agents.

The runtime is easily embedded in an existing Go service and can be exposed as a Hosted Agent Service. The user-facing idea is simple: provide a Model and optional Tools, then call a stateful Agent. Agent Core owns the execution details. “As a Service” describes the Agent's callable boundary; Hosted deployment is optional.

```text
Embedded: Application ── Agent interface ──► Agent Core
Hosted:   Client ── protocol adapter ──► Host / Orchestration ──► Agent Core
```

The two paths share one Agent contract and one execution model. Hosting changes access, routing, and lifecycle; it does not create a second Agent implementation. In this vocabulary, Orchestration coordinates, Host exposes the service boundary, and Infrastructure hosts the processes.

## The architecture in one picture

```text
                         Existing Infrastructure
                    hosts and connects the process
                                   │
                                   ▼
                        Agent Host / Orchestration
                 admission · routing · lifecycle · delivery
                                   │ Agent contract
                                   ▼
                              Agent Core
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
9. [Hosted Agent](02-agents-as-a-service.md) — the optional service form
10. [Boundaries and moving parts](05-moving-parts.md) — ownership and adapters
11. [Technology stack](09-technology-stack.md) — implementation and integration
12. [Technical specifications](../specs/README.md) — normative contracts

## Three system boundaries

### Agent Core

Core owns the minimal set of semantics needed by every Agent: conversation state, the canonical Model → Tool → Model Loop, Tool invocation, cancellation, local limits, and canonical Events. Its public handle is the main integration surface.

### Agent Host / Orchestration

The Host is optional. It coordinates multiple Agent instances, applies admission and routing policy, attaches protocol adapters, forwards Events, and manages lifecycle. It calls Core through a stable Agent contract.

### Existing infrastructure

Infrastructure is the environment chosen by the application: a Go process, an existing RPC server, a Gateway, Kubernetes, storage, secrets, or a load balancer. It is outside Gotato; Gotato integrates with it and does not reimplement it.

## Progressive disclosure

A first integration should need only:

```text
Agent · Model · Tool · Prompt · Result
```

Advanced users can open:

```text
Stream · Event · cancellation · Host · Orchestration · protocol adapters
```

The internal goroutine, Loop, state confinement, and delivery mechanics remain implementation details unless a caller needs the corresponding contract.
