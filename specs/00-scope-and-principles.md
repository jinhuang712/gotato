# 00. Scope and Principles

**Status:** Draft

> Phase one delivers a complete embedded Go Agent kernel. Phase two exposes that kernel as a standard service.

## 1. Phase-one kernel

```text
Prompt or Continue
  → streamed Model response
  → zero or more Tool Calls
  → Tool Result Messages
  → another Turn or completion
```

The kernel MUST run in one process with in-memory state and ordinary Go interfaces.

## 2. Required kernel behavior

Phase one MUST provide:

```text
stateful Agent
one active Run per Agent
Prompt and Continue
Model streaming and Message assembly
Tool Call assembly and Schema validation
Pre-Tool-Use and Post-Tool-Use
sequential and bounded parallel Tool execution
Tool progress and result Events
Steering and Follow-up
context.Context cancellation
local limits and stable errors
Tool and staged ToolSet composition
small Extension interfaces
Agent Routine spawn and bounded groups
deterministic test fakes
```

## 3. Phase-two service

Phase two MUST provide:

```text
Agent factory
in-service Agent cache
service preset
gRPC service definition
Go gRPC server and client
Event streaming
remote cancellation
admission bounds
readiness and graceful drain
Kubernetes deployment baseline
```

The service layer MUST invoke the canonical kernel API.

## 4. Layer ownership

```text
Core          Agent state and Model/Tool execution
ToolSet       capability composition and discovery
Extension     behavior at explicit lifecycle hooks
Agent Routine managed child Agent execution
Adapter       provider and protocol translation
Service       remote access and process lifecycle
Application   business meaning and presentation
Deployment    cluster resources and operational policy
```

## 5. Core admission rule

A capability belongs in Core when it:

1. is required for correct Model/Tool execution;
2. has semantics shared by every Agent;
3. can be implemented without infrastructure dependencies;
4. has deterministic acceptance tests.

Agent Routines form a focused composition package on top of Core Agent and Run contracts.

## 6. Dependencies

Core packages MUST depend only on the Go standard library and deliberately selected small foundational libraries. Provider SDKs, transport frameworks, databases, and Kubernetes clients belong to adapters and service packages.

## 7. Canonical control flow

The repository MUST contain one canonical Agent loop. Embedded APIs, Agent Routines, service presets, transports, and Extensions compose around that loop.
