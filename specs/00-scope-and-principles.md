# 00. Scope and Principles

**Status:** Draft

> The deliverable is a callable Agent service. The structure beneath it is a transport-independent runtime that the service invokes and never replaces.

## 1. Two directions

Discovery and dependency point opposite ways, and both are deliberate.

```text
Discovery
  service use case → required semantics → stable runtime contract

Dependency
  gRPC transport → service layer → runtime kernel
```

A working service reveals what callers need from Agent identity, conversation continuity, commands, Events, cancellation, errors, admission, and lifecycle. Contracts derived that way are grounded in observed use.

The runtime MUST remain independent of the technologies used to expose it. It MUST NOT depend on Protobuf, gRPC, Agent caches, process hosting, or cluster APIs.

## 2. Required runtime capability

The runtime kernel MUST provide:

```text
stateful Agent with one active mutating Run
Prompt and Continue
Model streaming and Message assembly
Tool Call assembly and Schema validation
Pre-Tool-Use and Post-Tool-Use
sequential and bounded parallel Tool execution
Tool and Steering progress Events
Steering and Follow-up
context.Context cancellation reaching every owned operation
local limits and stable typed errors
Tool and staged ToolSet composition
focused Extension interfaces
Agent Routine spawn and bounded groups
deterministic test fakes
```

## 3. Required service capability

The service MUST provide:

```text
named Agent definitions and factories
conversation-scoped Agent resolution
bounded in-process Agent cache
Run admission
Protobuf service contract
gRPC server and Go client
bounded Event delivery with a stated slow-consumer policy
remote cancellation
readiness and graceful drain
Kubernetes deployment baseline
```

The service MUST invoke the canonical runtime API. It MUST NOT maintain a second Agent state machine.

## 4. Layer ownership

```text
Runtime Kernel  Agent state · Model/Tool execution · canonical Events · limits
ToolSet         capability composition and staged discovery
Extension       behavior at explicit lifecycle stages
Agent Routine   managed child Agent execution
Adapter         provider and capability translation
Service         remote access · Agent lifecycle · admission · delivery
Transport       wire encoding and stream lifetime
Application     business meaning and presentation
Deployment      cluster resources and operational policy
```

## 5. One canonical loop

The repository MUST contain exactly one Agent loop. The gRPC service, a direct Go caller, and every Agent Routine MUST converge on it.

A transport handler, an Agent cache, or a Routine executor MUST NOT reproduce loop behavior.

## 6. One terminal Event

A Run MUST emit exactly one terminal Event. Nothing resumes execution after it.

Automatic retry after a transient Model failure, context compaction, and continuation for queued Steering or Follow-up MUST occur inside the Run:

```text
Model failure
      ↓
retry inside the Run
      ↓
      ...
      ↓
terminal Event      the only completion signal
```

An orchestration layer above the loop that re-invokes the runtime after completion is out of scope. Such a design forces every client to learn that the first completion signal is not authoritative, which a cross-language contract cannot express safely.

## 7. Two settlements

Execution and delivery settle independently.

```text
Execution settlement   the Run owns no further work        Runtime
Delivery settlement    the consumer has all it will get    Service
```

Neither MUST block indefinitely on the other. A slow or disconnected consumer MUST NOT hold a Run open without bound, and a completed Run MUST NOT be reported as delivered before its consumer has received anything.

## 8. Core admission rule

A capability belongs in the runtime kernel when it:

1. is required for correct Model/Tool execution;
2. has semantics shared by every Agent;
3. is meaningful without a network or process host;
4. is reachable through ordinary Go values;
5. has deterministic acceptance tests that need no provider or transport.

Agent Routines form a focused composition package over the Agent and Run contracts rather than a second execution model.

## 9. Dependencies

Runtime packages MUST depend only on the Go standard library and deliberately selected small foundational libraries. Provider SDKs, transport frameworks, databases, and cluster clients belong to adapter and service packages.

## 10. Presentation boundary

Gotato publishes Go APIs, a Protobuf contract, Events, examples, and diagnostic utilities. End-user CLI, TUI, web, and chat experiences belong to applications.
