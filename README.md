# Gotato

> **Agent as a Service.**
>
> **Core Native to Go.**

**Status:** design phase. This repository currently contains architecture documents and implementable specifications; no Go implementation has been committed.

## What this is

Gotato makes a stateful Agent a normal Go runtime unit. Each Agent runs as a goroutine, owns its private state and work, and communicates through explicit channels and capabilities. Agent Core executes the canonical Model → Tool → Model Loop.

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

In Embedded mode, the application sends a Prompt directly to Agent Core and waits for its RunResult. In Hosted mode, a client sends the same kind of command through a Run stream. Both paths reach the same Agent execution model.

Agent-as-a-Service is a remote access and coordination form, not a second Agent implementation. Orchestration may create goroutines, queue or reject Prompts, choose preemption policy, and connect Agents through channels. Infrastructure such as gateways, load balancers, Kubernetes, storage, and credentials remains an external deployment concern.

## Project principles

### Agents are goroutines

Each Agent runs as a goroutine with private state and explicit channel endpoints. It processes one Prompt or Continue at a time; external callers may invoke, steer, follow up, observe, or abort, but they do not directly mutate Agent state or run a parallel Loop.

### Agents own their work

The Agent goroutine is the authority for its own state, transcript, capabilities, and current execution. Agent-to-Agent and Agent-to-Orchestration communication uses explicit channels; spawning does not create shared mutable state or resource ownership.

### Infrastructure hosts. Orchestration coordinates. Agent Core executes.

Infrastructure hosts and routes processes. Orchestration is a set of Go goroutines and channels that admits, schedules, routes, and coordinates Agent work. Agent Core provides the state machine and executes the canonical Loop; the Agent goroutine remains the only authority over its private state, and no surrounding layer duplicates that Loop.

### Tight Core, Open Extensions

The Core contains the stable semantics every Agent needs. Model providers, Tools, ToolSets, Extensions, transport, orchestration, and platform integration connect through explicit boundaries instead of hidden globals or copied execution logic.

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

`docs/` explains why the architecture is shaped this way. `specs/` defines implementable contracts.

| Document | Subject |
|---|---|
| [Philosophy](docs/00-philosophy.md) | project principles and boundaries |
| [Glossary](docs/glossary.md) | shared vocabulary |
| [Conceptual models](docs/01-conceptual-models.md) | Agents, layers, and responsibilities |
| [Agent Core](docs/03-core-runtime.md) | Go-native runtime and canonical Loop |
| [Tools and ToolSets](docs/06-tools-and-toolsets.md) | capability composition |
| [Extensions](docs/07-extension-model.md) | Core lifecycle joints |
| [Agent Routines](docs/08-agent-routines.md) | goroutine-backed Agents |
| [Events and delivery](docs/04-events-and-delivery.md) | canonical facts and delivery |
| [Hosted Agent](docs/02-agents-as-a-service.md) | remote access and coordination |
| [Moving parts](docs/05-moving-parts.md) | replaceable boundaries by layer |
| [Technology stack](docs/09-technology-stack.md) | Go, providers, transport, and deployment |
| [Specifications](specs/README.md) | normative contracts and acceptance |

## Origin

Gotato is inspired by [Pi](https://pi.dev), a minimal and highly extensible coding-agent harness created by Mario Zechner and its contributors. Gotato is an independent Go design shaped around a channel-driven Agent Runtime and an optional Agent-as-a-Service composition, not a port of Pi's terminal product.

Details and attribution: [shout-out](docs/shout-out.md).

## License

Not yet selected.
