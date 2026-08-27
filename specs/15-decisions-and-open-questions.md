# 15. Decisions and Open Questions

**Status:** Draft

> Decisions guide implementation; prototypes resolve the remaining API choices.

## 1. Decisions

1. Gotato is inspired by Pi and phase one reimplements Pi agent-kernel semantics in Go.
2. Gotato provides an embedded kernel and an Agent-as-a-Service form built on the same loop.
3. Agent is stateful; Run is one `Prompt` or `Continue` invocation.
4. One Agent owns at most one active mutating Run.
5. `Prompt` and `Continue` return a terminal `RunResult`; subscribers receive Events during execution.
6. The provider-facing Model API uses an explicit stream, preferably `Recv`-style.
7. Core fixes state transitions, stream assembly, validation, commitment, Event order, cancellation, and terminal settlement.
8. Public Moving Parts begin with ContextTransformer, MessageConverter, Model, Tool, ToolSet, PreToolUse, PostToolUse, EventObserver, and TurnStopper.
9. Pre-Tool-Use receives immutable validated arguments and returns Proceed, Block, or a termination hint.
10. The Runtime invokes a Tool executor at most once per Tool Use.
11. Post-Tool-Use receives executed and blocked outcomes.
12. Blocking Extension errors terminate the Run; Tool execution errors become failed Tool Results when execution can continue.
13. Canonical Events are immutable facts; enrichment, filtering, projection, redaction, delivery, and sinks are Moving Parts.
14. Tool performs one operation; ToolSet groups and stages capability discovery.
15. Inactive ToolSets are selected through a built-in activation Tool.
16. ToolSet activation affects the next Model Turn and remains in Agent state.
17. Agent Routine is the Gotato concept for a managed child Agent Run.
18. Agent Routines use separate child Agents, parent Context cancellation, bounded concurrency, and settled Routine Results.
19. A model-driven sub-Agent spawn uses an ordinary Tool backed by Agent Routine APIs.
20. Phase two delivers an Agent factory, bounded in-service cache, service preset, and gRPC server/client.
21. The first gRPC contract is a bidirectional attached Run stream.
22. Kubernetes lifecycle belongs to service and deployment packages.
23. External applications own end-user presentation.

## 2. Open Core API questions

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

## 3. Open Event questions

```text
1. Which Event attributes belong to the canonical type versus an Enricher?
2. Which Event kinds allow progress coalescing?
3. How are blocking and advisory subscribers represented in the Go API?
4. Does a detailed child Routine Event stream merge into the parent stream or remain separate?
5. Which clock and correlation interfaces remain internal test dependencies?
```

## 4. Open Agent Routine questions

```text
1. What exact Spawn, Routine, Group, Wait, and Cancel APIs feel natural in Go?
2. Does WaitForIdle always wait for owned Routines or follow the group policy?
3. Which Routine Group policy is the default?
4. How is maximum nesting depth propagated across Agent factories?
5. Which Routine lifecycle Events carry progress versus only state transitions?
6. How does a remote Routine executor preserve cancellation and Event correlation?
```

## 5. Open service questions

```text
1. What exact Protobuf envelope preserves Event compatibility across versions?
2. Which slow-consumer policy should the gRPC preset use by default?
3. What active-Run policy should graceful drain use by default?
4. What Agent cache interface best expresses acquire, pin, release, reset, and eviction?
5. Which cache TTL and size defaults are safe for the preset?
6. Should Kubernetes session affinity be part of the baseline example?
```

## 6. Decision method

Open questions SHOULD be resolved with small executable prototypes, compatibility fixtures, fake clocks, and race-tested acceptance cases.

## 7. Review record

Each resolved question SHOULD record:

```text
chosen behavior
rejected alternatives
acceptance test
owning package
compatibility impact
```
