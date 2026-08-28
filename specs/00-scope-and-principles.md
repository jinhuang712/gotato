# 00. Scope and Principles

**Status:** Draft

> Agent Core is the stable execution boundary. Hosted orchestration is an optional composition around it.

## 1. Deliverables

The repository defines a Core-first runtime and an optional hosted composition:

```text
Agent Core library
Model, Tool, ToolSet, Extension, and Routine contracts
canonical Events and local subscriptions
optional Orchestrator / Agent Host
optional gRPC transport adapter and Go client
service admission, conversation ownership, and bounded delivery
integration guidance for existing Gateway and Kubernetes platforms
Model Router and provider/capability adapter boundaries
```

A regular Go service may consume only the Core. A Hosted deployment may consume all layers. The project does not claim novelty for the basic Agent loop, provider abstraction, or generic distributed hosting; the normative value is in the explicit contracts and their composition.

## 2. Boundary rules

The Core MUST be self-contained and transport-independent. Core packages MUST NOT depend on:

```text
Protobuf or gRPC
Gateway or Kubernetes
Agent caches or service admission
databases or process-hosting APIs
provider SDKs
```

The Host and Transport MUST call Core operations rather than copy its state machine. Infrastructure MUST route and host processes without defining Agent semantics.

The Core boundary MUST exist from the first implementation. Public package release can be staged; Core independence and testability cannot be postponed.

## 3. Required Core capability

Core MUST provide:

```text
stateful Agent with one active mutating Run
Prompt and Continue
Model stream and Message assembly
Tool Call assembly and Schema validation
Pre-Tool-Use and Post-Tool-Use
sequential and bounded parallel Tools
ToolSet composition and staged activation
Steering and Follow-up
Context cancellation and local limits
canonical Events and terminal settlement
focused Extensions
Agent Routine spawn and bounded groups
deterministic test fakes
```

## 4. Optional Host capability

The Host MUST provide these capabilities when Hosted mode is selected:

```text
named Agent definitions and factories
per-Host admission and concurrency bounds
conversation ownership and optional cache/lease
transport stream attachment
canonical Event projection and bounded delivery
remote cancellation
readiness and graceful drain
```

The Host MUST NOT be required for Embedded mode.

## 5. Two concurrency domains

Core guarantees one state owner per Agent. Host coordinates multiple Agents and Runs:

```text
Host:  multiple streams and Runs under admission bounds
Agent: one active mutating Run
Run:   bounded Tool and Routine concurrency
```

Infrastructure replica count is not a substitute for Host admission or Core limits.

## 6. One canonical loop

The repository MUST contain exactly one Agent loop. Embedded callers, Hosted callers, and child Routines MUST converge on it. No handler, cache, Gateway, or Routine executor may reproduce Model/Tool execution.

## 7. One terminal Event

A Run MUST emit exactly one terminal `agent_end` Event. Retry, context compaction, and queued continuation happen before it inside the same Run. Nothing starts after it.

## 8. Settlement

Execution settlement belongs to Core. Delivery settlement belongs to Host. Neither may wait indefinitely on the other. A disconnected consumer MUST NOT create unbounded Core work.

## 9. Model and infrastructure boundaries

Core consumes a provider-neutral Model contract. Routing, provider selection, fallback, and provider SDKs belong to the Model layer. Gateway, load balancing, Kubernetes, storage, and credentials belong to Infrastructure.

## 10. Presentation

Applications own business workflows and end-user CLI, TUI, Web, and chat interfaces. Gotato publishes Core APIs, Hosted protocols, Events, examples, and diagnostics.
