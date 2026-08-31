# Orchestration and Hosted Agent Service

**Status:** Draft

> Orchestration turns independent Agent Cores into an addressable system; Hosted access makes that system available to remote callers.

## 1. Purpose

Gotato exposes a multi-Agent service without turning any Agent into a second runtime. Orchestration owns the external identity, handle retention, routing, admission, lifecycle, and coordination of independent Core Agents. Host and protocol adapters provide the service-facing boundary:

```text
Remote Client
      ↓ protocol adapter
Host
      ↓
Orchestration
      ↓ Agent contracts
Agent Core × N
```

The same Agent Core can also be used directly:

```text
Existing Go Service → Agent handle → Agent Core
```

Hosted mode changes access, routing, admission, Event delivery, and lifecycle. It does not change the Agent Loop or the meaning of a Run. Agent closure and Conversation retention are separate lifecycle decisions; see [Agent Lifecycle](10-agent-lifecycle.md).

## 2. What Orchestration and Host provide

Orchestration coordinates service access to multiple Agents:

```text
Conversation identity and handle retention
Agent creation, routing, and rehydration
request admission and queue policy
per-Agent dispatch and coordination
Event observation and delivery
remote cancellation, retirement, and lifecycle
```

These are coordination responsibilities, not Core configuration. Orchestration calls Agent Core through stable contracts and does not maintain a parallel transcript or Loop. A single directly held Agent may bypass this layer; a managed multi-Agent service may not.

Host wraps Orchestration with readiness, drain, and a service boundary. A protocol adapter attaches a wire protocol to Host. gRPC is a useful first adapter, but HTTP, SSE, or an existing service protocol can serve the same role.

## 3. Hosted request path

```text
wire command
      ↓ protocol adapter
semantic service command
      ↓ Host
Orchestration
  identity · route · admit · schedule · coordinate
      ↓ Agent handle
Agent Core
      ↓
canonical Events and RunResult
      ↓ Host delivery policy
wire response / Event stream
```

The adapter handles encoding, decoding, connection lifetime, and protocol errors. Orchestration selects the Agent handle and applies service policy. Core executes the command.

## 4. Service contract

A Hosted service needs a small command and Event contract. One possible first adapter is a bidirectional gRPC stream:

```proto
rpc Run(stream RunCommand) returns (stream RunEvent);
```

Conceptually:

```text
RunCommand:  Start | Steer | FollowUp | Cancel
Start:       existing ConversationID or caller-scoped ConversationKey
RunEvent:    lifecycle | Message | Tool | terminal result + ConversationID
```

The wire contract is an adapter contract. It must preserve Core identity, correlation, Event class, ordering, and settled meaning without making Protobuf types part of Core. Agent `Close`/retirement is a separate Host lifecycle operation, not an implicit consequence of closing this Run stream; its acknowledgement means Core closure, while delivery of that acknowledgement may settle later.

## 5. Command and Agent lifecycle

The protocol stream has its own lifecycle and must not be confused with the Agent lifecycle:

```text
BeforeStart ── valid Start ──► Active
BeforeStart ── other command ► protocol error
Active ────── terminal ──────► Terminal
Active ────── stream close ──► Closed delivery stream
```

`Start` contains one Prompt or Continue. Commands after terminal settlement are rejected. The adapter serializes commands in arrival order; Core decides when a command takes effect according to its control boundaries. Closing this delivery stream does not automatically close the Agent or Conversation; the Host documents whether it also requests Run cancellation.

Whether a second external request waits, queues, is rejected, or becomes a control command is Orchestration policy, possibly exposed through Host.

## 6. Orchestration and Core

A single Agent can be called directly through its handle. Multiple Agents cannot be treated as a collection of anonymous handles: Orchestration must retain or resolve those handles, route requests, and apply the coordination policy.

```text
incoming request
      ↓
Orchestration
  identity · route · admit · queue · control · aggregate
      ↓ Agent contract(s)
Agent Core × N
```

For one Agent, application code may provide this coordination directly. For dynamic, concurrent, or remote use, the Orchestration layer provides it and Host may expose it. Different Agents may execute concurrently; Core remains responsible for one current Prompt or Continue per Agent.

Orchestration owns external bounds for streams, queued requests, Agent instances, and Event delivery. Core owns bounds for one Agent's local work.

## 7. Conversation routing and retirement

Orchestration or Host may map an application key to a Conversation record and then to a live Agent handle:

```text
Agent name + conversation key
              ↓
Conversation record / routing table
              ↓
live Agent handle, or Agent definition + Core snapshot
```

This mapping is application or Orchestration state. It does not make the Agent the owner of a user account, registry, or external resource. Without a retained handle or a recoverable Conversation record, an AgentID is only an identifier and cannot restore an in-memory Agent.

A live Agent may be retired after a Run, an idle TTL, or a capacity decision. During retirement the Conversation is `Retiring` and new dispatch is rejected or retried. With retention, Orchestration persists the Core state, closes the live handle, marks the Conversation Dormant, and later creates a new AgentID on rehydration. With Ephemeral or discard policy, it closes the Agent and the Conversation is removed or marked Closed. The initial PoC may use a process-local map; cross-process continuity additionally requires a persistence and routing contract.

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
  protocol handler → Host → Orchestration → Core × N

Separate services:
  client → protocol adapter → Orchestration service
                                ↓ internal protocol adapter(s)
                           Agent Core service(s)
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

## 12. Host lifecycle and drain

```text
Serving
  ├── liveness: process is operating
  └── readiness: new requests may be admitted

Draining
  ├── readiness false
  ├── new admission rejected
  ├── Orchestration stops new Agent creation/dispatch
  ├── active Runs settle or cancel by deadline
  ├── live Agents close according to retention policy
  └── delivery bridges flush or abandon within policy
```

The infrastructure consumes these signals. It does not define Agent semantics. Process shutdown is not Conversation closure; retained Conversations may be rehydrated later when the persistence contract exists.

## 13. Deployment forms

### Embedded, single Agent

```text
Existing Go Service
  ├── business handlers
  └── Agent Core
```

### Embedded, multiple Agents

```text
Existing Go Service
  ├── business handlers
  ├── application Orchestration
  └── Agent Core × N
```

### Same-process Hosted Service

```text
Existing Go Service
  ├── protocol adapter
  ├── Host
  ├── Orchestration
  └── Agent Core × N
```

### Dedicated Hosted Service

```text
Existing Infrastructure
        ↓
Protocol Adapter → Host → Orchestration → Agent Core × N
```

All forms use the same Core Agent. The difference is where identity, access, and multi-Agent coordination live.
