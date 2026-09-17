# Gotato Features — Implementation Inventory

A living checklist of the standard runtime surface described by the whitepaper, mapped to packages and files. Update the marker in the same commit that changes the status.

Markers: `[done]` implemented and tested · `[partial]` usable with listed gaps · `[missing]` not started · `[needs-refactor]` exists but conflicts with DESIGN.md

---

## G-F01 — Agent Runtime `[done]`

Package: root `gotato` (`agent.go`, `toolbatch.go`, `limits.go`, `errors.go`).

| Item | Status | Where |
|---|---|---|
| reusable Agent configuration | done | `NewAgent(...Option)`; `WithModel`, `WithInstruction`, `WithTool(s)`, `WithToolSet`, `WithExtension(s)`, `WithLimits`, `WithDeadlines`, `WithTranscript`, `WithContextBuilder`, `WithToolSource` |
| run/turn execution | done | `coreAgent.executeRun` (one loop, one goroutine per agent) |
| tool-call loop | done | `preflightTools`, `executeToolGroup`, source-ordered commit |
| final result | done | `RunResult.FinalMessage` |
| termination reason | done | `RunResult.Status`, `RunResult.Error`, `agent_end` payload `status`/`stop_reason`/`stopped_by_extension` |
| usage aggregation | done | `RunResult.Usage`; per-run record in `session.Run` |
| cancellation/deadline propagation | done | `context.Context` through model, tools, extensions; `CoreLimits` deadlines derive contexts |
| control operations (continue/steer/follow-up/abort) | done | `ControllableAgent` |
| defaults inspectable | done | `DefaultLimits()` |

## G-F02 — Session `[done]`

Package: `session` (`session.go`).

| Item | Status | Where |
|---|---|---|
| session ID | done | `Session.ID` (random, stable across restarts) |
| message history | done | `Session.Messages()`; implements `gotato.Transcript` |
| model/tool interaction history | done | assistant messages with `ToolCalls`, `tool_result` messages |
| metadata | done | `Session.Metadata` (application-owned) |
| usage | done | `Session.Usage` (aggregated by `Recorder`) |
| runtime events or references | done | `Session.Events` bounded ring (default 1000) via `Recorder`; run records in `Session.Runs` |
| context/compaction metadata | done | `Session.Compactions` |
| create/load/save/close lifecycle | done | `Store.Create/Get/Save/Delete` |
| fork/branch | done | `session.Fork` records `ParentID` |

## G-F03 — Session Store Interfaces `[partial]`

| Item | Status | Where |
|---|---|---|
| store interface | done | `session.Store` |
| in-memory store | done | `session.MemoryStore` |
| file store | done | `session.FileStore` (one JSON document per session, `schema_version`) |
| SQLite | missing | Stage H, separate module |
| application-defined adapter | done | implement `session.Store` |

## G-F04 — Context Runtime `[partial]`

Package: `modelctx`; contract in root (`context.go`).

| Item | Status | Where |
|---|---|---|
| ContextBuilder interface | done | `gotato.ContextBuilder`, `gotato.ModelContext` |
| full-history strategy | done | `modelctx.FullHistory()` (also the agent default) |
| window strategy | done | `modelctx.Window(n)` (keeps tool_call/tool_result adjacency) |
| compacted strategy | done | compaction rewrites the Session; `FullHistory` then sees the summary |
| summary + recent strategy | done | `modelctx.SummaryRecent(keep)` — projection-only (no session mutation) |
| selected-reference projection | missing | Stage C follow-up |
| custom application strategy | done | implement `gotato.ContextBuilder`; `modelctx.Chain` |

## G-F05 — Context Inspection `[done]`

| Item | Status | Where |
|---|---|---|
| source session state | done | `modelctx.Inspect` → `Report.SourceMessages` |
| selected messages | done | `Report.SelectedMessages`, `Report.Messages` |
| compaction state | done | `Report.Compactions` (from Session) |
| approximate token usage | done | `Report.ApproxTokens` (bytes/4 heuristic; provider-reported usage lives in Session) |
| final model context | done | `Report.Context`; CLI `gotato context build` |

## G-F06 — Context Compaction `[partial]`

| Item | Status | Where |
|---|---|---|
| compaction trigger hooks | partial | explicit `modelctx.Compact` + CLI; automatic trigger on `MaxTranscriptBytes` pressure is Stage C follow-up |
| summarizer interface | done | `modelctx.Summarizer`, `TruncateSummarizer`, `ModelSummarizer` |
| compacted segment metadata | done | `session.Compaction{ReplacedMessages, FromMessageID, ToMessageID, SummaryMessageID, Summarizer}` |
| explicit replacement/retention | done | `Compact` options `Keep`; summary message tagged `metadata.compaction=summary` |
| events | done | `session_compacted` emitted through the Session's recorded events |

## G-F07 — Model Interface `[done]`

Root `model.go`: `Model.Stream`, `ModelRequest{SystemInstructions, Messages, Tools, Options}`, `ModelEvent` kinds text/reasoning/tool_call/usage/done, provider errors classified by core. Capability discovery is by optional interface (none defined yet).

## G-F08 — Provider Packages `[partial]`

`gateway`: OpenAI-compatible chat completions, OpenAI Codex Responses (Pi OAuth reuse). Second, non-OpenAI provider missing (Stage H).

## G-F09 — Tool Interface `[done]`

Root `tool.go`, `toolfunc.go`: `Tool`, `ToolSpec`, `ToolUse`, `ToolResult` with status/safe-error, schema-subset validation before execution, `NewFuncTool` schema derivation.

## G-F10 — Tool Registry `[done]`

Package: `toolregistry`; contract `gotato.ToolSource` (`context.go`).

| Item | Status | Where |
|---|---|---|
| register/unregister | done | `Registry.Register/Unregister` |
| activate/deactivate | done | `Registry.Activate/Deactivate` |
| list/lookup/describe | done | `Registry.List/Lookup/Describe` |
| active set inspection | done | `Registry.Active()`; agent-side `ToolInspector.Tools()` |
| dynamic registration | done | agent refreshes `ToolSource`s at each Turn start |
| event hooks for tool-surface changes | done | `Registry.OnChange` |
| model-driven staged activation | done | pre-existing `ToolSet` / `activate_toolset` |

## G-F11 — Standard Tool Packages `[missing]`

Only demo tools in the CLI (`demo.echo`, `time.now`). Filesystem/shell/git/http are Stage H.

## G-F12 — MCP Integration `[missing]`

Stage H. `gotato.ToolSet` / `ToolSource` are the intended adapter points.

## G-F13 — Runtime Events `[partial]`

Root `events.go`.

| Family | Status | Kinds |
|---|---|---|
| run lifecycle | done | `agent_start`, `agent_end` |
| turn lifecycle | done | `turn_start`, `turn_end` |
| context built/compacted | done | `context_built`, `session_compacted` |
| model request/response | done | `message_start`, `message_update`, `message_end` (assistant) |
| tool requested/started/completed | done | `tool_execution_start/update/end`, `tool_result_committed`, `toolset_activated` |
| session updated | partial | `session.Recorder` records; no standalone event kind |
| usage | done | in `turn_end.summary` and `RunResult` |
| error/cancellation | done | `agent_end` payload `status`, `error` |
| typed payloads | needs-refactor | payloads are `map[string]any`; keys documented in `events.go` |
| reasoning deltas | missing | no `reasoning_update` yet |

## G-F14 — Streaming API `[done]`

`EventStream`, `EventSource.Subscribe`, protected/coalescable classes, terminal correlation via `agent_end`. Subscription goroutine leak fixed on this branch.

## G-F15 — Extensions / Hooks `[done]`

`ContextTransformer`, `MessageConverter`, `PreToolUse`, `PostToolUse`, `EventObserver`, `TurnStopper`, `AdvisoryExtension`; installed with `WithExtension(s)`. "Before finish" = `TurnStopper`; "before/after context build" = `ContextBuilder` + `ContextTransformer`.

## G-F16 — CLI: `gotato run` `[done]`

`cmd/gotato`: `run [--session ID] [--model echo|demo|gateway] [--json] [--events jsonl] [--context full|window:N|summary:N] [--timeout D] "prompt"`.

## G-F17 — CLI: Session Operations `[done]`

`session create|list|show|fork|events|resume` with `--json`/`--jsonl`.

## G-F18 — CLI: Context Operations `[done]`

`context inspect|build|compact <session>` with `--json`, `--context`, `--keep`.

## G-F19 — CLI: Tool Operations `[done]`

`tools list|describe|active|activate|deactivate` with `--json`; activation is stored per session (`--session`) or shown for the default registry.

## G-F20 — CLI: Event Inspection `[done]`

`events --session <id> --jsonl`; `run --events jsonl` streams live events to stdout as JSONL (result then goes to stderr unless `--json`, see CLI README).

## G-F21 — CLI: `doctor` `[done]`

`doctor --json`: Go version, store path writability, session count, configured models, gateway config presence/validity, builtin tools.

## G-F22 — Testing Package `[partial]`

`testkit`: `FakeModel`, `ReplayModel`, `FakeTool`, `EventRecorder`, `NewSession`, `EchoModel`, `DemoModel`. Missing: `FailureInjector`, context fixtures.

## G-F23 — Scenario Testing `[partial]`

CLI scenario tests in `cmd/gotato/cli_test.go` (build binary, run deterministic flows, assert JSON). No fixture-driven scenario runner yet.

## G-F24 — Examples `[missing]`

README snippets only. Stage H.

## G-F25 — Documentation as Runtime Contract `[partial]`

Root governance docs, package doc comments, `cmd/gotato/README.md` (exit codes, JSON fields). `docs/` and `specs/` still carry the pre-whitepaper narrative with a banner.

---

## Optional Service Layer (not runtime foundation)

| Package | Status | Note |
|---|---|---|
| `orchestration` | done, isolated | routing/admission/retirement/spawn groups built on `gotato.Agent`; application-side scheduler (G-N03) |
| `host` | done, isolated | `Service` boundary + HTTP handlers |
| `adapter/grpc` | done, isolated, separate module | gRPC over `host.Service` |
| `cmd/gotato-agent` | done | HTTP reference daemon |
| planned move to `service/` | next | Stage H with `MIGRATION.md` |
