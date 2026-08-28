# 11. Testing and Acceptance

**Status:** Draft

> Deterministic fakes prove the runtime; in-process transport proves the service; no acceptance test needs a provider network.

```text
Scripted Model ─► Canonical Loop ─► Recording Tool
                        │
                        ├──► Event Recorder
                        ├──► Child Agent Factory
                        └──► in-process gRPC ─► scripted client
```

## 1. Testkit

The testkit MUST include:

```text
scripted Model stream
succeeding, failing, and panicking Tools
streaming and cancellable Tools
ToolSet fixtures
Event recorder with sequence and class assertions
blocking and advisory Extension fixtures
child Agent factory and Routine fixtures
fake clock
slow-consumer stream fixtures
in-process gRPC harness
```

Runtime and service acceptance tests MUST run without a provider network call.

## 2. Loop acceptance

Tests MUST prove:

- a text response completes in one Turn;
- one Tool Call returns to the Model;
- multiple Tool Results commit in assistant source order;
- malformed streamed arguments never execute;
- Tool errors become failed Tool Results;
- `Continue` adds no user Message;
- Steering enters after the current Tool batch;
- Follow-up enters at normal completion;
- a stop decision prevents the next Model request;
- `agent_end` is the final Event and the only terminal signal;
- a transient Model failure retried inside the Run produces exactly one terminal Event.

## 3. Tool Use acceptance

Tests MUST prove:

- Pre-Tool-Use receives resolved and validated arguments;
- a blocked use does not execute its Tool;
- the Tool executor runs at most once;
- Post-Tool-Use receives executed and blocked outcomes;
- Pre executes in installation order and Post in reverse order;
- blocking Extension errors terminate the Run;
- Tool execution errors become failed Tool Results;
- parallel completion Events reflect actual order while commitment stays in source order.

## 4. ToolSet acceptance

Tests MUST prove:

- composition and ordering are deterministic;
- duplicate names fail construction;
- individual Tools remain visible;
- inactive ToolSets appear in the activation Tool;
- successful activation changes the next Model request;
- activation is idempotent and bounded;
- provider name encoding maps back to the correct qualified Tool.

## 5. Event and delivery acceptance

Tests MUST prove:

- canonical Event order matches state transitions;
- every Event kind is classified protected or coalescable;
- protected Events survive coalescing under load;
- a coalescable Event carries nothing its settling Event omits;
- `agent_end` is the last Event of every Run;
- enrichers and projections preserve Event identity, kind, and correlation;
- an observer exceeding its bound is detected;
- a full queue applies its documented policy;
- a protected Event is never silently dropped;
- a slow consumer does not stall an unrelated Run;
- `WaitForIdle` returns without remote delivery;
- drain flushes or abandons within its deadline.

## 6. Agent Routine acceptance

Tests MUST prove:

- a Routine uses a child Agent distinct from its parent;
- a Routine settles exactly once;
- parent cancellation reaches child Model, Tools, and nested Routines;
- active Routine count and nesting depth are bounded;
- Routine lifecycle Events contain parent and child correlation;
- completion Events use completion order;
- group Results use deterministic spawn order;
- collect-all, fail-fast, partial, and first-success policies settle correctly;
- a `spawn_agent` Tool returns the Routine Result to the parent Model.

## 7. Concurrency acceptance

Tests MUST prove:

- a second Run on one Agent receives a typed busy error;
- parallel Tool execution respects its bound;
- cancellation reaches Model, Tools, Extensions, observers, and Routines;
- cache, queue, bridge, and subscriber operations pass `go test -race`;
- per-key cache creation yields exactly one Agent under concurrent first requests.

## 8. Error and limit acceptance

Tests MUST cover:

- panic recovery at Tool, Extension, observer, Routine, and service boundaries;
- safe public error messages;
- Turn, Tool Call, ToolSet visibility, Routine, and result-size limits;
- deadline propagation across the ownership tree;
- typed limit outcomes that admit no further work governed by the limit.

## 9. Service acceptance

Tests MUST prove:

- a client completes a text-only attached Run;
- Tool, ToolSet, and Routine Events cross the transport in canonical order;
- `Start` ordering, duplicate `Start`, and post-terminal commands behave as specified;
- Steering and Follow-up reach the active Agent in accepted order;
- client cancellation reaches Model, Tool, and Routine fakes;
- concurrent conversations receive isolated Agent state;
- a conversation cache hit returns the same idle Agent with its transcript;
- active cache entries remain pinned and TTL applies only to idle entries;
- admission and Event buffering are bounded;
- readiness changes during drain and DrainPolicy is applied;
- error categories map to the specified transport statuses.

## 10. Equivalence acceptance

The direct consumer and the service MUST be tested against the same scenarios:

```text
same input        → identical canonical Event sequence
same cancellation → identical terminal outcome
same limits       → identical typed outcomes
```

Divergence between the two paths is a contract violation, not a configuration difference.

## 11. Integration tests

The acceptance suites above MUST pass with fakes alone. A separate integration layer exercises what those fakes deliberately stand in for:

```text
provider adapters      real Model endpoints and provider encoding
capability adapters    real HTTP, gRPC, and MCP capability services
deployment lifecycle   probes, rollout, and graceful termination
```

Integration tests MUST NOT be a precondition for the acceptance suites, and an acceptance test MUST NOT be relaxed because an integration test covers similar ground. The two answer different questions: acceptance asks whether the contract holds, integration asks whether an external system matches an adapter's assumptions about it.

## 12. Quality gates

```text
gofmt
go vet
go test ./...
go test -race ./...
```

Pi-semantic compatibility tests cover documented loop behavior where it applies. Gotato-specific tests cover ToolSets, delivery policy, Agent Routines, and service semantics.

## 13. Deterministic fixture contract

A fixture MUST make every external decision explicit:

```go
type ModelScript struct {
    Calls []ModelCallScript
}

type ModelCallScript struct {
    Events []ModelEvent
    Gate   <-chan struct{}
}

type ToolScript struct {
    Result ToolResult
    Delay  time.Duration
    Start  func()
    Stop   func()
}
```

The exact helper names MAY evolve, but a scripted Model MUST select its response by call order, a scripted Tool MUST record invocation count and arguments, and a fake clock MUST control TTL and deadline observations without sleeping the test process.

A deterministic acceptance test MUST:

1. use fixed IDs or an injectable ID generator;
2. use a fixed clock and explicit Model/Tool scripts;
3. record canonical Events before transport projection;
4. assert both transcript commitment and Event sequence;
5. wait on execution settlement rather than arbitrary sleeps;
6. restore or close every fixture-owned resource.

A concurrency test MUST coordinate goroutines with channels, barriers, or Context cancellation and MUST assert the bound being tested. A test that passes only because the scheduler happened to run one goroutine first is not an acceptance test.

A slow-consumer fixture MUST expose queue occupancy and sender progress. It MUST prove whether the configured policy blocks, coalesces, or terminates, and MUST assert that no protected Event is silently lost.

An integration test MAY use a provider, capability service, or Kubernetes cluster, but its failure MUST be distinguishable from a Runtime contract failure. Integration tests MUST record the adapter version and external endpoint class without recording credentials or raw prompts.
