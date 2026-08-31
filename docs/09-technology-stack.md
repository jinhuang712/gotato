# Technology Stack and Boundaries

**Status:** Draft

> Build a small Go-native Agent Core, attach provider and capability adapters, and host it in the platform that already runs the service.

## 1. The stack

```text
Existing Infrastructure
  Gateway · LB · Kubernetes · storage · secrets
          │ hosts / connects
          ▼
Agent Host / Orchestration (optional)
  admission · routing · coordination · lifecycle · delivery
          │ Agent contract
          ▼
Agent Core
  Go · private state · canonical Loop · Events
          │                         │
          ▼                         ▼
LLM Adapter                  Tool Adapter
          │                         │
External Model provider      Go service / application system
```

This is not a required deployment stack. An embedded Agent uses only Core and the adapters it needs. A Hosted Agent adds a Host and a protocol adapter to an existing service boundary.

## 2. Core technology

```text
Go
context.Context
small interfaces
ordinary typed errors
private goroutine-backed Agent execution
bounded local work
provider-neutral Messages and Model values
```

Core must not import Protobuf, gRPC, Kubernetes, Gateway, a database, a broker, or a provider SDK. The public Core API uses Go values and Context, not wire representations.

## 3. LLM adapters

```text
Model contract
      ▲
LLM Adapter
  provider protocol · authentication · usage · provider policy
      ▲
external provider
```

An LLM Adapter turns a provider API into the Core Model contract. It owns provider-specific encoding, streaming, authentication, and connection retry. Core owns the Agent Loop, Model call boundaries, transcript commitment, and Run settlement.

The adapter interface must remain provider-neutral so an existing service can change providers without changing its Agent code.

## 4. Tool adapters

```text
Go function / service / external system
                 ↓
             Tool Adapter
                 ↓
             Core Tool contract
```

A simple Go function should be easy to expose as a Tool. Service-backed Tools may use an existing HTTP, gRPC, RPC, database, or MCP client. The adapter owns external authentication, protocol details, and resource policy; Core owns Tool invocation and result commitment.

## 5. Orchestration and protocol adapters

Orchestration is a small coordination layer for Hosted use:

```text
Agent Factory
Agent routing
admission and queue policy
lifecycle and cancellation
Event observation and delivery
```

A protocol adapter connects a client to this layer:

```text
HTTP / gRPC / SSE / existing RPC
             ↓
      Host / Orchestration
```

The protocol adapter maps wire commands and Events. It is optional for Embedded use and is not a dependency of Core. When Orchestration and Core share a process, a direct Go interface or channel-backed handle is the simplest connection. When they have a deliberate process boundary, internal gRPC is a valid adapter.

## 6. Infrastructure integration

Infrastructure is external:

```text
Gateway / Ingress
Kubernetes / container runtime
load balancing and session affinity
identity and secrets
persistent stores
resource limits and autoscaling
```

Gotato provides integration contracts such as Context propagation, long-lived stream behavior, readiness, liveness, and drain. It does not provide or require an Infrastructure product.

The initial Hosted PoC may use one Host process, one Pod, local routing, and an existing HTTP or gRPC server. Multi-Pod continuity is a future routing and persistence contract.

## 7. Observability

Core and Host may expose:

```text
Agent ID · Run ID · Turn · Tool Call ID · Spawn ID
request ID · stream ID · terminal status
```

Observability uses Go interfaces or OpenTelemetry adapters. It must not force raw prompts, provider payloads, or credentials into logs, and it must not change Agent execution semantics.

## 8. Package direction

A possible layout is:

```text
agent/             small public Agent interface and Core implementation
model/             provider-neutral Model values and contract
adapter/llm/       provider integrations
adapter/tool/      application capability integrations
host/              optional Orchestration and lifecycle
adapter/protocol/   optional protocol adapters used by Host
```

The package names may evolve. The dependency direction must not:

```text
LLM / Tool adapters → Core contracts
Host / protocol adapters → Host / Core contracts
Infrastructure hosts everything from outside
```

## 9. Testing

Core tests use deterministic Models, Tools, Contexts, and clocks without a network. Host tests exercise admission, routing, cancellation, delivery, and drain in-process. Real provider and platform tests are integration suites; they are not prerequisites for Core acceptance.
