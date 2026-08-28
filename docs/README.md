# Gotato Documentation

> A stable Go Agent Runtime Core and an optional hosted service composition.

Gotato is Core-first. Embedded mode uses the Core directly inside an existing Go service. Hosted mode is an optional orchestration and transport composition for remote access; it does not define a second Agent execution model.

The project does not claim novelty for the basic Model-to-Tool loop, provider abstraction, or generic distributed hosting. Its focus is a disciplined Go runtime boundary for stateful Agent execution and a small, testable hosted composition around that boundary.

```text
Embedded:  Application → Agent Core → Model / Tools
Hosted:    Client → Transport → Orchestrator → Agent Core
External:  Gateway / Kubernetes / LB / Storage
```

The Core owns Agent semantics. The Orchestrator owns hosted coordination. Transport owns wire mapping. Infrastructure owns deployment. These are separate boundaries even when a small deployment places them in one process.

## Reading order

1. [Philosophy](00-philosophy.md) — architectural constitution
2. [Conceptual models](01-conceptual-models.md) — vocabulary and layers
3. [Hosted Agent](02-agents-as-a-service.md) — orchestration and remote access
4. [Agent Core](03-core-runtime.md) — the canonical loop
5. [Events and delivery](04-events-and-delivery.md) — facts and delivery
6. [Moving parts](05-moving-parts.md) — replacement boundaries
7. [Tools and ToolSets](06-tools-and-toolsets.md) — capabilities
8. [Extension model](07-extension-model.md) — lifecycle customization
9. [Agent Routines](08-agent-routines.md) — child Runs
10. [Technology stack](09-technology-stack.md) — implementation and deployment
11. [Technical specifications](../specs/README.md) — normative contracts

## Layer map

```text
┌──────────────────────────────────────────────────────────┐
│ Application                                              │
│ business workflows · presentation · Agent definitions   │
├──────────────────────────────────────────────────────────┤
│ Infrastructure (external)                                │
│ gateway · Kubernetes · LB · storage · secrets           │
├──────────────────────────────────────────────────────────┤
│ Transport (optional)                                     │
│ gRPC · Protobuf · HTTP projection                       │
├──────────────────────────────────────────────────────────┤
│ Orchestration / Agent Host (optional)                   │
│ admission · routing · concurrency · cache · delivery    │
├──────────────────────────────────────────────────────────┤
│ Agent Core (stable)                                      │
│ Agent · Run · Model/Tool loop · Events · cancellation   │
├──────────────────────────────────────────────────────────┤
│ Model and capability adapters                            │
│ provider routing · database · Redis · HTTP · MCP        │
└──────────────────────────────────────────────────────────┘
```

The same Core can be embedded directly or hosted behind a transport. A hosted service is a composition of boundaries, not a different execution model.
