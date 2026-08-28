# Gotato

> A Go-native Agent-as-a-Service with a compact runtime kernel and one canonical Agent loop.

**Status:** design phase. This repository currently contains the architecture documents and the implementable specifications. No Go code has been committed yet.

## What this is

Gotato runs stateful, tool-using Agents and makes them available to other services over gRPC.

```text
Client → Agent Service → Model → Tool → Model → Events → Client
```

Behind that network boundary, a transport-independent Go runtime owns Agent state, the Model/Tool loop, Events, cancellation, and limits. The runtime never imports Protobuf, gRPC, caching, or Kubernetes. That boundary can become a public embedded library once real service use has established a stable API.

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

## Two directions

The product is discovered from the service inward. The code depends inward.

```text
Product discovery
  service use case → required semantics → stable runtime contract

Code dependency
  gRPC transport → service layer → runtime kernel
```

A working service reveals what callers actually need from Agent identity, conversation state, commands, Events, cancellation, errors, admission, and lifecycle. Guessing at a "minimal library" first tends to produce abstractions that no caller asked for.

## Ideas that shape it

```text
one canonical Agent loop        service, direct Go use, and child Agents converge on it
fixed rails, moving parts       Core fixes ordering; named stages decide behavior
Tool and ToolSet                one operation; one capability domain with staged discovery
Agent Routine                   a managed child Agent Run with its own transcript
one terminal Event              agent_end is final; nothing resumes after it
explicit bounds                 every stream, batch, queue, and Routine has a stated limit
no first-party UI               applications own CLI, TUI, web, and chat
```

## Documentation

`docs/` explains the intended architecture. `specs/` defines what an implementation must satisfy.

| Document | Subject |
|---|---|
| [Philosophy](docs/00-philosophy.md) | purpose and lasting principles |
| [Conceptual models](docs/01-conceptual-models.md) | vocabulary and boundaries |
| [Agent as a Service](docs/02-agents-as-a-service.md) | the hosted product boundary |
| [Core runtime](docs/03-core-runtime.md) | state owner and canonical loop |
| [Events and delivery](docs/04-events-and-delivery.md) | facts, backpressure, settlement |
| [Runtime Moving Parts](docs/05-moving-parts.md) | complete visual map |
| [Tools and ToolSets](docs/06-tools-and-toolsets.md) | capability model |
| [Extension model](docs/07-extension-model.md) | behavior at named stages |
| [Agent Routines](docs/08-agent-routines.md) | managed child Agent Runs |
| [Technology stack](docs/09-technology-stack.md) | Go, Protobuf, gRPC, Kubernetes |

Entry points: [docs](docs/README.md) · [specs](specs/README.md)

## Origin

Gotato is inspired by [Pi](https://pi.dev), a minimal and highly extensible coding-agent harness created by Mario Zechner and its contributors. Pi demonstrates that a stateful, tool-using Agent runtime can stay compact and understandable. Gotato is an independent Go implementation of that idea shaped around a service boundary, not a port. Pi's terminal product, coding Tools, and session-tree features are out of scope here.

Details and attribution: [shout-out](docs/shout-out.md).

## License

Not yet selected.
