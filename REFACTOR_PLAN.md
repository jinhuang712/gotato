# Refactor Plan

Incremental migration from the audited state ([REFACTOR_AUDIT.md](REFACTOR_AUDIT.md)) to the whitepaper architecture ([PROPOSAL.md](PROPOSAL.md)). Each stage is independently testable and leaves `go test ./...` green. Status is updated as stages land; feature-level status lives in [FEATURES.md](FEATURES.md).

Legend: `[done]` landed on this branch · `[partial]` landed with listed gaps · `[next]` not started.

---

## Stage A — Establish Boundaries `[done]`

Goal: make the core the core, and make the seams the new primitives need.

- [x] Governance docs (`PHILOSOPHY`, `DESIGN`, `GOALS`, `FEATURES`, `PROPOSAL`, `AGENTS`, `GITFLOW`) + audit + this plan.
- [x] `layering_test.go`: root package imports stdlib only; `session`/`modelctx`/`toolregistry`/`testkit` never import `orchestration`/`host`/`adapter`/`cmd`.
- [x] Export `DefaultLimits()`; document `WithLimits` zero semantics (fixes TODO T04 ergonomics without changing semantics).
- [x] Core contracts: `Transcript`, `ContextBuilder` + `ModelContext`, `ToolSource`, `ToolInspector`. Options `WithTranscript`, `WithContextBuilder`, `WithToolSource`.
- [x] Fix core defects that the new primitives would otherwise inherit: empty-part prompt validation (T02), event subscription goroutine leak (T05), whole-transcript re-serialization per commit (T07 → one pass per Run, incremental per commit).
- [x] Remove dead `modelStreamDone`; deprecate `SpawnID`/`Event.SpawnID`/`Event.OriginRunID`.

Exit: all pre-existing tests pass unchanged except those asserting exact event sequences (updated for `context_built`).

## Stage B — First-Class Session `[done]`

- [x] `session.Session`: ID, CreatedAt/UpdatedAt, ParentID, Messages, Runs, Events (bounded), Usage, Compactions, Metadata; thread-safe; implements `gotato.Transcript`.
- [x] `session.Store` interface: Create/Get/Save/List/Delete; `MemoryStore`; `FileStore` (one JSON file per session, `schema_version`).
- [x] `session.Fork(parent)` copies state and records lineage.
- [x] `session.Recorder`: `EventObserver` that appends run records, events, and usage into the Session.
- [x] Tests: agent commits into a Session, reload from FileStore and continue a run, fork independence.

Exit: `gotato run --session <id>` twice against the file store continues the same history.

## Stage C — First-Class Context `[done]`

- [x] `modelctx.FullHistory()`, `Window(n)`, `SummaryRecent(keep)`, `Chain(...)`; each returns `gotato.ModelContext` with `Metadata` (`strategy`, `source_messages`, `selected_messages`, `dropped_messages`).
- [x] `modelctx.Inspect(builder, snapshot)` report with approximate tokens.
- [x] `modelctx.Compact(ctx, session, opts)` with `Summarizer` interface; `TruncateSummarizer` (deterministic) and `ModelSummarizer` (uses a `gotato.Model`); records `session.Compaction`; keeps tool_call/tool_result adjacency intact.
- [x] Loop emits `context_built` (protected) per Turn with the builder metadata.
- [ ] Selected-reference projection (attach resources by ID) — `[next]`.

Exit: `gotato context inspect <id> --json` shows source vs selected messages; `gotato context compact <id>` shrinks the session and records a compaction.

## Stage D — Tool Registry `[done]`

- [x] `toolregistry.Registry`: Register/Unregister/Lookup/List/Describe/Activate/Deactivate/Active/OnChange; implements `gotato.ToolSource`.
- [x] Agent refreshes `ToolSource`s at each Turn start; lookup within a Turn uses the Turn's set.
- [x] `ToolInspector.Tools()` on the agent exposes the currently visible `ToolSpec`s.
- [x] Existing static `WithTool(s)` and `ToolSet` activation unchanged.

Exit: `gotato tools list/describe/active/activate/deactivate` operate on a registry; activation state for a session is stored in session metadata and honored by `gotato run --session`.

## Stage E — Structured Events and Streaming `[partial]`

- [x] New kinds `context_built`, `session_compacted`; documented payload keys in `events.go`.
- [x] `testkit.EventRecorder`.
- [x] `gotato events --session <id> --jsonl` and `gotato run --events jsonl`.
- [ ] Typed payload structs per kind (TODO A06) — `[next]`, breaking for the gRPC `payload_json`; bundle with Stage H ContractVersion bump.
- [ ] `reasoning_update` coalescable event — `[next]`.

## Stage F — Official CLI `[done]`

`cmd/gotato`, stdlib `flag` only, thin over the packages:

```text
run, session {create,list,show,fork,events,resume}, context {inspect,build,compact},
tools {list,describe,active,activate,deactivate}, events, doctor
```

- [x] `--json` / `--jsonl` / `--quiet` / `--timeout`; no color anywhere so `--no-color` is accepted and a no-op.
- [x] stdout = data, stderr = diagnostics; exit codes 0/1/2/3/4 documented in `cmd/gotato/README.md`.
- [x] Store location: `--store DIR`, `GOTATO_HOME`, default `~/.gotato/sessions`.
- [x] Models: `echo`, `demo` (deterministic, from `testkit`), `gateway` (from `gateway` YAML).
- [x] CLI scenario tests build the binary and assert on JSON.

## Stage G — Testing Toolkit `[done]`

- [x] `testkit`: `FakeModel` (scripted per call), `ReplayModel` (from recorded events / JSON), `FakeTool`, `EventRecorder`, `NewSession` fixture, `EchoModel`, `DemoModel` (moved from `internal/testmodel`).
- [ ] `FailureInjector` helpers, context fixtures — `[next]`.

## Stage H — Optional Standard Packages `[next]`

Only after the foundation above is stable:

1. Move `orchestration`, `host`, `adapter/grpc`, `cmd/gotato-agent` under `service/` (or a separate module) with a `MIGRATION.md` entry; remove `SpawnID`/`Event.SpawnID`/`Event.OriginRunID` from core and bump the wire `ContractVersion`.
2. `session/sqlite` store (behind its own module to keep the root dependency-free).
3. MCP tool set (`mcp/`) implemented as a `gotato.ToolSet` / `ToolSource`.
4. A second provider adapter (Anthropic Messages API) to validate provider neutrality.
5. Standard tool packages (`tools/fs`, `tools/shell`) as optional imports.
6. `examples/` covering one-shot, persistent session, compaction, fork, dynamic tools, concurrent agents, CLI automation.
7. Rewrite `docs/` and `specs/` to the whitepaper vocabulary; conformance matrix (TODO A09).
8. CI workflow: gofmt, vet, `go test -race` for both modules; LICENSE.

## Acceptance Commands

```bash
gofmt -l . && go vet ./... && go test ./...
(cd adapter/grpc && go test ./...)
go build -o bin/gotato ./cmd/gotato
./bin/gotato doctor --json
id=$(./bin/gotato session create --json | jq -r .id)
./bin/gotato run --session "$id" --model demo --json "use-tool"
./bin/gotato run --session "$id" --model demo --json "hello again"
./bin/gotato context inspect "$id" --json
./bin/gotato context compact "$id" --keep 2 --json
./bin/gotato events --session "$id" --jsonl | wc -l
./bin/gotato tools list --json
```
