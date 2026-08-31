# Gotato

> **Agent as a Service.**
>
> **Core Native to Go.**

**Status:** design phase. Gotato is being designed as a small, embeddable Agent runtime with an optional Hosted form. The repository currently contains architecture documents and implementable specifications; no Go implementation has been committed.

## What Gotato is

Gotato gives an existing Go service a basic Agent without asking the service to assemble a Runner, a workflow engine, or a platform. The caller supplies a Model and optional Tools, then calls the Agent through a small Go interface. Gotato owns the Agent Loop, conversation state, Tool execution, cancellation, and Events internally.

```go
agent, err := gotato.NewAgent(
    gotato.WithModel(model),
    gotato.WithInstruction("You are a helpful assistant."),
    gotato.WithTools(tools...),
)
if err != nil {
    return err
}

result, err := agent.Prompt(ctx, gotato.UserMessage(input))
```

The same Agent can stay inside the existing service or be exposed as an Agent Service later:

```text
Embedded: Application → Agent Core
Hosted:   Client → Agent Host / Orchestration → Agent Core
```

Hosted mode changes access and coordination, not the Agent implementation. A remote caller uses the same Agent semantics as a local caller.

## Project principles

### Agents are goroutines.

Each Agent has one Go-native execution unit. Its private state is confined to that unit, and its public handle provides the safe way to call it.

### Agents own their work.

An Agent owns its private conversation state and current Run. Callers and Hosts own external request policy, routing, and scheduling.

### Infrastructure hosts. Orchestration coordinates. Agent Core executes.

The Agent Core executes Agent work. Orchestration creates, routes, and coordinates Agents. Existing infrastructure hosts and connects the process.

### Tight Core, Open Extensions.

The Core keeps the required Agent semantics small. LLM providers, business capabilities, protocol adapters, and orchestration policies attach through explicit contracts.

## The three system boundaries

```text
Existing Infrastructure
  hosts and connects the process
              │
              ▼
Agent Host / Orchestration
  admission · routing · coordination · lifecycle · delivery
              │ Agent contract
              ▼
Agent Core
  private state · canonical Loop · Tools · Events · cancellation
```

LLM and Tool adapters connect to the sides of Core:

```text
Agent Core ── LLM Adapter ──► Model provider
Agent Core ── Tool Adapter ──► application system
```

A protocol adapter may connect a remote client to the Host. It is an implementation detail of the Host boundary, not a fourth semantic layer and not a Core dependency.

## What stays out of the Core

Gotato Core does not require:

```text
service discovery
message brokers
workflow engines
long-term memory or retrieval
artifact platforms
Kubernetes or a Gateway
provider SDKs
```

These concerns can be supplied by the application, Host, adapters, or existing platform. They do not stand between a Go service and its first Agent.

## Initial shape

The first useful path is deliberately small:

```text
Model
  + optional Go Tools
  + short-lived conversation state
  + Context cancellation
        ↓
      Agent
        ↓
  Response / Events
```

The Core handles the canonical Model → Tool → Model Loop and bounded local work. It does not require a separate service process. Long-term memory, workflows, multi-agent policy, and durable distributed state are separate products or extensions, not prerequisites for a basic Agent.

## Agent as a Service

A Hosted Agent Service is a thin composition around the same Core:

```text
Client
  ↓ protocol adapter
Agent Host / Orchestration
  ├── Agent creation and routing
  ├── admission and request policy
  ├── lifecycle and cancellation
  └── Event delivery
  ↓ Agent contract
Agent Core
```

The initial Hosted PoC may use one process, one Pod, local routing, and an existing Gateway or HTTP/gRPC server. Gotato does not need to implement the platform around it.

## Documentation

`docs/` explains why the architecture is shaped this way. `specs/` defines implementable contracts.

| Document | Subject |
|---|---|
| [Philosophy](docs/00-philosophy.md) | project principles and boundaries |
| [Glossary](docs/glossary.md) | shared vocabulary |
| [Conceptual models](docs/01-conceptual-models.md) | Agent, Core, Host, and adapters |
| [Agent Core](docs/03-core-runtime.md) | the Go-native runtime |
| [Tools and ToolSets](docs/06-tools-and-toolsets.md) | capabilities |
| [Extensions](docs/07-extension-model.md) | Core customization |
| [Agent Routines](docs/08-agent-routines.md) | advanced concurrency and spawning |
| [Events and delivery](docs/04-events-and-delivery.md) | Agent facts and Host delivery |
| [Hosted Agent](docs/02-agents-as-a-service.md) | the optional service form |
| [Boundaries and moving parts](docs/05-moving-parts.md) | ownership and adapters |
| [Technology stack](docs/09-technology-stack.md) | implementation and integration |
| [Specifications](specs/README.md) | normative contracts and acceptance |

## Origin

Gotato is inspired by [Pi](https://pi.dev), a minimal and highly extensible coding-agent harness created by Mario Zechner and its contributors. Gotato is an independent Go design shaped around a small Agent Core and an optional Hosted composition, not a port of Pi's terminal product.

Details and attribution: [shout-out](docs/shout-out.md).

## License

Not yet selected.
