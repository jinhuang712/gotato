# Gotato Documentation

> **Agent as a Service, native to Go. Agents are goroutines; channels are the boundaries.**

Gotato treats an Agent as a stateful Go runtime unit. The Agent goroutine owns its local state and runs one simple canonical Loop. An optional Agent-as-a-Service composition exposes those routines through Transport and Orchestration.

```text
Embedded: Application goroutine ──channel──► Agent goroutine
Hosted:   Client → Transport goroutines → Orchestration → Agent goroutine
External: Gateway / Kubernetes / LB / Storage
```

The Core owns Agent state, capabilities, and execution. Orchestration schedules requests and connects goroutines. Transport maps wire messages. Infrastructure hosts and routes processes. These boundaries are connected by channels and explicit contracts, not a hierarchy of resource owners.

## Reading order

1. [Philosophy](00-philosophy.md) — project constitution and concurrency model
2. [Conceptual models](01-conceptual-models.md) — Agents, goroutines, channels, and layers
3. [Hosted Agent](02-agents-as-a-service.md) — orchestration and remote access
4. [Agent Core](03-core-runtime.md) — the Go-native runtime and canonical Loop
5. [Events and delivery](04-events-and-delivery.md) — facts and delivery
6. [Moving parts](05-moving-parts.md) — replacement boundaries
7. [Tools and ToolSets](06-tools-and-toolsets.md) — capabilities
8. [Extension model](07-extension-model.md) — lifecycle customization
9. [Agent Routines](08-agent-routines.md) — goroutine-backed Agent execution
10. [Technology stack](09-technology-stack.md) — implementation and deployment
11. [Technical specifications](../specs/README.md) — normative contracts

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
