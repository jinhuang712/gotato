# 15. Decisions and Open Questions

**Status:** Draft

> Decisions guide implementation; prototypes resolve the remaining API choices.

## 1. Decisions

### Product and structure

1. Gotato is inspired by Pi and reimplements its agent-kernel semantics in Go, shaped around a service boundary rather than a coding-agent product.
2. The service is the first product boundary. A direct Go API is a second consumer, promoted from runtime contracts after service use has proven them.
3. Delivery proceeds in vertical slices, each ending in a callable service. A slice that only adds interfaces is not complete.
4. Structural invariants hold from the first code commit: no transport imports in runtime packages, wire types mapped at the boundary, one loop, bounded work everywhere.
5. External applications own end-user presentation. Gotato provides no first-party CLI, TUI, web, or chat experience.

### Runtime semantics

6. Agent is stateful; Run is one `Prompt` or `Continue` invocation; one Agent owns at most one active mutating Run.
7. The repository contains exactly one canonical Agent loop. The service, a direct caller, and every Agent Routine converge on it.
8. `Prompt` and `Continue` return a terminal `RunResult`; Events arrive through `Subscribe` during execution.
9. The provider-facing Model API uses an explicit stream, preferably `Recv`-style.
10. Core fixes state transitions, stream assembly, validation, commitment, Event order, cancellation, and terminal settlement.
11. A Run emits exactly one terminal Event. Retry, compaction, and queued continuation occur inside the Run. No second completion signal exists.
12. Blocking Extension errors terminate the Run; Tool execution errors become failed Tool Results when execution can continue.
13. The Runtime invokes a Tool executor at most once per Tool Use. Retry belongs to an explicit idempotency-aware policy.

### Moving Parts

14. Public Moving Parts begin with Model, ContextTransformer, MessageConverter, Tool, ToolSet, PreToolUse, PostToolUse, TurnStopper, EventObserver, and the Agent Routine factory.
15. Pre-Tool-Use receives immutable validated arguments and returns Proceed, Block, or a termination hint.
16. Post-Tool-Use receives executed and blocked outcomes.
17. Tool performs one operation; ToolSet groups and stages capability discovery through a built-in activation Tool that affects the next Model Turn.

### Events and delivery

18. Canonical Events are immutable facts. Enrichment, filtering, projection, redaction, delivery, and sinks are consumer-side Moving Parts.
19. Every Event kind is classified protected or coalescable. Protected Events are delivered in canonical order or the consumer's stream fails; coalescable progress may be merged under load.
20. A coalescable Event carries nothing its settling protected Event omits.
21. Observers run in-process, are awaited, and MUST NOT block on a network peer. Remote delivery always crosses a bounded bridge.
22. Backpressure is chosen explicitly per boundary. Unbounded queues and detached sender goroutines are not acceptable defaults.
23. Execution settlement and delivery settlement are independent and owned by the Runtime and Service respectively. `WaitForIdle` observes execution settlement only.
24. A disconnect ends delivery; whether it cancels the Run is a documented service policy, with stream-close-as-cancel the expected default for attached Runs.

### Agent Routines

25. Agent Routine is the Gotato concept for a managed child Agent Run, composed from the canonical loop rather than a second execution model.
26. Routines use separate child Agents, parent Context cancellation, bounded concurrency, and settled Routine Results. Group Results aggregate in spawn order; completion Events follow actual completion order.

### Service

27. The first gRPC contract is a bidirectional attached Run stream carrying Start, Steer, FollowUp, and Cancel.
28. The service maps commands onto the canonical runtime API and MUST NOT maintain a second Agent state machine.
29. The service owns Agent factories, conversation resolution, a bounded cache with active-Run pinning, admission, readiness, and drain.
30. Kubernetes lifecycle belongs to service and deployment packages; cluster primitives consume service signals rather than define Agent semantics.

## 2. Open runtime questions

```text
1. What exact Go interface should ModelStream.Recv use?
2. Should EventHandler receive the Run Context as a second argument?
3. Which subscriber failures default to blocking versus advisory behavior?
4. How should structured Message content balance type safety and adapter extensibility?
5. Which JSON Schema implementation provides the required behavior with acceptable weight?
6. Should one sequential Tool force its entire batch into sequential mode?
7. What reserved namespace should individual always-visible Tools use?
8. How should ToolSet descriptions be encoded in the activation Tool Schema?
9. Which typed-function signatures can the reflection adapter support safely?
```

## 3. Open event and delivery questions

```text
1. Which correlation attributes belong to the canonical type versus an Enricher?
2. What observer-bound shape is detectable: wall-clock, event count, or both?
3. What queue-full policy should the gRPC preset use by default?
4. What queue capacity and coalescing window are safe preset defaults?
5. Does a detailed child Routine Event stream merge into the parent stream or remain separate?
6. Which clock and correlation interfaces remain internal test dependencies?
```

## 4. Open Agent Routine questions

```text
1. What exact Spawn, Routine, Group, Wait, and Cancel APIs feel natural in Go?
2. Does WaitForIdle always wait for owned Routines or follow the group policy?
3. Which Routine Group policy is the default?
4. How is maximum nesting depth propagated across Agent factories?
5. How does a remote Routine executor preserve cancellation and Event correlation?
```

## 5. Open service questions

```text
1. What exact Protobuf envelope preserves Event compatibility across versions?
2. What active-Run policy should graceful drain use by default?
3. What Agent cache interface best expresses acquire, pin, release, reset, and eviction?
4. Which cache TTL and size defaults are safe for the preset?
5. Should Kubernetes session affinity be part of the baseline example?
6. Should stream close cancel the attached Run, or follow a configurable policy?
```

## 6. Decision method

Open questions SHOULD be resolved with small executable prototypes, compatibility fixtures, fake clocks, and race-tested acceptance cases, inside the delivery slice that first needs the answer.

## 7. Review record

Each resolved question SHOULD record:

```text
chosen behavior
rejected alternatives
acceptance test
owning package
compatibility impact
```
