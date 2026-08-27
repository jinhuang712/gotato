# Technology Stack and Runtime Primitives

**Status:** Draft

> Gotato uses Protobuf and gRPC for its external service contract and Go-native primitives for its transport-independent runtime.

## 1. Stack overview

```text
Clients and Go Services
        │
        │ gRPC + Protobuf
        ▼
┌──────────────────────────────────────────────┐
│ Gotato Agent Service                        │
│ factory · cache · admission · Event bridge  │
├──────────────────────────────────────────────┤
│ Go Runtime Kernel                           │
│ Agent · Model · Tools · Events · Context    │
├──────────────────────────────────────────────┤
│ Adapters                                    │
│ Model providers · HTTP/gRPC/MCP capabilities│
└───────────────┬──────────────────────────────┘
                │
                ▼
       Kubernetes Deployment
```

The service use case drives behavior. Package dependencies point inward toward runtime contracts.

## 2. Technology map

| Layer | Technology | Role |
|---|---|---|
| Language | Go | Runtime, service, clients, and adapters |
| Service IDL | Protocol Buffers | Versioned external Agent contract |
| Service transport | gRPC | Bidirectional attached Run stream |
| Cancellation | `context.Context` | Stream, Run, Model, Tool, Routine, and shutdown propagation |
| Concurrency | goroutines | Runtime child work and service coordination |
| Coordination | channels, `sync`, `errgroup` | Bounded bridges, queues, groups, and settlement |
| Tool schemas | JSON Schema | Model-facing Tool argument contracts |
| Deployment | Kubernetes | Replication, health, drain, and resource bounds |
| Packaging | Go modules and OCI images | Runtime packages, clients, adapters, and service artifact |
| Observability | structured logging and OpenTelemetry | Correlated runtime and transport diagnostics |
| Testing | `testing`, deterministic fakes, race detector, in-process gRPC | Runtime and service acceptance |

## 3. Go package boundary

A coherent implementation keeps service and runtime packages distinct from the beginning:

```text
cmd/gotatod
      │
      ▼
service / transport
      │ ordinary Go contracts
      ▼
runtime kernel
      │
      ├── model contracts
      ├── tool contracts
      └── event contracts
```

The runtime package can begin as an internal implementation boundary. Promotion to a public library adds compatibility commitments without changing the service's dependency direction.

Runtime packages do not import generated Protobuf, gRPC, Kubernetes, cache, or process-hosting packages.

## 4. Go runtime primitives

Gotato relies on:

```text
small interfaces       Model, Tool, ToolSet, Extension contracts
context.Context        cancellation and deadlines
ordinary errors        typed runtime and service failure categories
generics                typed Tool helpers where useful
goroutines              concurrent owned work
channels                bounded internal communication
sync primitives         state ownership and lifecycle coordination
```

The supported Go version is pinned by `go.mod`. Runtime dependencies remain deliberately small.

## 5. Context ownership

`context.Context` carries cancellation, deadlines, and request-scoped values across ownership boundaries:

```text
gRPC stream Context
        ↓
service Run Context
        ↓
runtime Run Context
  ├── Model stream
  ├── Tool Uses
  ├── Extensions and observers
  └── Agent Routines
```

Model reasoning context is a different concept represented by Messages and Model request content.

Every blocking operation selects on its owning Context. Child deadlines fit within parent deadlines.

## 6. Goroutines and Agent Routines

Goroutines schedule concurrent work:

```text
attached gRPC stream
  ├── command receiver
  ├── runtime Run
  └── bounded Event sender

runtime Run
  ├── Model stream receiver
  ├── bounded Tool workers
  └── local child Agent workers
```

An Agent Routine is a managed child Agent Run. A goroutine is one possible local scheduling mechanism.

```text
Agent Routine semantics
  identity · child Agent · child Run · cancellation · Result

Goroutine mechanics
  scheduling · stack · blocking · synchronization
```

Every goroutine belongs to a Context and settlement boundary. No request, Tool, subscriber, bridge, or Routine creates unowned background work.

## 7. Channels

Channels remain internal coordination primitives rather than the primary public API.

```text
bounded Event bridge
Steering and Follow-up queues
Tool and Routine progress
service command handoff
worker admission
```

Channel rules:

```text
producer owns close
capacity is explicit
Context participates in blocking send and receive
terminal facts retain delivery priority
optional progress may be coalesced
channels stay behind typed APIs
```

In Go this means a bounded channel plus a cancellable send:

```go
select {
case queue <- event:
case <-ctx.Done():
    return ctx.Err()
}
```

The failure modes are specific:

```text
unbounded channel      no backpressure; memory tracks the slowest consumer
bare send              backpressure becomes deadlock when nobody receives
detached sender        work outlives the Context that authorized it
receiver closes        the next send panics
```

The external API uses gRPC streams. A direct Go API uses runtime methods and Event subscriptions. Channel ownership stays inside the creating package.

## 8. Synchronization and `errgroup`

`golang.org/x/sync/errgroup` supports cancellation-aware groups when its first-error semantics match the selected policy. Bounded Tool and Routine groups may require a focused coordinator for collect-all, partial, or first-success behavior.

Synchronization protects small ownership boundaries:

```text
Agent active-Run state
Steering and Follow-up queues
observer registry
Agent cache entries
admission counters
readiness and drain state
```

Mutexes and atomics do not become part of public contracts. Applications interact through operations and immutable snapshots.

## 9. JSON Schema

Tool inputs use JSON Schema because Model providers share this capability format:

```text
Go input type
      ├── explicit Schema
      └── typed-function adapter
              ↓
        JSON Schema
              ↓
      Runtime Tool Spec
              ↓
       provider adapter
```

The selected implementation supports deterministic generation, strict validation, useful error paths, and the provider-compatible subset required by supported adapters.

## 10. Protocol Buffers

Protobuf is the source of truth for the Agent service wire contract:

```text
gotato.agent.v1
  AgentService
  RunCommand
  RunEvent
  portable Message and Event payloads
  portable error details
```

Generated types stay at the transport boundary:

```text
protobuf RunCommand
      ↓ mapper
runtime command and Message
      ↓
runtime execution
      ↓
canonical Event
      ↓ projector
protobuf RunEvent
```

Compatibility follows standard Protobuf rules:

```text
field numbers remain stable
removed fields are reserved
new fields are additive
enums retain an unspecified value
oneof command and Event variants evolve compatibly
```

The standard Go Protobuf toolchain generates server and client bindings. Buf or an equivalent build wrapper can provide linting and breaking-change checks.

## 11. gRPC

`google.golang.org/grpc` is the canonical external transport:

```proto
service AgentService {
  rpc Run(stream RunCommand) returns (stream RunEvent);
}
```

Bidirectional streaming carries:

```text
client → Start · Steer · FollowUp · Cancel
server → lifecycle · Message · Tool · Routine · terminal Events
```

The stream Context participates in Run cancellation. gRPC status represents protocol and service failures. Tool and Routine failures remain runtime Results when parent reasoning can continue.

The first-party Go client presents a typed remote Agent experience without requiring applications to implement command ordering, Event decoding, or cancellation mapping.

## 12. Bounded Event transport

The service decouples runtime observation from network transmission with a bounded bridge:

```text
Runtime Event producer
        ↓
projection and redaction
        ↓
bounded queue
        ↓
gRPC sender
```

The bridge defines:

```text
capacity
progress coalescing
producer backpressure
terminal Event priority
slow-consumer termination
Context cancellation
settlement deadline
```

Unbounded channels and detached sender goroutines are not acceptable service defaults.

The split matters because runtime observers are awaited inside the loop. A local observer may hold the loop briefly; a network peer may not hold it at all, so the gRPC sender lives on the far side of the queue:

```text
Runtime goroutine              Sender goroutine
─────────────────              ────────────────
emit → observers → enqueue     dequeue → grpc.Send
awaited · local · fast          slow · remote · bounded
```

When the queue is full, the enqueue side applies the configured policy instead of waiting forever. Runtime execution then settles on its own schedule, and delivery settles on the client's.

## 13. In-service Agent caching

Conversation Agents can live in a bounded process-local cache:

```text
Conversation Key
       │
       ▼
┌────────────────────────────┐
│ Agent Cache                │
│ max entries · TTL · pin    │
└─────────────┬──────────────┘
              ▼
         stateful Agent
```

The cache contract includes:

```text
per-key creation coordination
active-Run pinning
idle-only eviction
explicit reset
fake-clock testing
cache metrics
```

The cache stores live runtime objects, not durable truth. An external state provider can restore Agents across processes while the cache remains a bounded optimization.

Model responses are not cached by default because their output depends on transcript, Model version, sampling, visible Tools, and external state.

## 14. Kubernetes

Kubernetes is the standard hosting environment:

```text
Deployment      replicated Gotato processes
Service         stable gRPC endpoint
Gateway         optional external routing
ConfigMap       non-secret configuration
Secret/identity credentials and workload identity
Probes          liveness and readiness
HPA             resource and admission signals
PDB             controlled disruption
```

Graceful termination follows service lifecycle:

```text
SIGTERM
   ↓
readiness = false
   ↓
stop new Run admission
   ↓
settle active Runs until deadline
   ↓
apply DrainPolicy
   ↓
flush bounded delivery and telemetry
   ↓
exit
```

Kubernetes does not define Agent semantics; it consumes service health and drain signals.

## 15. Observability

Structured logs and OpenTelemetry consume canonical correlation:

```text
request_id
service stream ID
Agent name
conversation key
runtime Run ID
Turn sequence
Model call ID
ToolSet and Tool identity
Tool Call ID
Routine and child Run ID
terminal status
latency and usage
```

Secrets, credentials, raw prompts, and unrestricted Tool payloads stay outside default telemetry. Redaction occurs before export.

## 16. Packaging

```text
Go modules        runtime, service, client, and adapter versioning
Protobuf sources  external compatibility source of truth
generated code    transport and optional Tool glue
OCI image         executable Agent service
```

Separate modules are useful when they isolate provider SDKs, transport dependencies, or release commitments. Module boundaries follow dependency ownership rather than repository aesthetics.

## 17. Testing

```text
runtime tests
  scripted Model · fake Tools · Event recorder · fake clock

service tests
  in-process gRPC · fake Agent factory · bounded bridge fixtures

concurrency tests
  cancellation · race detector · slow consumers · drain

integration tests
  provider adapters · capability APIs · Kubernetes lifecycle
```

Runtime acceptance does not require a network. Service acceptance proves that remote commands and Events preserve runtime semantics.
