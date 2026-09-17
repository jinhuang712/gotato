# Refactor Audit

Implementation artifact for the runtime-foundation refactor. Not part of the constitution. Facts below were read from the code at baseline commit `eff07cb` (branch `main`) on 2026-09-17; line numbers refer to that commit.

## 1. Current Repository Architecture

Two Go modules:

| Module | Path | Go | Deps |
|---|---|---|---|
| `github.com/jinhuang712/gotato` | `/` | 1.22 | `gopkg.in/yaml.v3` (only used by `gateway`) |
| `github.com/jinhuang712/gotato/adapter/grpc` | `adapter/grpc` | 1.25 | grpc, protobuf; `replace` to `../..` |

Narrative (README, docs/, specs/): "Go-native Agent Runtime **and Orchestration**", three scales: embedded single agent → orchestration of N agents → hosted service behind HTTP/gRPC. The only executable is an HTTP daemon.

Baseline verification: `go build ./...`, `go vet ./...`, `go test ./...` pass in both modules (see final report for timings). No CI configuration, no Makefile, no LICENSE, no lint config.

## 2. Package Map

| Package | Files | Lines (non-test) | Responsibility |
|---|---|---|---|
| `gotato` (root) | agent.go, types.go, model.go, tool.go, toolset.go, toolfunc.go, toolbatch.go, events.go, extensions.go, errors.go, limits.go | ~3,100 | agent goroutine + loop, messages, model contract, tools, tool sets with activation, JSON-schema subset validator, func-tool schema generator, event hubs, extensions, errors, limits |
| `internal/testmodel` | model.go | 70 | `EchoModel`, `DemoModel` deterministic models |
| `orchestration` | orchestrator.go, spawn.go | ~1,000 | routing by agent name + conversation key, admission limits, FIFO/reject queue, retirement, drain, spawn + group coordination with provenance |
| `host` | service.go, server.go, async_runs.go | ~1,070 | protocol-independent `Service` boundary, HTTP handlers (sync, SSE stream, ndjson progress, async+poll, cancel, drain), retained async run table |
| `adapter/grpc` (module) | server.go + generated | ~210 + gen | gRPC over `host.Service` |
| `gateway` | gateway.go, codex.go, config.go | ~1,590 | OpenAI-compatible chat completions SSE, OpenAI Codex Responses SSE with Pi OAuth credential reuse, YAML config |
| `cmd/gotato-agent` | main.go | 140 | HTTP daemon wiring orchestration + host + models |
| `docs/` (14 files), `specs/` (18 files) | | ~4,700 | prior architecture narrative and normative specs |

## 3. Current Agent Semantics

- `Agent` interface: `Prompt(ctx, Message) (RunResult, error)` and `Close(ctx) error` (`agent.go:18`).
- Optional capability interfaces discovered by type assertion: `AgentLifecycle` (Status, Done), `EventAgent`/`EventSource` (Subscribe), `LifecycleAgent`, `RunCanceler`, `IdleWaiter`, `ControllableAgent` (Continue, Steer, FollowUp, Abort).
- `NewAgent(options...)` starts one goroutine (`go a.loop()`) that is the sole mutation authority for transcript, registry, and events (`agent.go:155-201`). Single admission: a second concurrent `Prompt` fails with `busy`.
- One run = `executeRun` (`agent.go:623`): emits `agent_start`, commits the prompt, loops Turns: clone transcript → extension transformers/converters → `ModelRequest{SystemInstructions, Messages, Tools}` → `readAssistant` streams `ModelEvent`s → commit assistant → tools (preflight source-ordered, execution sequential or bounded parallel, results committed source-ordered) → `turn_end` → `TurnStopper` → control messages → settle or continue.
- Termination: `RunResult{Status completed|failed|cancelled|deadline_exceeded, FinalMessage, Usage, Metrics, Error}`. Stop by extension recorded in the `agent_end` payload.
- Limits: `CoreLimits` with defaults in unexported `defaultLimits()`; `WithLimits` sets `limitsSet=true`, after which a zero field means "admit nothing" (documented defect T04 in `TODO.md`).
- Satisfies G-D01, G-D02, G-D09, G-D11 already. Violates G-D03/G-D10 in one respect: the committed transcript is `coreAgent.messages` (`agent.go:231`), so the agent is also the session.

## 4. Current Session / History / Context Behavior

- **History**: `coreAgent.messages []Message`, appended only by `commitMessage` (`agent.go:541`). No identity, no persistence, no fork, no usage/event record. It dies with the agent. The last commit on `main` (`eff07cb refactor: remove snapshot and restore lifecycle`) deleted the previous snapshot/restore mechanism, so today nothing outlives the agent goroutine.
- `commitMessage` deep-clones and re-marshals the *entire* transcript on every commit to enforce `MaxTranscriptBytes` (O(n²), TODO T07).
- **Orchestration "Conversation"**: `orchestration.Record` is routing identity + status only ("conversation content belongs to the live Agent", `orchestrator.go:31`). It is not a Session.
- **Context**: the model receives `cloneMessages(a.messages)` unless a `ContextTransformer`/`MessageConverter` extension rewrites it (`agent.go:719-733`). There is no builder abstraction, no named strategy, no inspection API, no `context_built` event, no compaction. `ContextSnapshot` (in `extensions.go`) is the right input shape and is reused by the new `ContextBuilder`.
- Conclusion: Session and Context are conflated by default; the extension hook is the only seam.

## 5. Current Tool Architecture

- `Tool{Spec() ToolSpec; Execute(ctx, ToolUse, ToolProgress) (ToolResult, error)}` (`tool.go`). `ToolSpec` has ID, Name, Description, InputSchema/OutputSchema JSON, Sequential flag, Metadata. Satisfies G-D13 / G-F09.
- `NewFuncTool[In,Out]` derives a JSON schema from a struct via reflection (`toolfunc.go`); core validates arguments against a schema subset before execution (`agent.go:1182+`).
- `ToolSet` = named, lazily resolved collection that is hidden until activated by the model through a built-in `activate_toolset` tool (`toolset.go`). Activation commits at the batch boundary and emits `toolset_activated`.
- `toolRegistry` (`toolset.go:79`) is **unexported**, built once in `NewAgent`, mutable only by the agent goroutine. No public register/unregister/list/describe/activate/deactivate; no way for an application to change the tool surface after construction other than model-driven ToolSet activation. Root tools are static.
- `ToolUse.Executed` and `ToolUse.Result` are never assigned (TODO T15).

## 6. Current Event / Streaming Architecture

- `Event{AgentID, RunID, Sequence, Kind, Class, Turn, MessageID, ToolCallID, SpawnID, OriginRunID, Payload map[string]any, Timestamp}` (`events.go:36`).
- Kinds: agent_start, turn_start, message_start, message_update, message_end, tool_execution_start/update/end, tool_result_committed, toolset_activated, turn_end, agent_end. Classes: protected (buffer-full closes the subscriber) vs coalescable (dropped when full).
- `EventStream{Next(ctx) (Event, error); Close() error}` via a per-agent hub; `LifecycleEvent` stream separately.
- Observers (`EventObserver` extension) are awaited synchronously at each event boundary (blocking by default).
- Gaps versus G-F13: no context built/compacted events, no explicit model request/response events (covered by message_start/end), no session_updated, reasoning deltas produce no event. Payloads are untyped maps (TODO A06).
- Defect: each `Subscribe` spawns a goroutine waiting on `ctx.Done()` that never exits when the subscription is closed first (TODO T05).

## 7. Current CLI State

There is **no CLI**. `cmd/gotato-agent` is an HTTP daemon with flags for model, timeouts, admission limits. All interaction is `curl` against `/v1/runs*`. Nothing satisfies G-D22/23/24 or G-F16–G-F21.

## 8. Current Persistence State

None. Snapshot/restore were removed in `eff07cb`. `orchestration` keeps routing records in memory only. No store interface, no file store, no SQLite.

## 9. Current Testability

- 84 test functions across packages; all deterministic; none call a network provider. Good foundation.
- Test doubles duplicated: root `testModel`/`testStream`/`scriptedTool`/`toolModel` (`agent_test.go`), `internal/testmodel.EchoModel/DemoModel`, plus per-package fakes in `orchestration` and `host` tests.
- No exported fake/replay model, fake tool, event recorder, or session fixtures (G-D25, G-F22 missing).
- `cmd/gotato-agent/main_test.go` starts the real process and hits HTTP: the only end-to-end test.
- No `-race` in any documented command; no CI.

## 10. Public API Compatibility Concerns

| Surface | Users found | Concern |
|---|---|---|
| root `Agent`, `NewAgent`, options, messages, tools, events, extensions | orchestration, host, gateway, cmd, tests | must stay source-compatible; all changes additive |
| `SpawnID`, `Event.SpawnID`, `Event.OriginRunID` | `orchestration/spawn.go`, gRPC proto | orchestration provenance in core types; deprecate, remove in Stage H with a wire ContractVersion bump |
| `CoreLimits` zero semantics after `WithLimits` | documented in specs/08 | keep semantics; export `DefaultLimits()` so partial overrides are possible |
| `internal/testmodel` | cmd/gotato-agent, host tests? | internal; may move freely |
| HTTP/gRPC wire contract `ContractVersion "1"` | none outside repo | unchanged in this refactor |
| `gateway` YAML schema | `gateway*.yaml` examples | unchanged |

## 11. Mapping Existing → Whitepaper Components

| Whitepaper | Existing | Verdict |
|---|---|---|
| Agent (G-F01) | `coreAgent` + `Agent`/`ControllableAgent` | keep; add `WithTranscript`, `WithContextBuilder`, `WithToolSource`, `Tools()` |
| Session (G-F02) | none (`coreAgent.messages`) | **add** `session` package; agent commits through `Transcript` |
| Session Store (G-F03) | none | **add** `session.Store`, `MemoryStore`, `FileStore` |
| Context Runtime (G-F04) | `ContextTransformer` extension, `ContextSnapshot` | **add** `ContextBuilder` + `modelctx` strategies; keep transformers as post-processing |
| Context Inspection (G-F05) | none | **add** `modelctx.Inspect` + CLI `context inspect/build` |
| Compaction (G-F06) | none (TODO A03) | **add** `modelctx.Compact`, `Summarizer`, `session.Compaction`, events |
| Model (G-F07) | `Model`/`ModelStream`/`ModelEvent` | keep |
| Providers (G-F08) | `gateway` (OpenAI-compatible + Codex) | keep as is |
| Tool (G-F09) | `Tool`/`ToolSpec`/`NewFuncTool` | keep |
| Tool Registry (G-F10) | unexported `toolRegistry` + `ToolSet` | **add** `toolregistry.Registry` implementing `gotato.ToolSource`; registry refresh per Turn |
| Standard tools (G-F11) | `demo.echo` in cmd only | missing; Stage H |
| MCP (G-F12) | none | missing; Stage H (ToolSet is the natural adapter) |
| Runtime Events (G-F13) | `Event` kinds above | keep; add `context_built`, `session_compacted` |
| Streaming (G-F14) | `EventStream` | keep; fix subscription leak |
| Extensions (G-F15) | six interfaces | keep |
| CLI (G-F16–21) | none | **add** `cmd/gotato` |
| Testing (G-F22) | duplicated fakes | **add** `testkit`; fold `internal/testmodel` in |
| Scenario testing (G-F23) | `cmd/gotato-agent/main_test.go` | add CLI scenario tests in `cmd/gotato` |
| Examples (G-F24) | README snippets | missing; Stage H |
| Orchestration/host/gRPC | `orchestration`, `host`, `adapter/grpc`, `cmd/gotato-agent` | keep as **optional service layer**; not runtime foundation (G-D19/G-N03) |

## 12. Code to Preserve

Everything in the root package; `gateway`; `orchestration`, `host`, `adapter/grpc`, `cmd/gotato-agent` (as optional layer); all tests.

## 13. Code to Move / Rename

| From | To | When |
|---|---|---|
| `internal/testmodel` | `testkit` | now |
| root test doubles (`testModel`, `scriptedTool`, …) | keep in root tests (root cannot import `testkit` without a cycle) | n/a |
| `orchestration`, `host`, `adapter/grpc`, `cmd/gotato-agent` | `service/…` umbrella (or separate module) | Stage H, with migration note |
| `docs/`, `specs/` product narrative | historical; banner added pointing to root docs | now (banner), rewrite later |

## 14. Code to Deprecate or Remove

| Item | Action |
|---|---|
| `SpawnID` type, `Event.SpawnID`, `Event.OriginRunID` | `// Deprecated` now; remove in Stage H together with a wire ContractVersion bump |
| `modelStreamDone()` (unused, `model.go:53`) | remove now |
| `ToolUse.Executed`, `ToolUse.Result` (never written) | document as reserved now; remove in Stage H |
| empty `requestLog` middleware in `host/server.go` | Stage H |
| old README "Runtime and Orchestration" narrative | replace now |

## 15. Highest-Risk Refactor Points

1. **Routing commits through `Transcript`** touches the hottest path (`commitMessage`, `executeRun`). Risk: limit semantics and clone discipline regress. Mitigation: keep the default in-memory transcript behavior identical, keep all existing tests, add tests with an external `session.Session`.
2. **Per-Turn registry refresh for `ToolSource`** changes when `visibleSpecs` is computed. Risk: a tool the model was shown disappears before lookup. Mitigation: refresh only at Turn start; lookup uses the same built set for the whole Turn.
3. **New `context_built` event** changes event sequences that existing tests and the host equivalence tests assert. Mitigation: run and update tests; the event is protected and always emitted, so embedded and hosted sequences stay equal.
4. **Event subscription goroutine fix** touches the hub used by host streaming. Mitigation: unit test for goroutine count; host tests unchanged.
5. **CLI file store layout** becomes a contract once scripts depend on it. Mitigation: one JSON file per session under a directory, versioned with a `schema_version` field.
6. **Doc replacement** may drop useful design rationale. Mitigation: keep `docs/` and `specs/` intact with a banner; only root docs are rewritten.
