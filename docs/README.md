# Gotato Documentation

> **Agent as a Service.**
>
> **Core Native to Go.**

Gotato treats an Agent as a stateful Go runtime unit that runs as a goroutine. Agents own their work and communicate through explicit channels. An optional Agent-as-a-Service composition exposes those routines through Transport and Orchestration without replacing Agent Core.

The same Agent Core works in two modes:

- **Embedded:** a Go application calls the Agent directly.
- **Hosted:** a remote client reaches the Agent through Transport and Orchestration.

Hosted mode changes access and coordination, not the Agent's execution model.

```text
Embedded: Application goroutine ──channel──► Agent goroutine
Hosted:   Client → Transport goroutines → Orchestration → Agent goroutine
External: Gateway / Kubernetes / LB / Storage
```

Infrastructure hosts and routes processes. Transport maps wire messages. Orchestration admits, schedules, routes, and coordinates Agent work. Each Agent goroutine owns its state and execution; Agent Core provides the runtime and canonical Loop. Each layer has a distinct role and connects to the next through explicit contracts and channels. The Core stays tight while models, capabilities, transport, and infrastructure remain open extensions.

## Reading order

1. [Philosophy](00-philosophy.md) — project constitution and principles
2. [Glossary](glossary.md) — shared vocabulary
3. [Conceptual models](01-conceptual-models.md) — Agents, layers, and responsibilities
4. [Agent Core](03-core-runtime.md) — the Go-native runtime
5. [Tools and ToolSets](06-tools-and-toolsets.md) — capabilities
6. [Extension model](07-extension-model.md) — Core customization
7. [Agent Routines](08-agent-routines.md) — goroutines, spawning, and cancellation
8. [Events and delivery](04-events-and-delivery.md) — facts and delivery
9. [Hosted Agent](02-agents-as-a-service.md) — remote access and coordination
10. [Moving parts](05-moving-parts.md) — replaceable boundaries
11. [Technology stack](09-technology-stack.md) — implementation and deployment
12. [Technical specifications](../specs/README.md) — normative contracts

## Layer map

```text
┌──────────────────────────────────────────────────────────┐
│ Application / Client                                     │
│ business workflows · requests · presentation             │
├──────────────────────────────────────────────────────────┤
│ Transport (optional)                                     │
│ gRPC · Protobuf · HTTP projection                       │
├──────────────────────────────────────────────────────────┤
│ Orchestration (optional)                                 │
│ admission · queue · routing · coordination · delivery   │
├──────────────────────────────────────────────────────────┤
│ Agent Core                                               │
│ goroutine · private state · canonical Loop · Events     │
├──────────────────────────────────────────────────────────┤
│ Open extensions and adapters                             │
│ Model · Tools · ToolSets · Extensions · capabilities    │
├──────────────────────────────────────────────────────────┤
│ Infrastructure (external)                                │
│ gateway · Kubernetes · LB · storage · secrets           │
└──────────────────────────────────────────────────────────┘
```

The same Agent Core can be called inside a Go process or reached through a Hosted transport. Hosted mode schedules access to Agents; it does not replace their execution model.
