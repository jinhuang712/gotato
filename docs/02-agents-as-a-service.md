# Hosted Agent Service

**Status:** Draft

> The Hosted form gives remote callers the same Agent Core that Go services can use locally.

## 1. Purpose

Gotato can expose an Agent as a Service without turning the Agent into a second runtime. The Host is a thin composition around Agent Core:

```text
Remote Client
      ↓ protocol adapter
Agent Host / Orchestration
      ↓ Agent contract
Agent Core
```

The same Agent can also be used directly:

```text
Existing Go Service → Agent Core
```

Hosted mode changes access, routing, admission, Event delivery, and lifecycle. It does not change the Agent Loop or the meaning of a Run.

## 2. What the Host provides

The Host coordinates service access to Agents:

```text
Agent creation and routing
request admission and queue policy
per-Agent dispatch
Event observation and delivery
remote cancellation
readiness and drain
```

These are Host responsibilities, not Core configuration. The Host calls Agent Core through a stable semantic interface and does not maintain a parallel transcript or Loop.

A protocol adapter attaches a wire protocol to this interface. gRPC is a useful first adapter, but HTTP, SSE, or an existing service protocol can serve the same role.

## 3. Hosted request path

```text
wire command
      ↓ protocol adapter
semantic Agent command
      ↓ Host / Orchestration
Agent handle
      ↓
Agent Core
      ↓
canonical Events and RunResult
      ↓ Host delivery policy
wire response / Event stream
```

The adapter handles encoding, decoding, connection lifetime, and protocol errors. Orchestration handles Agent selection, admission, scheduling, and lifecycle. Core executes the command.

## 4. Service contract

A Hosted service needs a small command and Event contract. One possible first adapter is a bidirectional gRPC stream:

```proto
rpc Run(stream RunCommand) returns (stream RunEvent);
```

Conceptually:

```text
RunCommand:  Start | Steer | FollowUp | Cancel
RunEvent:    lifecycle | Message | Tool | terminal result
```

The wire contract is an adapter contract. It must preserve Core identity, correlation, Event class, ordering, and settled meaning without making Protobuf types part of Core.

## 5. Command lifecycle

```text
BeforeStart ── valid Start ──► Active
BeforeStart ── other command ► protocol error
Active ────── terminal ──────► Terminal
Active ────── stream close ──► Closed
Terminal ──── delivery done ─► Closed
```

`Start` contains one Prompt or Continue. Commands after terminal settlement are rejected. The adapter serializes commands in arrival order; Core decides when a command takes effect according to its control boundaries.

Whether a second external request waits, queues, is rejected, or becomes a control command is Host policy.

## 6. Orchestration and Core

Orchestration may coordinate many Agents:

```text
incoming request
      ↓
Host policy
  route · admit · queue · control
      ↓ Agent contract
Agent Core
```

For one Agent, the Host can reject while Busy, queue the request, or dispatch a control message. Different Agents may execute concurrently. Core remains responsible for one current Prompt or Continue per Agent.

The Host owns external bounds for streams, queued requests, Agent instances, and Event delivery. Core owns bounds for one Agent's local work.

## 7. Conversation routing

The Host may map an application key to an Agent handle:

```text
Agent name + conversation key
              ↓
Host routing table
              ↓
Agent handle
```

This mapping is application or Host state. It does not make the Agent the owner of a user account, registry, or external resource.

The initial PoC may use a process-local map. Cross-process or multi-Pod continuity is a separate routing and persistence contract, not a requirement for the first Hosted service.

## 8. Event delivery

Core produces canonical Events. The Host may project them for a remote client:

```text
Core Event
    ↓
projection / redaction
    ↓ bounded delivery
remote Event
```

A delivery bridge must be bounded and must preserve protected lifecycle and outcome Events. Optional progress may be coalesced. Execution settlement belongs to Core; delivery settlement belongs to the Host.

A slow client must not create an unbounded queue or hold unrelated Agent work open.

## 9. Cancellation

```text
client Cancel / stream Context / deadline
                  ↓
            Host policy
                  ↓
             Core Abort
```

The Host documents whether closing an attached stream also cancels the Run. Explicit cancellation reaches the current Model, Tools, Extensions, and local work through the Agent boundary. Spawn provenance does not imply cancellation ownership.

## 10. Protocol adapters

A protocol adapter may be in the same process as the Host or in a separate service:

```text
Same process:
  protocol handler → Host interface → Core

Separate services:
  client → protocol adapter → Orchestration service
                                ↓ internal protocol adapter
                           Agent Core service
```

An internal gRPC call is reasonable when a process boundary, independent deployment, or a standard service contract is useful. When components share a process, a direct Go interface or channel-backed handle is the simpler implementation. Both forms preserve the same semantic Host and Agent contracts.

## 11. Infrastructure relationship

Infrastructure surrounds the Host:

```text
Gateway / LB / Kubernetes / existing Go service
                  ↓
             Gotato Host
```

Gotato does not implement or require a Gateway, Kubernetes, load balancer, broker, service registry, database, or secrets platform. It provides integration points such as readiness, liveness, drain, Context propagation, and long-lived stream requirements.

An existing Go service may host the Agent Core and Host beside its own APIs.

## 12. Lifecycle

```text
Serving
  ├── liveness: process is operating
  └── readiness: new requests may be admitted

Draining
  ├── readiness false
  ├── new admission rejected
  ├── queued requests handled by Host policy
  ├── active Runs settle or cancel by deadline
  └── delivery bridges flush or abandon within policy
```

The infrastructure consumes these signals. It does not define Agent semantics.

## 13. Deployment forms

### Embedded Agent

```text
Existing Go Service
  ├── business handlers
  └── Agent Core
```

### Same-process Host

```text
Existing Go Service
  ├── protocol adapter
  ├── Agent Host / Orchestration
  └── Agent Core
```

### Dedicated Host

```text
Existing Infrastructure
        ↓
Protocol Adapter → Host / Orchestration → Agent Core
```

All three forms use the same Core Agent. The difference is where access and coordination live.
