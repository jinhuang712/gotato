# Gotato Documentation

> A Go-native Agent-as-a-Service with a compact runtime kernel and one canonical Agent loop.

Gotato's first-class external interface is gRPC. Behind that interface, a transport-independent Go runtime owns Agent state, Model/Tool execution, Events, cancellation, and limits.

Each document below is self-contained. None depends on another, and none refers to another; a concept a document needs, it states.

## Reading order

0. [Shout-out and project origin](shout-out.md) — optional background
1. [Philosophy](00-philosophy.md)
2. [Conceptual models](01-conceptual-models.md)
3. [Agent as a Service](02-agents-as-a-service.md)
4. [Core runtime](03-core-runtime.md)
5. [Events and delivery](04-events-and-delivery.md)
6. [Runtime Moving Parts](05-moving-parts.md)
7. [Tools and ToolSets](06-tools-and-toolsets.md)
8. [Extension model](07-extension-model.md)
9. [Agent Routines](08-agent-routines.md)
10. [Technology stack and runtime primitives](09-technology-stack.md)
11. [Technical specifications](../specs/README.md)

The number on a filename is a suggested entry point, not a prerequisite. Any document can be read first.

## Architecture at a glance

```text
Go / other service
       │
       │ gRPC
       ▼
Gotato Agent Service
  factory · admission · lifecycle · Event bridge
       │
       ▼
Go Runtime Kernel
  Agent · Run · Model · Tools · Events · cancellation
       │
       ├──► Model adapters
       ├──► capability adapters
       └──► Agent Routines
```

The code dependency points inward: transport and hosting depend on the runtime kernel; the kernel never depends on gRPC, Protobuf, caching, or Kubernetes.

## Documentation layers

```text
┌──────────────────────┐
│ Philosophy           │  purpose and lasting principles
└──────────┬───────────┘
           ▼
┌──────────────────────┐
│ Concept documents    │  vocabulary, boundaries, and architecture
└──────────┬───────────┘
           ▼
┌──────────────────────┐
│ Specifications       │  implementable and testable contracts
└──────────┬───────────┘
           ▼
┌──────────────────────┐
│ Implementation       │  service, runtime packages, clients, and adapters
└──────────────────────┘
```

The documents describe the enduring architecture. Delivery sequence, acceptance criteria, and API promotion decisions belong in `specs/`.
