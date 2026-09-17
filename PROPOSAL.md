# Gotato Proposal — Runtime Foundation Refactor

**Status:** active refactor (branch `refactor/runtime-foundation`)
**Constitution:** [PHILOSOPHY.md](PHILOSOPHY.md), [DESIGN.md](DESIGN.md), [GOALS.md](GOALS.md)
**Checklist:** [FEATURES.md](FEATURES.md) · **Audit:** [REFACTOR_AUDIT.md](REFACTOR_AUDIT.md) · **Plan:** [REFACTOR_PLAN.md](REFACTOR_PLAN.md)

---

## 1. What Gotato Is

> **Gotato is a minimalistic, composable Go agent runtime.**

It provides the standard runtime primitives needed to build agentic applications in Go without prescribing what those applications must become: **Agent, Session, Context, Model, Tool, Tool Registry, Event, Extension, Provider, Persistence, CLI, and Testing**. It is broader than a single agent loop and smaller than an application framework. It has no built-in UI and no built-in agent organization.

## 2. Why This Refactor Exists

The repository before this refactor was titled "Go-native Agent Runtime and Orchestration". It contains a strong, well-tested agent core (one goroutine per agent, one canonical loop, tools, tool sets, extensions, structured events), but the code and narrative had grown in a direction the whitepaper rejects:

| Observation (see REFACTOR_AUDIT.md) | Whitepaper rule violated |
|---|---|
| The committed transcript is a private field of the agent (`coreAgent.messages`); there is no Session type, no store, no fork | G-D03, G-D04, G-D05, G-D06 |
| Model input is the transcript itself unless an extension rewrites it; no ContextBuilder, no inspection, no compaction | G-D07, G-D08 |
| The tool registry is unexported and mutable only by the agent goroutine; no public register/list/activate | G-D12 |
| The only executable is an HTTP daemon (`cmd/gotato-agent`); there is no CLI and no machine-readable runtime surface | G-D22, G-D23, G-D24 |
| Multi-agent routing, admission, retirement, HTTP host and gRPC adapter carry the product narrative ("Hosted Service") | G-D19, G-D20, G-N03, G-N05 |
| Test doubles are duplicated in three packages; no exported testing toolkit | G-D25 |
| `Event` carries `SpawnID`/`OriginRunID`, `types.go` defines `SpawnID`, and `orchestration/spawn.go` implements provenance trees | G-D18 (borderline: provenance metadata, not hierarchy, but it is in core types) |

None of this requires a rewrite. The core loop is exactly the loop G-D09 describes. The refactor re-centers the repository: keep the core, add the missing standard runtime primitives around it, demote the service layer to optional packages, and add the CLI and testing toolkit.

## 3. Target Runtime Layers

```text
+----------------------------------------------------------------------+
|  APPLICATIONS                                                        |
|  cmd/gotato (CLI)   services   automation   tests   Mow-like systems |
+----------------------------------+-----------------------------------+
                                   |
+----------------------------------v-----------------------------------+
|  OPTIONAL SERVICE LAYER (kept, isolated, never imported by core)     |
|  orchestration/   host/   adapter/grpc/   cmd/gotato-agent/          |
+----------------------------------+-----------------------------------+
                                   |
+----------------------------------v-----------------------------------+
|  STANDARD RUNTIME                                                    |
|  session/        Session · Store · MemoryStore · FileStore · Fork    |
|  modelctx/       ContextBuilder strategies · Compaction · Inspect    |
|  toolregistry/   Registry: register/unregister/list/activate         |
|  testkit/        FakeModel · ReplayModel · FakeTool · EventRecorder  |
|  gateway/        provider adapter (OpenAI-compatible, Codex)         |
+----------------------------------+-----------------------------------+
                                   |
+----------------------------------v-----------------------------------+
|  CORE  (root package `gotato`, stdlib only)                          |
|  Agent · Loop · Message · Model · Tool · ToolSet · Events            |
|  Extensions · Errors · Limits · Transcript · ContextBuilder          |
+----------------------------------------------------------------------+
```

### Package naming note

The whitepaper's illustrative shape uses `context/`. A Go package named `context` collides with the standard library in every file that also needs cancellation, forcing an alias everywhere and violating G-P06 (ordinary Go). The context-building package is therefore named **`modelctx`** ("what the model sees now"). Similarly the tool registry package is `toolregistry` and the testing package is `testkit` (`testing` collides with the standard library). Whitepaper §9 explicitly allows the spelling to differ.

## 4. Agent, Session, and Context in Code

```text
Session (session.Session)             "what happened"
   implements gotato.Transcript
        |
        v
ContextBuilder (gotato.ContextBuilder) "what should the model see now"
   strategies in modelctx/
        |
        v
ModelContext (gotato.ModelContext)     the one Turn's model view
        |
        v
Agent (gotato.Agent) ---- Tool Registry (gotato.ToolSource / toolregistry.Registry)
        |
        v
Agentic Loop (one, unchanged)
        |
        +---- model
        +---- tools
        v
Transcript appends + structured events (incl. context_built)
```

- `gotato.NewAgent(gotato.WithTranscript(session))` makes the agent commit to a Session instead of a private slice. The default remains a private in-memory transcript, so the two-line embedded path is unchanged.
- `gotato.WithContextBuilder(modelctx.Window(20))` decides the model view per Turn. The default is full history, which is what happened before; the difference is that it is now explicit, inspectable, and emits a `context_built` event.
- `modelctx.Compact(...)` rewrites a Session prefix into a summary and records a `session.Compaction`. Compaction is a Session state operation; it never happens silently inside the loop.
- `session.Fork(...)` creates a new Session from an existing one and records the parent ID. That is lineage of state, not of agents.

No relationship in this design creates a sub-agent.

## 5. The Role of the CLI

`cmd/gotato` is the official runtime interface for humans, scripts, and coding agents:

```text
gotato run [--session ID] [--model echo|demo|gateway] [--json|--events jsonl] "prompt"
gotato session create|list|show|fork|events|resume
gotato context inspect|build|compact <session>
gotato tools list|describe|active|activate|deactivate
gotato events --session <id> --jsonl
gotato doctor --json
```

The CLI composes `session.FileStore`, `modelctx`, `toolregistry`, `testkit` models, and the `gateway` provider exactly as an application would. It contains no agent semantics of its own. It is how later refactor stages are tested without writing Go.

`cmd/gotato-agent` (the HTTP reference service) stays as an optional service-layer executable.

## 6. Package and Dependency Direction

```text
cmd/gotato, cmd/gotato-agent, adapter/grpc, host, orchestration
        |  may import anything below
        v
session, modelctx, toolregistry, testkit, gateway
        |  import the root package only (plus stdlib and their own narrow deps)
        v
gotato (root)
        |  imports the standard library only
        v
Go
```

Enforced by review and by a test (`layering_test.go`) that asserts the root package has no intra-module imports and that standard runtime packages do not import the service layer.

## 7. Migration Principles

1. **Additive first.** New primitives are added as options and packages; existing constructors and the two-method `Agent` interface keep working.
2. **Compatibility adapters where cheap.** `internal/testmodel` is replaced by `testkit` (internal, so no external compatibility concern); everything exported in the root package remains.
3. **Deprecate before removing.** Symbols that conflict with the whitepaper (`SpawnID`, `Event.SpawnID`, `Event.OriginRunID`) are marked deprecated and scheduled for removal in Stage H, not removed now, because `orchestration` and the gRPC wire contract use them.
4. **Document every break** in `MIGRATION.md`.
5. **Do not preserve confusion forever.** When Stage H moves the service layer under one directory, the move is a documented break with a `replace`-friendly path.

## 8. Refactor Phases

| Stage | Content | Status |
|---|---|---|
| A | boundaries: layering test, `DefaultLimits()`, `Transcript`/`ContextBuilder`/`ToolSource` contracts in core, fix known core defects that block the new primitives (empty-message validation, subscription goroutine leak, per-commit whole-transcript re-serialization) | this branch |
| B | `session/`: Session, Store, MemoryStore, FileStore (JSONL), Fork, event/usage recorder | this branch |
| C | `modelctx/`: FullHistory, Window, SummaryRecent, Chain, Inspect, Compact + summarizers; `context_built` event | this branch |
| D | `toolregistry/`: Registry with register/unregister/lookup/list/describe/activate/deactivate; `WithToolSource`; `ToolInspector` | this branch |
| E | events: `context_built`, `session_compacted`; event recorder; documented payload keys | this branch (partial: typed payloads deferred) |
| F | `cmd/gotato` CLI with `--json`/`--jsonl`, documented exit codes | this branch |
| G | `testkit/` | this branch |
| H | optional packages: move `orchestration`/`host`/`adapter`/`cmd/gotato-agent` under a `service/` umbrella, remove deprecated `SpawnID`, MCP tool set, second provider, SQLite store, standard tools | next |

## 9. What Is Preserved, Moved, or Discarded

**Preserved as is (core):** `Agent`, `NewAgent`, all `With*` options, `Message`/`ContentPart`/`ToolCall`/`ToolResult`, `Model`/`ModelStream`/`ModelEvent`, `Tool`/`ToolSpec`/`ToolUse`, `ToolSet`/`WithToolSet`/`WithActiveToolSet`, `NewFuncTool`/`WithFunc`, all extension interfaces, `Event`/`EventStream`/`LifecycleEvent`, `RuntimeError`/`ErrorCode`, `CoreLimits`, `ControllableAgent` (Continue/Steer/FollowUp/Abort), `RunCanceler`, `IdleWaiter`.

**Preserved as optional service layer:** `orchestration`, `host`, `adapter/grpc`, `cmd/gotato-agent`, `gateway`. Their tests continue to run. They are documented as "built on the runtime", not as the runtime.

**Moved:** `internal/testmodel` → `testkit` (exported).

**Deprecated now, removed later:** `SpawnID`, `Event.SpawnID`, `Event.OriginRunID` (core types carrying orchestration provenance).

**Discarded:** nothing functional. The old product narrative in `README.md`, `docs/README.md`, and `specs/README.md` is replaced by the whitepaper's; `docs/` and `specs/` are retained as historical design records with a banner pointing to the root documents.
