# 15. Decisions and Open Questions

**Status:** Draft

> The Core boundary is decided. Host and platform policies remain explicit compatibility choices.

## 1. Decisions

### Product shape

1. Gotato provides a self-contained Agent Core and an optional Hosted Agent Service.
2. Embedded and Hosted modes are first-class; public Core release may be staged, but Core independence begins immediately.
3. Infrastructure is external and replaceable. Gateway, Kubernetes, load balancing, storage, and secrets are not Core dependencies.
4. Delivery proceeds in slices that end in a usable Embedded or Hosted capability.
5. Applications own business meaning and end-user presentation.

### Core semantics

6. Agent owns state; Run is one Prompt or Continue; one Agent has at most one active mutating Run.
7. There is exactly one canonical Agent Loop for Embedded, Hosted, and child Routine execution.
8. Prompt and Continue return a settled Core RunResult; Events are observed through local subscriptions.
9. Core uses explicit provider-neutral Model streams and Tool contracts.
10. Core fixes state transitions, assembly, validation, commitment, Event order, cancellation, limits, and terminal settlement.
11. Every Run emits exactly one terminal `agent_end`; nothing starts after it.
12. Tool executor invocation is at most once per ToolUse; explicit retry creates a new identity.
13. Tool failures become Tool Results when the loop can continue; blocking Extension and protocol failures terminate the Run.

### Host and transport

14. Orchestration/Agent Host manages multiple Core instances, admission, concurrency, conversation ownership, cache/lease, remote delivery, and lifecycle.
15. gRPC is a Hosted Transport adapter, not a Core dependency.
16. Host and Transport must not duplicate Core state or Loop behavior.
17. Protected Events cannot be silently dropped; remote progress may be coalesced under bounds.
18. Execution settlement and delivery settlement are independent.
19. A Host may treat attached stream closure as Run cancellation, but the policy must be explicit.
20. Ordinary Kubernetes load balancing does not guarantee Conversation continuity across Pods.

### Model and capabilities

21. Model Router and provider adapters own provider selection and provider-specific policy.
22. Tools and ToolSets are explicit Core capabilities implemented by application or protocol adapters.
23. Agent Routine is managed child Agent execution using the canonical Loop.

## 2. Open Core questions

```text
1. Default blocking versus advisory observer failure
2. Structured Message content extensibility
3. JSON Schema implementation and supported subset
4. Whether a sequential Tool forces a whole batch sequential
5. Root namespace for always-visible Tools
6. Typed-function helper limits
7. Whether one Agent owner goroutine is the default implementation
```

## 3. Open Host questions

```text
1. Host concurrency defaults: streams, Runs, queues, and per-Agent limits
2. Default Event Bridge capacity and queue-full policy
3. Progress coalescing window and memory bound
4. Cache size, TTL, lease, and reset defaults
5. Default stream-close cancellation behavior
6. Exact Conversation ownership mechanism for multi-Pod deployments
7. Whether detailed child Events share or separate the parent stream
8. Host admission and quota scope for multi-tenant deployments
```

## 4. Open Infrastructure questions

```text
1. Supported Gateway products and long-lived gRPC requirements
2. Whether session affinity is an example or a supported guarantee
3. Durable state provider contract and failure behavior
4. Required Kubernetes baseline versus application-provided platform
```

## 5. Decision method

Resolve an open question with a small executable prototype, fake clock or deterministic fixture where relevant, race-tested acceptance cases, an owning layer, and a recorded compatibility impact.

## 6. Review record

Every resolution records:

```text
chosen behavior
rejected alternatives
acceptance test
owning package
compatibility impact
```
