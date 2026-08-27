# 10. Testing and Acceptance

**Status:** Draft

> Deterministic fakes prove the kernel and Agent Routines; in-process transport tests prove the service.

```text
Scripted Model ─► Agent Loop ─► Recording Tool
                        │
                        ├──────► Event Recorder
                        └──────► Child Agent Factory
```

## 1. Testkit

Phase one MUST include:

```text
scripted Model stream
successful, failing, and panicking Tools
streaming and cancellable Tools
ToolSet fixtures
Event recorder and sequence assertions
blocking and advisory Extension fixtures
child Agent factory and Routine fixtures
fake clock
```

Kernel acceptance tests MUST run without a provider network call.

## 2. Loop acceptance

Tests MUST prove:

- a text response completes in one Turn;
- one Tool Call returns to the Model;
- multiple Tool Results commit in source order;
- malformed streamed arguments never execute;
- Tool errors become failed Tool Results;
- `Continue` adds no user Message;
- Steering enters after the current Tool batch;
- Follow-up enters at normal completion;
- turn stopping prevents the next Model request;
- `agent_end` is the final Event and settlement barrier.

## 3. Tool Use acceptance

Tests MUST prove:

- Pre-Tool-Use receives resolved and validated arguments;
- a blocked use does not execute its Tool;
- the Tool executor runs at most once;
- Post-Tool-Use receives executed and blocked outcomes;
- Pre executes in installation order and Post in reverse order;
- blocking Extension errors terminate the Run;
- Tool execution errors become failed Tool Results.

## 4. ToolSet acceptance

Tests MUST prove:

- composition and ordering are deterministic;
- duplicate names fail construction;
- individual Tools remain visible;
- inactive ToolSets appear in the activation Tool;
- successful activation changes the next Model request;
- activation is idempotent and bounded;
- provider name encoding maps back to the correct qualified Tool.

## 5. Event acceptance

Tests MUST prove:

- canonical Event order matches state transitions;
- enrichers preserve Event identity and kind;
- filters affect one consumer projection;
- terminal Events survive progress coalescing;
- `agent_end` remains the final Run Event;
- bounded bridges apply their documented slow-consumer policy.

## 6. Agent Routine acceptance

Tests MUST prove:

- a Routine uses a child Agent distinct from its parent;
- a Routine settles exactly once;
- parent cancellation reaches child Model and Tools;
- active Routine count and nesting depth are bounded;
- Routine lifecycle Events contain parent and child correlation;
- completion Events use completion order;
- group Results use deterministic spawn order;
- collect-all, fail-fast, partial, and first-success policies settle correctly;
- a `spawn_agent` Tool returns the Routine Result to the parent Model.

## 7. Concurrency acceptance

Tests MUST prove:

- a second Run on one Agent receives a busy error;
- parallel Tool execution respects its bound;
- completion Event order may differ from transcript order;
- cancellation reaches Model, Tools, Extensions, subscribers, and Routines;
- `WaitForIdle` includes terminal subscriber and owned Routine settlement;
- queue, Routine, cache, and subscriber operations pass `go test -race`.

## 8. Error and limit acceptance

Tests MUST cover panic recovery, safe public errors, Turn limits, Tool Call limits, ToolSet visibility limits, Routine limits, result-size limits, and deadline propagation.

## 9. Service acceptance

Phase-two tests MUST prove:

- a gRPC client starts an attached Run;
- canonical Events cross the transport in order;
- Steering, Follow-up, and Cancel map to the active Agent;
- each concurrent conversation uses isolated Agent state;
- Agent cache entries obey TTL, pinning, and idle eviction;
- admission and transport buffering are bounded;
- readiness and drain follow the service lifecycle.

## 10. Quality gates

```text
gofmt
go vet
go test ./...
go test -race ./...
```

Pi-semantic compatibility tests cover documented kernel behavior while Gotato-specific tests cover ToolSets, Moving Parts, Agent Routines, and service semantics.
