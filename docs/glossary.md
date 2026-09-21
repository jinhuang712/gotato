# Glossary

**Status:** Draft

This glossary fixes the minimal set of terms used across the architecture documents and specifications. `docs/` explains why they fit together; `specs/` defines their behavior.

## Core terms

### Agent

A callable, self-contained, stateful Go runtime unit. An Agent accepts a Prompt or Continue, runs its Model and Tools, and returns a result or stream through a small interface. Self-contained means that it owns its private state and current work; Model and Tool adapters may remain external.

### Agent Core

The Go-native runtime behind an Agent. Core owns the current conversation state, canonical Model → Tool → Model Loop, Tool invocation, cancellation, local limits, Events, and result settlement.

### Agent lifecycle

The lifetime of one live Core execution unit: `Created`, `Idle`, `Busy`, `Closing`, and `Closed`. Run settlement does not close an Agent. An explicit close does.

### Agent handle

The safe callable reference to a live Agent Core. An `AgentID` identifies the execution unit but is not itself a handle, locator, or recovery record.

### Session

A first-class runtime primitive: the record of what happened across Turns and Runs. It holds identity, the committed Messages, per-Run records, usage, a bounded window of runtime Events, compaction history, and free-form application `Metadata`. The Session is the unit of identity and continuity; a live Agent is an optimization that serves Runs, never an identity. Persisted by a `session.Store`.

### Session Store

The pluggable storage contract for Sessions (`session.Store`), with in-memory, file-backed, and application-provided implementations (DESIGN G-D05). Because continuity lives in the Store, any process holding it can serve any Session.

### Session Fork

`session.Fork` creates a new Session from an existing Session's state: it copies Messages, Usage, Compactions, and Metadata and records the parent Session ID. It is a state operation, not an agent hierarchy operation; derived work is a Fork plus another Run, and parentage is data lineage (DESIGN G-D06).

### Session state

The committed Messages and local execution state needed for an Agent's current work. Core may keep this state in memory. It is not a long-term Memory product.

### Work

The private state and currently accepted Run owned by an Agent. Work does not include a service or Host request queue, routing table, or admission policy.

### Run

One accepted Prompt or Continue processed by an Agent. A Run has an identity, Context, Event sequence, and settled result.

### Turn

One Model request and the Tool batch produced by that response. A Turn ends after its assistant Message, Tool outcomes, and Tool Result Messages are committed.

### Agent Routine

The internal running form of an Agent: its private execution unit, state boundary, and result/Event boundary. A Routine may use one goroutine. A Routine created by another Routine is independent of it.

## Capability terms

### Model

A provider-neutral Core contract for normalized Model responses and streams. Provider protocol, authentication, and provider policy belong to an LLM Adapter.

### LLM Adapter

The adapter that converts a Model provider's API into the Core Model contract. It owns provider-specific encoding, streaming, authentication, usage, and provider errors.

### Tool

One model-callable operation with a stable identity, validated arguments, bounded execution, and a committed Tool Result.

### Tool Adapter

The adapter that connects a Go function, service, or external system to the Core Tool contract. It owns external protocol, authentication, and resource policy.

### ToolSet

A named group of related Tools that can be activated and exposed in deterministic order. ToolSets are optional for the minimal Agent path.

### Extension

An explicit component installed at a named Core stage, such as context transformation, Message conversion, Tool interception, Event observation, or Turn stopping. An Extension cannot directly take over Agent state.

### Event

An immutable fact emitted by Core for a committed transition or declared operation. Events carry identity, order, correlation, class, and settled meaning.

## Service terms

### Orchestration

The coordination responsibility that creates and routes multiple Agents, applies admission and queue policy, manages lifecycle, and coordinates Event delivery. It may be ordinary application code or the optional Gotato Orchestration package; it is not needed for one directly held Agent, but it is unavoidable when multiple Agents must be found or coordinated. Orchestration coordinates; Core executes.

### Host

The service-facing composition around Orchestration. A Host combines Orchestration with protocol adapters, remote access, cancellation mapping, readiness, and drain. Host is optional for direct single-Agent use, but Hosted multi-Agent access requires an Orchestration responsibility behind it.

### Protocol adapter

The boundary adapter that maps wire commands and Events to the Host interface and its Orchestration. HTTP, gRPC, SSE, and an existing RPC protocol may implement the same semantic contract. A protocol adapter is not a Core layer.

### Infrastructure

The existing environment that hosts and connects processes, such as a Go service, Gateway, Kubernetes, load balancer, storage, and secrets. Infrastructure is outside Gotato's implementation scope.

### Retirement (historical)

Removed. The current model has no retirement: an Agent is created for a Run and discarded when it settles, while the Session persists ([PROPOSAL.md §5a](../PROPOSAL.md); [MIGRATION.md](../MIGRATION.md)). An explicit `Close` still releases a directly held Agent. Retained here only for the historical record.

### Hosted Agent Service

An Orchestration managing one or more Agent Cores exposed through a Host and a protocol adapter. Hosted access changes how callers reach and coordinate Agents; it does not create a second Agent implementation.
