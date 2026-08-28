# Gotato

> **Agent as a Service, native to Go.**
>
> **Agents are goroutines. Channels are the boundaries.**
>
> **Tight Core, Open Extensions.**

**Status:** design phase. This repository currently contains architecture documents and implementable specifications; no Go implementation has been committed.

## What this is

Gotato makes a stateful Agent a normal Go runtime unit. An Agent owns its private state and runs one simple Model → Tool → Model Loop in a Go goroutine. It communicates through explicit channels and capabilities.

```text
Caller / Client
      │ command channel
      ▼
Agent handle ──► Agent goroutine
                    ├── private state
                    ├── canonical Loop
                    ├── Model / Tools / Extensions
                    └── result / Event channels
```

The same Agent can be used directly from a Go process or exposed as Agent-as-a-Service:

```text
Embedded: Application goroutine ──channel──► Agent goroutine
Hosted:   Client → Transport → Orchestration → Agent goroutine
```

Agent-as-a-Service is a remote access and coordination form, not a second Agent implementation. Orchestration may create goroutines, queue or reject Prompts, choose preemption policy, and connect Agents through channels. Infrastructure such as gateways, load balancers, Kubernetes, storage, and credentials remains an external deployment concern.

## Project principles

### The Agent owns the Loop

The Agent goroutine is the authority for its own state, transcript, capabilities, and current execution. It processes one Prompt or Continue at a time. External callers may invoke, steer, follow up, observe, or abort; they do not directly mutate Agent state or run a parallel Loop.

### Tight Core, Open Extensions

The Core contains the stable semantics every Agent needs. Model providers, Tools, ToolSets, Extensions, transport, orchestration, and platform integration connect through explicit boundaries instead of hidden globals or copied execution logic.

### Orchestration schedules; Agents execute

Orchestration is a set of Go goroutines and channels. It decides whether external Prompts are rejected, queued, prioritized, steered, or aborted, and when a free Agent receives the next command. It does not become the owner of Agent state.

### Agents communicate through channels

A spawned Agent is an independent Agent goroutine. Spawn provenance may be correlated, but it does not create a resource ownership hierarchy or automatic shared state. Cancellation, waiting, and result delivery are explicit signals.

## Core invariants

```text
one Agent goroutine processes one Prompt or Continue at a time
external request queueing is caller/Orchestration policy
one canonical Agent Loop exists
Prompt, Continue, Tool, and control messages converge on that Loop
canonical Events are immutable runtime facts
one Run emits exactly one terminal agent_end Event
Core never depends on Transport, Host, Infrastructure, or provider SDKs
```

A single Agent may use internal goroutines for bounded Tool work. Agent-to-Agent and Agent-to-Orchestration communication uses explicit channels. The public contract does not expose an implementation-specific channel layout, but the Go-native routine model is fundamental.

## Stable boundaries

```text
Agent Core
  Agent goroutine · state · Run · Turn · Model contract
  Tool · ToolSet · Extension · Agent Routine · canonical Events

Orchestration / Agent Host
  Agent creation · request admission · queue policy
  routing · stream attachment · Event forwarding · lifecycle

Transport
  Protobuf · gRPC · HTTP projections · wire error mapping

Infrastructure
  Gateway · Kubernetes · load balancing · storage · secrets

Model and capability adapters
  Model Router · provider adapters · database · Redis · HTTP · MCP
```

## Initial PoC

The initial Hosted PoC deliberately uses:

```text
one Host process
one Pod
process-local Agent registry and routing
local goroutines and channels
```

It validates the Agent Core and its hosted composition without claiming cross-Pod Conversation continuity. Multi-Pod routing and durable restoration remain separate future work.

## Documentation

`docs/` explains the architecture. `specs/` defines implementable contracts.

| Document | Subject |
|---|---|
| [Philosophy](docs/00-philosophy.md) | project principles and boundaries |
| [Conceptual models](docs/01-conceptual-models.md) | Agents, goroutines, channels, and layers |
| [Hosted Agent](docs/02-agents-as-a-service.md) | orchestration and remote service |
| [Agent Core](docs/03-core-runtime.md) | Go-native runtime and canonical Loop |
| [Events and delivery](docs/04-events-and-delivery.md) | canonical facts and delivery |
| [Moving parts](docs/05-moving-parts.md) | replaceable boundaries by layer |
| [Tools and ToolSets](docs/06-tools-and-toolsets.md) | capability composition |
| [Extensions](docs/07-extension-model.md) | Core lifecycle joints |
| [Agent Routines](docs/08-agent-routines.md) | goroutine-backed Agents |
| [Technology stack](docs/09-technology-stack.md) | Go, providers, transport, and deployment |
| [Specifications](specs/README.md) | normative contracts and acceptance |

## Origin

Gotato is inspired by [Pi](https://pi.dev), a minimal and highly extensible coding-agent harness created by Mario Zechner and its contributors. Gotato is an independent Go design shaped around a channel-driven Agent Runtime and an optional Agent-as-a-Service composition, not a port of Pi's terminal product.

Details and attribution: [shout-out](docs/shout-out.md).

## License

Not yet selected.
