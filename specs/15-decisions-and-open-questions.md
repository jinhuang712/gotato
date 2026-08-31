# 15. Decisions and Open Questions

**Status:** Draft

> **Go-native Agent Runtime and Orchestration.**

> Gotato turns a self-contained Agent into an embeddable execution unit and, when needed, an addressable multi-Agent service.

## 1. Decisions

### Product shape

1. Gotato provides an atomic Go-native Agent Core and a first-class Orchestration path for managed multi-Agent use. Hosted Agent Service is the service-facing form of that path.
2. An Agent is a callable, goroutine-backed stateful execution unit with private state.
3. A single Agent may be used through a retained handle; multiple Agents require Orchestration to retain, address, route, and coordinate those handles. Hosted access does not create a second Agent implementation.
4. Infrastructure is external and replaceable. Gateway, Kubernetes, load balancing, storage, and secrets are integration choices, not Gotato products.
5. Applications own business meaning, presentation, and may implement Orchestration for fixed Embedded Agent sets; Gotato provides the reusable Orchestration path.

### Core semantics

6. The Agent goroutine is the only authority for its private state and canonical Loop.
7. One Agent goroutine processes one Prompt or Continue at a time.
8. Core does not own the external Prompt queue. Application Orchestration or Host decides reject, queue, priority, Steer, Abort, or creation of another Agent routine.
9. There is exactly one canonical Agent Loop for all Agent goroutines.
10. Prompt, Continue, Tool, Extension, and control messages converge on that Loop.
11. Prompt and Continue return a settled Core RunResult; Events are published through the Agent Event boundary.
12. Core uses explicit provider-neutral Model streams and Tool contracts.
13. Core fixes state transitions, assembly, validation, commitment, Event order, cancellation, local limits, and terminal settlement.
14. Every Run emits exactly one terminal `agent_end`; nothing starts after it.
15. Tool executor invocation is at most once per ToolUse; explicit retry creates a new identity.
16. Tool failures become Tool Results when the current Agent can continue; blocking Extension and protocol failures terminate the current Run.

### Agent routines and channels

17. An Agent Routine is the running goroutine-backed form of an Agent, not a wrapper around a child Agent Run.
18. A spawned Agent is an independent Agent routine with private state and channels.
19. Spawn provenance may be correlated by IDs but does not create resource ownership, shared mutable state, or automatic cancellation inheritance.
20. Agent-to-Agent and Agent-to-Orchestration communication uses explicit channels or channel-backed handles.
21. A failure or cancellation in one independent Agent does not automatically terminate another Agent.

### Orchestration, Host, and protocol adapters

22. Orchestration manages Agent identity, handle retention, creation, admission, external request queues, dispatch, routing, multi-Agent coordination, and lifecycle. Host adds the service-facing boundary, remote delivery, and protocol attachment.
23. Orchestration, Host, and protocol adapters must not duplicate Core state or Loop behavior.
24. gRPC is one optional protocol adapter for Hosted use, not a Core dependency.
25. Protected Events cannot be silently dropped; remote progress may be coalesced under bounds.
26. Execution settlement and remote delivery settlement are independent.
27. A Host may treat attached stream closure as Run cancellation, but the policy must be explicit.
28. The initial PoC is single-Pod. Ordinary Kubernetes load balancing does not guarantee Conversation continuity across Pods.

### Model and capabilities

29. Model Router and provider adapters own provider selection and provider-specific policy.
30. Tools, ToolSets, and Extensions are explicit Agent capabilities.
31. Agent Routines and spawned Agents use the canonical Loop and channel-backed coordination.
32. A single Agent can be used through a retained handle without Orchestration. Once multiple Agents must be found, revisited, scheduled, or coordinated, those responsibilities are mandatory somewhere in application code or Gotato Orchestration, with Host exposing it when needed; Core provides neither a global lookup nor recovery from AgentID alone.
33. Run settlement does not close an Agent. A retained Agent remains usable until explicit close or an owner-selected retirement policy.
34. Core exposes an idempotent, context-bounded close operation. Closing rejects new Runs, settles or explicitly cancels the current Run, closes local resources exactly once, and does not wait for remote delivery.
35. The default policy for a directly constructed Agent is `Retain`; `AfterRun`, `AfterIdle`, and `Ephemeral` are Orchestration retirement policies.
36. An Agent may request retirement after its current Run, but the owner decides whether to honor it. The request cannot mutate an external routing table or destroy another Agent.
37. Conversation identity belongs to Orchestration and may outlive a live Agent. Retained retirement persists state before route removal; rehydration creates a new AgentID for the same ConversationID.
38. A failed retained-retirement persistence operation must not be reported as successful. Ephemeral/discard retirement must not silently reuse discarded state.
39. Parent/child spawn correlation does not imply lifecycle ownership. Workflow-level cancellation or cleanup must be an explicit Orchestration policy.
40. A retained Conversation passes through `Retiring`; Orchestration stops admission and fences stale handles before persistence, close acknowledgement, and `Dormant` route installation.
41. Host drain expiry is an incomplete-drain outcome, not proof that Busy or Closing Agents were closed. Go cannot forcibly terminate work that ignores Context.

## 2. Open Core questions

```text
1. Exact channel-backed Core API and whether raw channels remain private
2. Default behavior when a direct Prompt arrives while the Agent is Busy
3. Structured Message content extensibility
4. JSON Schema implementation and supported subset
5. Whether a sequential Tool forces a whole batch sequential
6. Root namespace for always-visible Tools
7. Typed-function helper limits
8. Exact safe boundaries for Steer and Abort
9. FollowUp buffer capacity and the exact continuation boundary
```

## 3. Open Agent Routine questions

```text
1. Agent handle shape and channel ownership/close protocol
2. Whether a spawned Agent is created by Core, application, or Host factory
3. Which spawn metadata is normative: SpawnID, origin AgentID, origin RunID
4. Whether Agent-to-Agent channels are request/response, Event, or both
5. Whether routine-level groups belong in Core or Orchestration
6. How independent Agent shutdown is acknowledged
```

## 4. Open Lifecycle and Host questions

```text
1. Whether `Close(context.Context) error` is the final Core spelling or an additive lifecycle interface
2. Whether lifecycle signals use a separate AgentLifecycleEvent type or a separate channel contract
3. Snapshot format and atomic persistence interface for retained Conversations
4. Host concurrency defaults: streams, Agent goroutines, Runs, and queues
5. Default external Prompt policy while an Agent is Busy
6. Default Event Bridge capacity and queue-full policy
7. Progress coalescing window and memory bound
8. Cache size, idle TTL, and reset defaults
9. Default stream-close cancellation and Agent-retirement behavior
10. Exact Conversation routing mechanism for future multi-Pod deployments
11. Whether detailed Events from independent spawned Agents share or separate a stream
12. Host admission and quota scope for multi-tenant deployments
```

## 5. Open integration questions

```text
1. Required long-lived stream and Context behavior for supported protocol adapters
2. Which Gateway or load-balancer behaviors can be documented as compatibility guarantees
3. Durable state provider contract and failure behavior, if a future Host needs it
4. Which deployment examples should be maintained versus left to the application platform
```

## 6. Decision method

Resolve an open question with a small executable prototype, fake clock or deterministic channel fixture where relevant, race-tested acceptance cases, an owning layer, and a recorded compatibility impact.

## 7. Review record

Every resolution records:

```text
chosen behavior
rejected alternatives
acceptance test
owning package
channel and Context lifetime
compatibility impact
```
