# Orchestration and Hosted Agent Service

**Status:** Superseded

> **Superseded (see [PROPOSAL.md §5a](../PROPOSAL.md) and [MIGRATION.md](../MIGRATION.md)).** The `orchestration` and `host` packages and the Conversation/retirement service model described here were removed. The service is now `service.Runner` (a Session store plus Agents created per Run), with `service/httpapi` over HTTP (wire contract 2) and `adapter/grpc` over gRPC (`gotato.v2.SessionService`). The unit of identity is the Session ID; there are no Conversation keys, agent generations, retirement, or spawn groups. Derived work is `session.Fork` plus another Run, with lineage in `Session.Metadata`. Read the rest of this file as the historical design record only.

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

Hosted mode changes access, routing, admission, Event delivery, and lifecycle. It does not change the Agent Loop or the meaning of a Run. Agent closure and Session continuity are separate decisions; the Session outlives the Agent that served a Run (see [Agent Lifecycle](10-agent-lifecycle.md), superseded).

## 2. What Orchestration and Host provide

Orchestration coordinates service access to multiple Agents:

```text
Session identity and handle retention
Agent creation and routing
request admission and queue policy
per-Session dispatch and coordination
Event observation and delivery
remote cancellation and lifecycle
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
Start:       existing Session ID
RunEvent:    lifecycle | Message | Tool | terminal result + Session ID
```

The wire contract is an adapter contract. It must preserve Core identity, correlation, Event class, ordering, and settled meaning without making Protobuf types part of Core. Agent `Close` is a separate Host lifecycle operation, not an implicit consequence of closing this Run stream; its acknowledgement means Core closure, while delivery of that acknowledgement may settle later.

## 5. Command and Agent lifecycle

The protocol stream has its own lifecycle and must not be confused with the Agent lifecycle:

```text
BeforeStart ── valid Start ──► Active
BeforeStart ── other command ► protocol error
Active ────── terminal ──────► Terminal
Active ────── stream close ──► Closed delivery stream
```

`Start` contains one Prompt or Continue. Commands after terminal settlement are rejected. The adapter serializes commands in arrival order; Core decides when a command takes effect according to its control boundaries. Closing this delivery stream does not automatically close the Agent; the Host documents whether it also requests Run cancellation.

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

## 7. Session routing and continuity

> **Superseded:** `ConversationStatus` (`Active`/`Retiring`/`Dormant`/`Closed`), `ConversationID`/`ConversationKey`, `AgentGeneration`, and retirement/rehydration are not runtime concepts. A service maps a Session ID to a Session in a `session.Store` and creates a disposable Agent for each Run; derived work is `session.Fork` plus another Run ([PROPOSAL.md §5a](../PROPOSAL.md); [MIGRATION.md](../MIGRATION.md)).

A service layer routes by Session ID to a stored Session, then to a live Agent handle if one currently serves a Run:

```text
Session ID
     ↓
session.Store (committed Session state)
     ↓
live Agent handle created for a Run, if present
```

This mapping is application or service state. It does not make the Agent the owner of a user account, registry, or external resource. An `AgentID` is only an identifier and cannot restore an in-memory Agent; continuity comes from the Session in the Store. Cross-process continuity requires a shared Store, not a process-local routing table.

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

The Host documents whether closing an attached stream also cancels the Run. Explicit cancellation reaches the current Model, Tools, Extensions, and local work through the Agent boundary. There is no runtime Spawn type; any correlation between Agents is application metadata and does not imply cancellation ownership.

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
  ├── live Agents close according to host policy
  └── delivery bridges flush or abandon within policy
```

The infrastructure consumes these signals. It does not define Agent semantics. Process shutdown is not Session deletion; a Session persisted in a `session.Store` may be served later by a new Agent.

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
