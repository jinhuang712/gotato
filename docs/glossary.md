# Glossary

**Status:** Draft

This glossary fixes the terms used across the architecture documents and specifications. `docs/` explains why the terms fit together; `specs/` defines their normative behavior.

## Runtime terms

### Agent

A stateful Go runtime unit with private state, explicit capabilities, and one running execution boundary. An Agent processes one Prompt or Continue at a time.

### Agent Core

The Go-native runtime that provides Agent state, the canonical Model → Tool → Model Loop, capabilities, Events, cancellation, and local limits. Agent Core does not provide transport, hosting, or an external request queue.

### Agent Routine

The running form of an Agent: one Agent identity, one goroutine, private state, command channels, result channels, and an Event channel. A spawned Routine is independent of the Routine that created it.

### Work

The private state and currently accepted Run owned by an Agent. Work does not include a Host's request queue, Conversation routing, or admission policy.

### Run

One accepted Prompt or Continue processed by an Agent Routine. A Run has its own identity, Context, Event sequence, and settled RunResult.

### Turn

One Model request and the Tool batch produced by that response. A Turn ends after its assistant Message, Tool outcomes, and Tool Result Messages are committed.

### Conversation

An application or Host routing key used to find an Agent. A Conversation is not Agent state and is not owned by the Agent.

## Capability terms

### Model

A provider-neutral Core contract that returns a normalized stream of text, reasoning, Tool Calls, usage, completion, or failure Events. Provider selection and SDK details stay in adapters.

### Tool

One model-callable operation with a stable identity, validated arguments, bounded execution, and a committed Tool Result.

### ToolSet

A named group of related Tools that can be activated and exposed in a deterministic order. ToolSets support staged capability discovery.

### Extension

An explicit component installed at a named Core stage, such as context transformation, message conversion, Tool interception, Event observation, or Turn stopping. Extensions cannot directly mutate Agent state.

## Layer terms

### Orchestration

The coordinating layer that admits requests, applies queue and preemption policy, creates and routes Agent Routines, forwards Events, and manages lifecycle. Orchestration coordinates; it does not execute the Agent Loop.

### Host

The optional service composition around Agent Core. A Host combines Orchestration with transport-facing streams, admission, delivery, cancellation, readiness, and drain.

### Transport

The protocol adapter that maps wire commands and projected Events. gRPC, Protobuf, HTTP, and SSE belong here, not in Agent Core.

### Infrastructure

The external environment that hosts and routes processes, such as gateways, Kubernetes, load balancers, storage, and secrets. Infrastructure does not define Agent semantics.

### Event

An immutable runtime fact emitted by an Agent Routine. Core Events carry identity, order, correlation, class, and settled meaning. A Host may project them for remote delivery without creating a second Event history.
