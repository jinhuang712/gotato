# Gotato

> **Go-native Agent Runtime and Orchestration.**

> Gotato turns a self-contained Agent into an embeddable execution unit and, when needed, an addressable multi-Agent service.

**Status:** Phase 1 implementation underway. The repository contains the Core library, a local Reference Agent service, architecture documents, and implementable specifications.

## What Gotato is

Gotato has one Agent semantics and two scales of use:

```text
Single Agent:
  Application → Agent Core

Multiple Agents:
  Application / Orchestration → Agent Core × N

Hosted Service:
  Client → Protocol Adapter → Host → Orchestration → Agent Core × N
```

The single-Agent path is the smallest entry point. The multi-Agent path is not a second Agent implementation: Orchestration retains and routes Core handles, applies admission, retirement, and lifecycle policy, and coordinates results. A Run can finish while its Agent remains available; if the live Agent is retired, a retained Conversation may later rehydrate it with a new AgentID. Hosted mode adds a protocol and process boundary around that same Orchestration. Core does not provide a global lookup, and an AgentID alone cannot recover a lost in-memory Agent.

```go
agent, err := gotato.NewAgent(
    gotato.WithModel(model),
    gotato.WithInstruction("You are a helpful assistant."),
    gotato.WithTools(tools...),
)
if err != nil {
    return err
}
defer func() { _ = agent.Close(context.Background()) }()

result, err := agent.Prompt(ctx, gotato.UserMessage(input))
```

The code above is the atomic Core path. The Core semantics remain the same as the application grows: Orchestration retains and routes multiple Core handles, and Host exposes that Orchestration remotely. Hosting changes access and delivery; it does not create a second Agent implementation.

## Project principles

### Agents are self-contained goroutines: each owns its state and work.

Each Agent has one Go-native execution unit. Its private conversation state and current Run are confined to that unit, and its public handle provides the safe way to call it. Application Orchestration and Hosts own external request policy, routing, and scheduling.

### Infrastructure hosts. Orchestration coordinates. Host exposes. Agent Core executes.

Agent Core executes one Agent's work. Orchestration creates, addresses, routes, and coordinates multiple Agents. A Host exposes Orchestration through a service boundary, while existing infrastructure hosts and connects the process.

### Tight Core, Open Extensions.

The Core keeps the required Agent semantics small. LLM providers, business capabilities, protocol adapters, and orchestration policies attach through explicit contracts.

## The runtime boundaries

```text
Existing Infrastructure
  hosts and connects the process
              │
              ▼
Host / Protocol Adapter (optional)
  remote access · wire mapping · delivery
              │
              ▼
Orchestration (required for managed multi-Agent use)
  identity · routing · admission · lifecycle · coordination
              │ Agent contract(s)
              ▼
Agent Core × N
  private state · canonical Loop · Tools · Events · cancellation
```

For a single embedded Agent, the application may connect directly to Core. The diagram becomes layered only when the application needs multiple Agents or a remote service boundary.

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

These concerns can be supplied by the application, Orchestration, adapters, or existing platform. They do not stand between a Go service and its first Agent, but a multi-Agent service must assign identity, routing, and lifecycle ownership somewhere outside Core.

## Initial shape

The first useful path is deliberately small:

```text
Model
  + optional Go Tools
  + short-lived conversation state
  + Context cancellation
        ↓
      Agent Core
        ↓
  Response / Events
```

The Core handles the canonical Model → Tool → Model Loop and bounded local work. It does not require a separate service process. When the application needs multiple Agents, Orchestration becomes the next explicit layer for identity, routing, lifecycle, and coordination. Long-term memory, workflows, and durable distributed state remain separate products or future contracts.

## From Embedded Agent to Hosted Service

The product path is a progression from direct execution to coordinated service:

```text
Embedded, single:
  Application → Agent Core

Embedded, multi:
  Application Orchestration → Agent Core × N

Hosted:
  Client → Protocol Adapter → Host → Orchestration → Agent Core × N
```

The initial Hosted PoC may use one process, one Pod, local routing, and an existing Gateway or HTTP/gRPC server. Gotato does not need to implement the platform around it.

## Local Reference Agent

The first implementation includes a deterministic local service assembled from the Gotato library:

```bash
go run ./cmd/gotato-agent --model demo
```

Then call it without an API key or external service:

```bash
curl -X POST http://127.0.0.1:8787/v1/runs \\
  -H 'content-type: application/json' \\
  -d '{"agent_name":"default","conversation_key":"local","prompt":"hello"}'
```

The local service also exposes `/v1/runs/stream` for SSE, `POST /v1/runs/{run_id}/cancel` for best-effort Run cancellation, Conversation retirement, Agent close, health/readiness, and drain. The SSE `agent_start` event contains the `run_id` needed by the cancel endpoint. It is a Reference Agent for testing library semantics, not yet a production deployment.

For an OpenAI-compatible LLM Gateway, configure the Library adapter with YAML:

```bash
cp gateway.example.yaml gateway.yaml
# edit gateway.yaml, or provide ${GOTATO_GATEWAY_API_KEY}
go run ./cmd/gotato-agent --model gateway --gateway-config gateway.yaml
```

The first Pi-compatible provider is also available through Codex Responses SSE:

```bash
cp gateway.codex.example.yaml gateway.yaml
go run ./cmd/gotato-agent --model gateway --gateway-config gateway.yaml
```

The local service allows long-running work with `--run-timeout`, `--model-timeout`, and `--tool-timeout` (defaults: 10m, 5m, and 5m; `0` disables a deadline). This reads the OAuth credential from Pi's `auth.json`, derives the ChatGPT account ID, refreshes expired credentials, and preserves encrypted reasoning artifacts for tool-loop replay. The current Codex adapter intentionally starts with SSE; Pi's WebSocket transport/session cache remains a later optimization.

The `gateway` package owns YAML loading, provider authentication, HTTP/SSE encoding, streaming normalization, retries before stream consumption, and provider errors. Core remains provider-neutral.

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
| [Orchestration and Hosted Agent](docs/02-agents-as-a-service.md) | multi-Agent coordination and service form |
| [Boundaries and moving parts](docs/05-moving-parts.md) | ownership and adapters |
| [Agent lifecycle](docs/10-agent-lifecycle.md) | Run, Agent, retirement, and Conversation retention |
| [Technology stack](docs/09-technology-stack.md) | Core, Orchestration, adapters, and integration |
| [Specifications](specs/README.md) | normative contracts and acceptance |

## Origin

**Inspired by [Pi's Agent Kernel](https://pi.dev), redesigned as a Go-native Agent Runtime.**

Gotato is an independent Go design shaped around a minimal Agent Core and a first-class Orchestration path for multi-Agent services, not a port of Pi's terminal product.

Details and attribution: [shout-out](docs/shout-out.md).

## License

Not yet selected.
