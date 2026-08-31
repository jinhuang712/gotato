# Glossary

**Status:** Draft

This glossary fixes the minimal set of terms used across the architecture documents and specifications. `docs/` explains why they fit together; `specs/` defines their behavior.

## Core terms

### Agent

A callable, self-contained, stateful Go runtime unit. An Agent accepts a Prompt or Continue, runs its Model and Tools, and returns a result or stream through a small interface. Self-contained means that it owns its private state and current work; Model and Tool adapters may remain external.

### Agent Core

The Go-native runtime behind an Agent. Core owns the current conversation state, canonical Model → Tool → Model Loop, Tool invocation, cancellation, local limits, Events, and result settlement.

### Agent lifecycle

The lifetime of one live Core execution unit: `Created`, `Idle`, `Busy`, `Closing`, and `Closed`. Run settlement does not close an Agent. Explicit close or an owner-selected retirement policy does.

### Agent handle

The safe callable reference to a live Agent Core. An `AgentID` identifies the execution unit but is not itself a handle, locator, or recovery record.

### Conversation

An Orchestration-owned, addressable application thread. Its stable identity may outlive the live Agent that currently serves it. During retained retirement it may be `Active`, `Retiring`, or `Dormant`; a business close moves it to `Closed`. A retained Conversation needs an Agent definition and recoverable Core state to rehydrate after retirement or restart.

### Conversation state

The committed Messages and local execution state needed for an Agent's current conversation. Core may keep this state in memory. It is not a long-term Memory product.

### Work

The private state and currently accepted Run owned by an Agent. Work does not include an Orchestration or Host request queue, routing table, admission policy, or retirement policy.

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

The coordination responsibility that creates and routes multiple Agents, applies admission and queue policy, manages lifecycle, and coordinates Event delivery. It may be ordinary application code or the optional Gotato Orchestration package; it is not needed for one directly held Agent, but it is unavoidable when multiple Agents must be found or coordinated. Orchestration coordinates; Core executes.

### Host

The service-facing composition around Orchestration. A Host combines Orchestration with protocol adapters, remote access, cancellation mapping, readiness, and drain. Host is optional for direct single-Agent use, but Hosted multi-Agent access requires an Orchestration responsibility behind it.

### Protocol adapter

The boundary adapter that maps wire commands and Events to the Host interface and its Orchestration. HTTP, gRPC, SSE, and an existing RPC protocol may implement the same semantic contract. A protocol adapter is not a Core layer.

### Infrastructure

The existing environment that hosts and connects processes, such as a Go service, Gateway, Kubernetes, load balancer, storage, and secrets. Infrastructure is outside Gotato's implementation scope.

### Retirement

The owner-directed process of closing a live Agent after a Run, idle period, capacity decision, or explicit request. Retirement may preserve the Conversation as `Dormant` or discard it according to policy; it is not the same as Run settlement.

### Hosted Agent Service

An Orchestration managing one or more Agent Cores exposed through a Host and a protocol adapter. Hosted access changes how callers reach and coordinate Agents; it does not create a second Agent implementation.
