# Glossary

**Status:** Draft

This glossary fixes the minimal set of terms used across the architecture documents and specifications. `docs/` explains why they fit together; `specs/` defines their behavior.

## Core terms

### Agent

A callable, self-contained, stateful Go runtime unit. An Agent accepts a Prompt or Continue, runs its Model and Tools, and returns a result or stream through a small interface. Self-contained means that it owns its private state and current work; Model and Tool adapters may remain external.

### Agent Core

The Go-native runtime behind an Agent. Core owns the current conversation state, canonical Model → Tool → Model Loop, Tool invocation, cancellation, local limits, Events, and result settlement.

### Conversation state

The committed Messages and local execution state needed for an Agent's current conversation. Core may keep this state in memory. It is not a long-term Memory product.

### Work

The private state and currently accepted Run owned by an Agent. Work does not include a Host's request queue, routing table, or admission policy.

### Run

One accepted Prompt or Continue processed by an Agent. A Run has an identity, Context, Event sequence, and settled result.

### Turn

One Model request and the Tool batch produced by that response. A Turn ends after its assistant Message, Tool outcomes, and Tool Result Messages are committed.

### Agent Routine

The internal running form of an Agent: its private execution unit, state boundary, and result/Event boundary. A Routine may use one goroutine. A spawned Routine is independent of the Routine that created it.

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

The optional coordinating layer that creates and routes Agents, applies admission and queue policy, manages lifecycle, and coordinates Event delivery. Orchestration coordinates; Core executes.

### Host

The optional service composition around Agent Core. A Host combines Orchestration with protocol adapters, remote access, cancellation mapping, readiness, and drain. Orchestration is the coordination responsibility; Host is the composition that exposes and operates it.

### Protocol adapter

The boundary adapter that maps wire commands and Events to the Host or Orchestration interface. HTTP, gRPC, SSE, and an existing RPC protocol may implement the same semantic contract. A protocol adapter is not a Core layer.

### Infrastructure

The existing environment that hosts and connects processes, such as a Go service, Gateway, Kubernetes, load balancer, storage, and secrets. Infrastructure is outside Gotato's implementation scope.

### Hosted Agent Service

An Agent Core exposed through a Host and an optional protocol adapter. Hosted access changes how callers reach and coordinate an Agent; it does not create a second Agent implementation.
