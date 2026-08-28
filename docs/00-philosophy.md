# Gotato Philosophy

**Status:** Draft
**Purpose:** Project constitution

> **Agent as a Service, native to Go.**
>
> **Agents are goroutines. Channels are the boundaries.**
>
> **Tight Core, Open Extensions.**

## 1. Mission

Gotato makes a stateful Agent a normal Go runtime unit that can be called from a Go program or exposed as an Agent-as-a-Service. The Agent is not a stateless completion endpoint and it is not a workflow object owned by a service.

```text
request
  ↓ channel
Agent goroutine
  ├── private state
  ├── simple Model → Tool → Model loop
  ├── explicit capabilities
  └── response / Event channels
```

An Agent can start another independent Agent goroutine. The spawned Agent communicates through channels; it does not become a child resource owned by the spawning Agent. Orchestration is another set of goroutines and channels that decides when work is admitted, queued, interrupted, or routed.

## 2. The Agent owns its local Loop

The Agent goroutine is the sole authority for its own state and execution:

```text
Agent goroutine
  ├── Agent state and transcript
  ├── current Prompt / Continue
  ├── Model and Tool loop
  ├── control messages: Steer / FollowUp / Abort
  └── canonical Events
```

An Agent processes one Prompt or Continue at a time. This is a single-flight property of the Agent goroutine, not ownership of a Conversation, Host, or orchestration system. A second request is not processed concurrently by that Agent.

The Agent does not decide what an external caller should do while it is busy. It reports execution availability through its channel/API. The caller or Orchestrator decides whether to reject, queue, prioritize, steer at a safe boundary, or abort.

## 3. Channels, not hierarchy

Communication is message-based:

```text
Orchestrator goroutine ── command channel ──► Agent goroutine
Agent goroutine         ── result channel  ──► caller
Agent goroutine         ── Event channel   ──► observer / Host
Agent A                 ── channel         ──► Agent B
```

A `spawn` relationship records origin and correlation when useful. It does not imply parent ownership, shared mutable state, automatic resource inheritance, or an execution tree. Cancellation and waiting are explicit messages or Context signals chosen by the caller or Orchestrator.

## 4. Tight Core, Open Extensions

The Core contains only the semantics every Agent needs:

```text
Agent state and transcript
Agent goroutine and canonical Loop
Model / Tool / ToolSet contracts
Extensions and local limits
Events and cancellation
```

Model providers, capability adapters, transport, orchestration, storage, and deployment are extension or surrounding layers. They connect through explicit interfaces and channels; they do not enter Core through hidden globals or copied loops.

## 5. Orchestration decides what happens next

Orchestration is not an Agent owner. It is a channel-driven coordination layer that may spawn goroutines for:

```text
request admission
per-Agent scheduling
queueing and rejection
priority and preemption policy
Agent creation and routing
remote Event forwarding
lifecycle and drain
```

For one Agent goroutine, the Orchestrator may wait for it to become Free before dispatching the next Prompt. It may instead queue incoming Prompts, reject them, send a Steer signal, or Abort the current execution. These are Host/application policies, not innate Agent behavior.

## 6. Agent-as-a-Service

Agent-as-a-Service exposes Agent goroutines through a remote channel-shaped protocol:

```text
Client
  ↓ bidirectional stream
Transport goroutines
  ↓ channels
Orchestration goroutines
  ↓ channels
Agent goroutine
```

The service provides access, admission, scheduling, and delivery. It does not create a second Agent Loop. A single-Pod deployment is enough for the initial PoC; distributed ownership is a separate future concern.

## 7. Less is more

Less is more means fewer semantic primitives, not fewer capabilities:

- no second Agent Loop
- no Core-owned user request scheduler
- no hidden global registry
- no mandatory gRPC or Kubernetes dependency
- no built-in workflow engine
- no platform-specific state in Core

The system may still have many goroutines, channels, Tools, Extensions, Providers, and Host policies. They remain outside the tight semantic Core when they do not belong to every Agent.

## 8. Review questions

1. Is this behavior intrinsic to one Agent goroutine or a caller/Host policy?
2. Can it communicate through an explicit channel or interface?
3. Does it add a second Agent Loop or mutate another Agent's private state?
4. Does it require a hierarchy, or is correlation sufficient?
5. Who decides queueing, priority, preemption, and routing?
6. Can a Go program use the Agent without deploying a platform?
7. Is this a Core semantic or an Open Extension?

## 9. Declaration

> Gotato treats an Agent as a Go-native, stateful execution unit. The Agent runs one simple Loop, communicates through channels, exposes explicit capabilities, and can be served remotely without surrendering its semantics to the service around it.
