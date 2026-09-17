# Gotato Features — Implementation Inventory

The standard runtime surface, mapped to packages and files. Update the marker in the same commit that changes the status.

Markers: `[done]` implemented and tested · `[partial]` usable with listed gaps · `[missing]` not started · `[needs-refactor]` exists but conflicts with DESIGN.md

---

## G-F01 — Agent Runtime `[done]`

Package: root `gotato` (`agent.go`, `toolbatch.go`, `context.go`, `limits.go`, `errors.go`).

| Item | Status | Where |
|---|---|---|
| reusable Agent configuration | done | `NewAgent(...Option)`; `WithModel`, `WithInstruction`, `WithTool(s)`, `WithToolSet`, `WithToolSource`, `WithTranscript`, `WithContextBuilder`, `WithExtension(s)`, `WithLimits`, `WithDeadlines` |
| run/turn execution | done | one goroutine per agent, one loop (`executeRun`) |
| tool-call loop | done | source-ordered preflight, sequential or bounded-parallel execution, source-ordered commit |
| final result | done | `RunResult.FinalMessage` |
| termination reason | done | `RunResult.Status`, `RunResult.Error`, `agent_end` payload `status`/`stop_reason`/`stopped_by_extension` |
| usage aggregation | done | `RunResult.Usage`; per-run record in `session.Run` |
| cancellation/deadline propagation | done | `context.Context` through model, tools, extensions; `CoreLimits` deadlines derive contexts |
| control operations | done | `ControllableAgent`: Continue, Steer, FollowUp, Abort |
| defaults inspectable | done | `DefaultLimits()` |
| restart-safe identifiers | done | agent/run/message IDs carry a per-process nonce |

## G-F02 — Session `[done]`

Package: `session` (`session.go`, `recorder.go`).

| Item | Status | Where |
|---|---|---|
| session ID | done | `Session.ID()`; random, stable across restarts |
| message history | done | `Session.Messages()`; implements `gotato.Transcript` |
| model/tool interaction history | done | assistant messages with `ToolCalls`, `tool_result` messages |
| metadata | done | `Session.Metadata()/Get/Set` (application-owned) |
| usage | done | `Session.Usage()` aggregated by `Recorder` |
| runtime events | done | `Session.Events()` bounded (default 1000) via `Recorder`; run records in `Session.Runs()` |
| context/compaction metadata | done | `Session.Compactions()` |
| create/load/save/close lifecycle | done | `Store.Save/Get/List/Delete`; `Snapshot`/`Load` documents with `schema_version` |
| fork/branch | done | `session.Fork` records `ParentID` |

## G-F03 — Session Store Interfaces `[partial]`

| Item | Status | Where |
|---|---|---|
| store interface | done | `session.Store` |
| in-memory store | done | `session.MemoryStore` |
| file store | done | `session.FileStore` (one JSON document per session, atomic writes) |
| SQLite | missing | planned as a separate module |
| application-defined adapter | done | implement `session.Store` |

## G-F04 — Context Runtime `[partial]`

Package: `modelctx`; contract in root `context.go`.

| Item | Status | Where |
|---|---|---|
| ContextBuilder interface | done | `gotato.ContextBuilder`, `gotato.ContextBuilderFunc`, `gotato.ModelContext` |
| full-history strategy | done | `modelctx.FullHistory()` (the agent default) |
| window strategy | done | `modelctx.Window(n)` aligned to user-message boundaries |
| compacted strategy | done | `modelctx.Compact` rewrites the Session; any strategy then sees the summary |
| summary + recent strategy | done | `modelctx.SummaryRecent(keep, summarizer)` — projection only |
| selected-reference projection | missing | planned |
| custom application strategy | done | implement `gotato.ContextBuilder`; `modelctx.Chain` |
| CLI strategy spec | done | `modelctx.Parse("full" \| "window:N" \| "summary:N")` |

## G-F05 — Context Inspection `[done]`

| Item | Status | Where |
|---|---|---|
| source session state | done | `modelctx.Report.SourceMessages`, `InspectSession` |
| selected messages | done | `Report.SelectedMessages`, `Report.Context.Messages` |
| compaction state | done | `Report.Compactions` |
| approximate token usage | done | `Report.ApproxTokens` (bytes/4); provider-reported usage in `session.Run` |
| final model context | done | `Report.Context`; CLI `gotato context build` |

## G-F06 — Context Compaction `[partial]`

| Item | Status | Where |
|---|---|---|
| compaction trigger hooks | partial | explicit `modelctx.Compact` and `gotato context compact`; automatic trigger on transcript-size pressure is planned |
| summarizer interface | done | `modelctx.Summarizer`, `TruncateSummarizer`, `ModelSummarizer` |
| compacted segment metadata | done | `session.Compaction{At, ReplacedMessages, FromMessageID, ToMessageID, SummaryMessageID, Summarizer, BytesBefore, BytesAfter}` |
| explicit replacement/retention | done | `CompactOptions.Keep`; summary message tagged `metadata.compaction=summary`; tool calls never split from results |
| events | done | `session_compacted` recorded in the Session |

## G-F07 — Model Interface `[done]`

Root `model.go`: `Model.Stream`, `ModelRequest{SystemInstructions, Messages, Tools, Options}`, `ModelEvent` kinds text/reasoning/tool_call/usage/done; provider errors classified by core; opaque reasoning artifacts carried, never interpreted. Capability discovery is by optional interface.

## G-F08 — Provider Packages `[partial]`

`gateway`: OpenAI-compatible chat completions and OpenAI Codex Responses (SSE, retries, YAML config). A second, non-OpenAI provider is planned.

## G-F09 — Tool Interface `[done]`

Root `tool.go`, `toolfunc.go`: `Tool`, `ToolSpec`, `ToolUse`, `ToolResult` with status and safe error; schema-subset validation before execution; `NewFuncTool` derives schemas from Go structs.

## G-F10 — Tool Registry `[done]`

Package: `toolregistry`; contract `gotato.ToolSource`.

| Item | Status | Where |
|---|---|---|
| register/unregister | done | `Registry.Register/Unregister` |
| activate/deactivate | done | `Registry.Activate/Deactivate` |
| list/lookup/describe | done | `Registry.List/Lookup/Describe` |
| active set inspection | done | `Registry.Active()`; agent-side `gotato.ToolInspector.Tools()` |
| dynamic registration | done | agent refreshes every `ToolSource` at each Turn start |
| event hooks for tool-surface changes | done | `Registry.OnChange` |
| model-driven staged activation | done | `ToolSet` / `activate_toolset` |

## G-F11 — Standard Tool Packages `[missing]`

Only `demo.echo` and `time.now` in the CLI. Filesystem, shell, Git, and HTTP packages are planned as optional imports.

## G-F12 — MCP Integration `[missing]`

Planned. `gotato.ToolSet` and `gotato.ToolSource` are the adapter points.

## G-F13 — Runtime Events `[partial]`

Root `events.go` (payload keys documented per kind).

| Family | Status | Kinds |
|---|---|---|
| run lifecycle | done | `agent_start`, `agent_end` |
| turn lifecycle | done | `turn_start`, `turn_end` |
| context built/compacted | done | `context_built`, `session_compacted` |
| model request/response | done | `message_start`, `message_update`, `message_end` |
| tool lifecycle | done | `tool_execution_start/update/end`, `tool_result_committed`, `toolset_activated` |
| session updated | partial | recorded by `session.Recorder`; no standalone kind |
| usage | done | `turn_end.summary`, `RunResult.Usage` |
| error/cancellation | done | `agent_end` payload `status`, `error` |
| typed payloads | needs-refactor | payloads are `map[string]any` with documented keys |
| reasoning deltas | missing | `reasoning_update` planned |

## G-F14 — Streaming API `[done]`

`EventStream`, `EventSource.Subscribe`, protected/coalescable classes, terminal correlation via `agent_end`; subscriptions release their goroutine on close.

## G-F15 — Extensions / Hooks `[done]`

`ContextTransformer`, `MessageConverter`, `PreToolUse`, `PostToolUse`, `EventObserver`, `TurnStopper`, `AdvisoryExtension`; installed with `WithExtension(s)`. "Before finish" is `TurnStopper`; "before/after context build" is `ContextBuilder` followed by `ContextTransformer`.

## G-F16 — CLI: `gotato run` `[done]`

`run [--session ID] [--model echo|demo|gateway] [--instruction S] [--context SPEC] [--json | --events jsonl] [--continue] [--no-save] "prompt" | -`.

## G-F17 — CLI: Session Operations `[done]`

`session create | list | show | fork | events | resume | delete` with `--json`/`--jsonl`.

## G-F18 — CLI: Context Operations `[done]`

`context inspect | build | compact <session>` with `--json`, `--context`, `--keep`, `--summarizer`.

## G-F19 — CLI: Tool Operations `[done]`

`tools list | describe | active | activate | deactivate` with `--json`; activation is session state (`--session`).

## G-F20 — CLI: Event Inspection `[done]`

`events --session <id> [--kind K] [--json]` (JSONL by default); `run --events jsonl` streams live events and ends with a `run_result` line.

## G-F21 — CLI: `doctor` `[done]`

`doctor [--json]`: Go version, store writability and session count, models, gateway config presence and validity, builtin tools.

## G-F22 — Testing Package `[partial]`

`testkit`: `FakeModel`, `ReplayModel` (+ `LoadReplay`), `FakeTool`, `EventRecorder`, `NewSession`, `EchoModel`, `DemoModel`, `DemoEchoTool`. Failure injection helpers and context fixtures are planned.

## G-F23 — Scenario Testing `[partial]`

CLI scenario tests in `cmd/gotato/cli_test.go` run in-process and against the built binary, asserting on JSON. A fixture-driven scenario runner is planned.

## G-F24 — Examples `[missing]`

README snippets only. Examples for one-shot, persistent session, compaction, fork, dynamic tools, concurrent agents, and CLI automation are planned.

## G-F25 — Documentation as Runtime Contract `[partial]`

Root governance documents, package doc comments, `cmd/gotato/README.md` (exit codes, JSON fields), `MIGRATION.md`. `docs/` and `specs/` remain the earlier design record behind a banner.

---

## Optional Service Layer (built on the runtime)

| Package | Status | Note |
|---|---|---|
| `orchestration` | done | routing by agent name and conversation key, admission limits, retirement, drain, spawn groups |
| `host` | done | protocol-independent `Service` boundary and HTTP handlers |
| `adapter/grpc` | done, separate module | gRPC over `host.Service` |
| `cmd/gotato-agent` | done | HTTP reference daemon |

These packages depend on the runtime; the runtime never depends on them. Regrouping them under one `service/` umbrella, with a wire `ContractVersion` bump that removes the deprecated provenance fields from core types, is planned.
