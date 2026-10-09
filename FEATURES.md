# Gotato Features — Implementation Inventory

The standard runtime surface, mapped to packages and files. Update the marker in the same commit that changes the status.

Markers: `[done]` implemented and tested · `[partial]` usable with listed gaps · `[missing]` not started · `[needs-refactor]` exists but conflicts with DESIGN.md. Row status `application` names a seam an application implements; it stays off Gotato's roadmap.

---

## G-F01 — Agent Runtime `[done]`

Package: root `gotato` (`agent.go`, `toolbatch.go`, `context.go`, `limits.go`, `errors.go`).

| Item | Status | Where |
|---|---|---|
| reusable Agent configuration | done | `NewAgent(...Option)` returns `RuntimeAgent` (Agent + control + lifecycle + events + inspection); `WithModel`, `WithInstruction`, `WithTool(s)`, `WithToolSet`, `WithToolSource`, `WithTranscript`, `WithContextBuilder`, `WithExtension(s)`, `WithLimits`, `WithDeadlines` |
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

## G-F03 — Session Store Interfaces `[done]`

| Item | Status | Where |
|---|---|---|
| store interface | done | `session.Store` (`Save`, `Get`, `List`, `Delete`) |
| in-memory store | done | `session.MemoryStore` |
| file store | done | `session.FileStore` (one JSON document per session, atomic writes) |
| database-backed store | application | the application implements `session.Store` with the database it already runs |

## G-F04 — Context Runtime `[done]`

Package: `modelctx`; contract in root `context.go`.

Within a Session the Model sees the whole history, append-only, laid out for prompt caching: static system content first, tools next, history as an append-only prefix, a dynamic `<panel>` on the tail Message. History shrinks only through compaction. Full history is the one built-in selection strategy because a stable prefix keeps provider prompt caches warm; other projections are application `ContextBuilder`s.

| Item | Status | Where |
|---|---|---|
| ContextBuilder interface | done | `gotato.ContextBuilder`, `gotato.ContextBuilderFunc`, `gotato.ModelContext{SystemInstructions, System, Messages, Panel, Metadata}` |
| full-history strategy | done | `modelctx.FullHistory()` (the agent default) |
| static blocks in the system prompt | done | `modelctx.WithStatic`, `modelctx.Resource/Text/JSON`, `gotato.Block`, `gotato.RenderBlocks` |
| dynamic panel on the tail | done | `modelctx.WithPanel`, `modelctx.Time`; rendered by `gotato.AssembleRequest` into the last Message, never committed |
| compacted history | done | `modelctx.Compact` rewrites the Session; `modelctx.AutoCompact` applies a `CompactPolicy` budget at Run start via `gotato.RunPreparer` |
| cache-friendly request layout | done | `gotato.AssembleRequest`: sorted tools, `ForModel` strips runtime fields, `CacheBreakpoints` after system / tools / before the tail, `prefix_hash` |
| selected-reference projection | done | `modelctx.Resource` blocks in static or panel position |
| other context organization | application | the application implements `gotato.ContextBuilder` |

## G-F05 — Context Inspection `[done]`

| Item | Status | Where |
|---|---|---|
| source session state | done | `modelctx.InspectSession` → `Report.SourceMessages` |
| final model context | done | `Report.Context`, `Report.Request` (exactly what the Agent sends for the committed Session); `InspectOptions` overrides; CLI `gotato context inspect/build --panel/--instruction` |
| compaction state | done | `Report.Compactions` |
| approximate token usage | done | `Report.ApproxTokens`, `modelctx.EstimateTokens` (bytes/4); provider-reported usage in `session.Run` |
| cache prefix | done | `Report.PrefixHash`, `Report.SystemBytes`, `Report.PanelBytes`; `context_built` payload `prefix_hash` |

## G-F06 — Context Compaction `[done]`

| Item | Status | Where |
|---|---|---|
| compaction trigger hooks | done | explicit `modelctx.Compact` / `gotato context compact`; automatic `modelctx.AutoCompact(session, CompactPolicy{Ceiling, Floor})` at Run start |
| summarizer interface | done | `modelctx.Summarizer`, `TruncateSummarizer`, `ModelSummarizer` |
| compacted segment metadata | done | `session.Compaction{At, ReplacedMessages, FromMessageID, ToMessageID, SummaryMessageID, Summarizer, BytesBefore, BytesAfter}` |
| explicit replacement/retention | done | `CompactOptions.Keep`; summary tagged `metadata.compaction=summary`; the cut prefers a user Message but may land on a non-user boundary that still keeps tool calls with their results (`safeCut`); a no-op returns a fully populated non-replaced `Result` and omits `compaction` |
| events | done | `session_compacted` recorded in the Session |

## G-F07 — Model Interface `[done]`

Root `model.go`: `Model.Stream`, `ModelRequest{SystemInstructions, Messages, Tools, Options}`, `ModelEvent` kinds text/reasoning/tool_call/usage/done; provider errors classified by core; opaque reasoning artifacts carried, never interpreted. Capability discovery is by optional interface.

| Item | Status | Where |
|---|---|---|
| provider rate-limit information | missing | planned: limits, remaining, and reset reported with model usage so host services apply their own throttling; `gateway` uses `Retry-After` only for its own retries today |

## G-F08 — Provider Packages `[partial]`

| Item | Status | Where |
|---|---|---|
| OpenAI Chat Completions and Responses | done | `gateway` (SSE, retries, YAML config, API-key auth) |
| Anthropic Messages | missing | planned in `gateway` on the standard library like the OpenAI adapters, mapping `CacheBreakpoints` to Anthropic cache control |

## G-F09 — Tool Interface `[partial]`

Root `tool.go`, `toolfunc.go`.

| Item | Status | Where |
|---|---|---|
| Tool contract | done | `Tool`, `ToolSpec`, `ToolUse`, `ToolResult` with status and safe error; schema-subset validation before execution |
| Go function tools | done | `NewFuncTool` derives schemas from Go structs |
| effect classification | missing | planned field on `ToolSpec`: `read`, `write_local`, `write_external`, `destructive`, ordered by impact; applications filter the tools an agent loads by it |

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

## G-F11 — Tool Supply `[done]`

Tools come from MCP servers (G-F12) and from applications through the `Tool`, `ToolSet`, and `ToolSource` contracts; `NewFuncTool` turns a Go function into a Tool. Filesystem, shell, Git, and HTTP tools belong to MCP servers or the application. The CLI ships `demo.echo` and `time.now` for diagnostics.

## G-F12 — MCP Integration `[missing]`

Planned as an optional package that adapts MCP servers through `gotato.ToolSet` and `gotato.ToolSource`: client lifecycle, server configuration, tool discovery, lazy connection, and dynamic Tool Registry updates. MCP tool annotations (`readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`) map onto the tool effect field (G-F09). MCP is how standalone Gotato users reach filesystem, shell, and other tools.

## G-F13 — Runtime Events `[partial]`

Root `events.go` (payload keys documented per kind).

| Family | Status | Kinds |
|---|---|---|
| run lifecycle | done | `agent_start`, `agent_end` |
| turn lifecycle | done | `turn_start`, `turn_end` |
| context built/compacted | done | `context_built` (with `prefix_hash`, `panel_bytes`, `system_bytes`), `session_compacted` |
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

`ContextTransformer`, `MessageConverter`, `PreToolUse`, `PostToolUse`, `EventObserver`, `TurnStopper`, `RunPreparer`, `AdvisoryExtension`; installed with `WithExtension(s)`. "Before finish" is `TurnStopper`; "before/after context build" is `ContextBuilder` followed by `ContextTransformer`; `RunPreparer` is the sanctioned point to rewrite the Transcript before a Run.

## G-F16 — CLI: `gotato run` `[done]`

`run [--session ID] [--model echo|demo|gateway] [--instruction S] [--panel time,cwd] [--compact-ceiling N] [--timeout D] [--json | --events jsonl] [--continue] [--no-save] "prompt" | -`. The CLI drives a `service.Runner` in-process: the same code path as `gotato serve` and `gotato-grpc`.

## G-F17 — CLI: Session Operations `[done]`

`session create | list | show | fork | events | resume | delete` with `--json`/`--jsonl`.

## G-F18 — CLI: Context Operations `[done]`

`context inspect | build | compact <session>` with `--json`, `--panel`, `--keep`, `--summarizer`; `inspect` reports the exact request, `prefix_hash`, and byte sizes.

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

README snippets only. Examples for one-shot, persistent session, compaction, fork, dynamic tools, MCP integration, concurrent agents, and CLI automation are planned. Each demonstrates composition; none presents one blessed application architecture.

## G-F25 — Documentation as Runtime Contract `[partial]`

Root governance documents, package doc comments, `cmd/gotato/README.md` (exit codes, JSON fields), `MIGRATION.md`. Goal: a coding agent can discover the intended semantics of every public package and CLI command from the repository itself.

---

## Service (the runtime as a service) `[done]`

| Item | Status | Where |
|---|---|---|
| Runner: Session store + Agent per Run | done | `service.Runner`, `service.AgentSpec`, `RunRequest`, `RunResult` |
| per-Session single flight | done | `service.Admission.Queue` (`reject` → busy, `wait` → queue); every Session mutation (run, compact, tool activation, delete) goes through one lock |
| capacity bound, drain | done | `Admission.MaxActiveRuns`, `Runner.Drain` (cancels started and queued Runs after grace); `Draining()` backs `/readyz` 503 |
| cancellation | done | `Runner.CancelRun(runID)`, `Runner.CancelSession(id)` via `Agent.Abort` so the settled result is returned |
| per-run deadline | done | `RunRequest.Timeout` → `RunDeadline`; settles as `deadline_exceeded` |
| Session settings honored per run | done | metadata `gotato.agent`, `gotato.instruction`, `gotato.panel`, `gotato.compact_ceiling`, `gotato.tool.<id>` |
| inspection / compaction / fork | done | `Runner.Inspect`, `Runner.Compact` (under the Session lock), `Runner.Fork`, `Runner.Tools`, `Runner.SetToolActive` |
| HTTP adapter | done | `service/httpapi` (`ContractVersion "2"`): sessions, runs, SSE stream, events, context, compact, cancel, delete; a settled failed Run is 200 with the outcome, a lost save is 500 |
| gRPC adapter | done | `adapter/grpc` module, `gotato.v2.SessionService`, `gotato-grpc` binary |
| `gotato serve` | done | the CLI runs the same Runner behind `httpapi` |
| multi-process Session lease | missing | the Session lock is process-local; a Store-level lease is planned for multi-replica deployments |
| middleware (auth, logging) | by design | wrap the `http.Handler` / use gRPC interceptors; the adapters carry none |

The service depends on the runtime; the runtime never depends on the service (`layering_test.go`).
