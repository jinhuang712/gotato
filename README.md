# Gotato

> A Go-native Agent Core with an optional Agent-as-a-Service host.

**Status:** design phase. This repository currently contains architecture documents and implementable specifications; no Go implementation has been committed.

## What this is

Gotato has a stable, self-contained in-process kernel and an optional hosted composition:

```text
Embedded application
        │
        ▼
   Agent Core ───────► Model Router / Provider Adapters
        │
        └────────────► Tools / ToolSets / Capability Adapters

Hosted application
        │
        ▼
Transport ─► Orchestrator / Agent Host ─► Agent Core
                    │
                    ├── admission and concurrency
                    ├── conversation ownership
                    ├── event delivery
                    └── lifecycle and drain
```

The Core owns Agent execution. The Orchestrator owns the coordination of many Core instances. Transport maps a protocol onto the Orchestrator. Infrastructure such as gateways, load balancers, Kubernetes, storage, and credentials remains an external deployment concern.

## Two first-class modes

### Embedded Agent mode

A regular Go service can use the Core directly:

```text
HTTP/RPC handler or business workflow
        │
        ├── database / Redis / domain APIs
        └── Agent Core.Prompt(...)
```

The application may assemble Tools, query data itself, or let the Agent call explicitly installed Tools. It does not need a Gotato gateway, Agent Service, conversation router, or Kubernetes integration.

### Hosted Agent mode

An application that wants remote Agent access composes the Core with an Orchestrator and a transport adapter:

```text
Client → optional Gateway/LB → gRPC Transport
       → Orchestrator → Agent Core → Model / Tools
```

This mode provides concurrent streams, conversation resolution, admission, cancellation, canonical Event projection, bounded delivery, readiness, and drain.

## Stable boundaries

```text
Agent Core
  Agent · Run · Turn · Model contract · Tool · ToolSet
  Agent Routine · canonical Events · Context · local Limits

Orchestration / Agent Host
  Factory · conversation ownership · admission · concurrency
  cache/lease · stream attachment · Event bridge · lifecycle

Transport
  Protobuf · gRPC · HTTP projections · wire error mapping

Infrastructure
  Gateway · Kubernetes · load balancing · storage · secrets

Model layer
  Model Router · provider adapters · fallback · provider policy
```

The Core is transport-, host-, and provider-independent. The hosted path and the embedded path execute the same canonical Agent loop.

## Core invariants

```text
one Agent has at most one active mutating Run
one canonical Agent loop exists
Prompt, Continue, Tool, Routine, and cancellation share that loop
canonical Events are immutable runtime facts
one Run emits exactly one terminal agent_end Event
all owned work has a Context, bound, and settlement owner
```

A single Agent may use internal goroutines for bounded Tool and Routine work. The semantic guarantee is one state owner, not a public promise about a particular goroutine layout.

## Documentation

`docs/` explains the architecture. `specs/` defines implementable contracts.

| Document | Subject |
|---|---|
| [Philosophy](docs/00-philosophy.md) | boundaries and lasting principles |
| [Conceptual models](docs/01-conceptual-models.md) | Core, Host, Transport, Infrastructure, and modes |
| [Hosted Agent](docs/02-agents-as-a-service.md) | orchestration and remote service |
| [Agent Core](docs/03-core-runtime.md) | self-contained runtime kernel |
| [Events and delivery](docs/04-events-and-delivery.md) | canonical facts and remote delivery |
| [Moving parts](docs/05-moving-parts.md) | replaceable boundaries by layer |
| [Tools and ToolSets](docs/06-tools-and-toolsets.md) | capability composition |
| [Extensions](docs/07-extension-model.md) | Core lifecycle joints |
| [Agent Routines](docs/08-agent-routines.md) | managed child Runs |
| [Technology stack](docs/09-technology-stack.md) | Go, providers, transport, and deployment |
| [Specifications](specs/README.md) | normative contracts and acceptance |

## Origin

Gotato is inspired by [Pi](https://pi.dev), a minimal and highly extensible coding-agent harness created by Mario Zechner and its contributors. Gotato is an independent Go design shaped around a reusable Agent Core and an optional service host, not a port of Pi's terminal product.

Details and attribution: [shout-out](docs/shout-out.md).

## License

Not yet selected.
