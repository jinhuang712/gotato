# Gotato Proposal

**Constitution:** [PHILOSOPHY.md](PHILOSOPHY.md) · [DESIGN.md](DESIGN.md) · [GOALS.md](GOALS.md)
**Inventory:** [FEATURES.md](FEATURES.md) · **Working notes:** [REFACTOR_AUDIT.md](REFACTOR_AUDIT.md), [REFACTOR_PLAN.md](REFACTOR_PLAN.md)

---

## 1. What Gotato Is

> **Gotato is a minimalistic, composable Go agent runtime.**

Gotato provides the standard runtime primitives needed to build agentic applications in Go without prescribing what those applications must become: **Agent, Session, Context, Model, Tool, Tool Registry, Event, Extension, Provider, Persistence, CLI, and Testing.**

It is deliberately broader than a single agent loop and deliberately smaller than an application framework. A useful Gotato installation gives an application every primitive it would otherwise rebuild, and nothing that tells the application what to be. It has no built-in UI. It defines no agent organization: there are no Masters, Operators, Workers, supervisors, or sub-agents inside Gotato, only agents.

## 2. Why the Runtime Is Shaped This Way

Every agentic application needs the same things: a loop that alternates model calls and tool executions, a record of what happened, a decision about what the model should see now, a registry of capabilities, structured facts about execution, and a way to test all of it without paying a provider. When each application rebuilds these, they diverge in subtle ways and cannot be composed.

Gotato owns exactly that set. The constraint that keeps it small is conceptual: each primitive answers one question and nothing else.

| Primitive | Question it answers |
|---|---|
| Agent | who acts? |
| Session | what happened? |
| Context | what does the model see now? |
| Tool / Tool Registry | what can the agent do, and which of it is visible? |
| Event | what is happening, as structured facts? |
| Extension | how do I wrap the loop without replacing it? |
| Store | where does continuity live? |
| CLI | how do humans, scripts, and coding agents drive all of the above? |
| Testkit | how is any of this exercised deterministically? |

Anything that answers an application question (which agent should do this task, how many agents may run, where a project lives, what the UI shows) is built above the runtime.

## 3. Runtime Layers

```text
+----------------------------------------------------------------------+
|  APPLICATIONS                                                        |
|  cmd/gotato (CLI)   services   automation   tests   multi-agent apps |
+----------------------------------+-----------------------------------+
                                   |
+----------------------------------v-----------------------------------+
|  OPTIONAL SERVICE LAYER  (built on the runtime, never imported by it)|
|  orchestration/   host/   adapter/grpc/   cmd/gotato-agent/          |
+----------------------------------+-----------------------------------+
                                   |
+----------------------------------v-----------------------------------+
|  STANDARD RUNTIME                                                    |
|  session/        Session · Store · MemoryStore · FileStore · Fork    |
|  modelctx/       ContextBuilder strategies · Inspect · Compact       |
|  toolregistry/   Registry (register/unregister/list/activate)        |
|  testkit/        FakeModel · ReplayModel · FakeTool · EventRecorder  |
|  gateway/        provider adapters                                   |
+----------------------------------+-----------------------------------+
                                   |
+----------------------------------v-----------------------------------+
|  CORE  (root package `gotato`, standard library only)                |
|  Agent · Loop · Message · Model · Tool · ToolSet · Events            |
|  Extensions · Errors · Limits · Transcript · ContextBuilder          |
+----------------------------------------------------------------------+
```

### Package naming

The illustrative repository shape in the whitepaper spells the context package `context/` and the testing package `testing/`. Both names collide with the Go standard library in every file that also needs cancellation or `testing.T`, which would force an alias everywhere and break the "ordinary Go" principle. The packages are therefore **`modelctx`** ("what the model sees now") and **`testkit`**; the registry package is **`toolregistry`**. The whitepaper permits the spelling to differ; the concepts do not.

## 4. Agent, Session, and Context

```text
Session (session.Session)              "what happened"        append-only
   |  implements gotato.Transcript
   v
ContextBuilder (gotato.ContextBuilder) "what the model sees now"
   |  modelctx: FullHistory · WithStatic · WithPanel
   v
ModelContext { SystemInstructions, System[], Messages, Panel[] }
   |
   v  gotato.AssembleRequest
ModelRequest, laid out for prompt caching:
   ┌ system   instruction + static <blocks>        stable across Runs     ┐ breakpoint
   ├ tools    sorted ToolSpecs                      stable across Turns   ┤ breakpoint
   ├ history  m0 … m(n-1)                           append-only prefix    ┤ breakpoint
   └ tail     m(n) + <panel>dynamic blocks</panel>  changes every Turn    ┘
   |
   v
Agent (gotato.Agent) ---- Tool Registry (gotato.ToolSource / toolregistry.Registry)
   |
   v
Agentic Loop (one)  →  Transcript appends + Events (context_built carries prefix_hash)
```

- **The agent commits to a Transcript, not to itself.** `gotato.WithTranscript(session)` makes the agent append every committed message to the Session. Without the option the agent uses a private in-memory transcript, so the two-line embedded path stays two lines.
- **Within a Session, history is append-only and the Model sees all of it.** There is one selection strategy, full history. Sliding windows and per-Turn summaries are not offered: they rewrite the request prefix every Turn, which defeats provider prompt caches and hides history from the Model.
- **History shrinks only by compaction.** `modelctx.Compact` rewrites a Session prefix into one summary and records a `session.Compaction` naming what was replaced and what replaced it. `modelctx.AutoCompact` applies a token budget (`CompactPolicy{Ceiling, Floor}`) at the start of a Run, through the `RunPreparer` extension stage, the one point where no Turn is using the Transcript. A compaction costs one cache miss; every Turn until the next one hits.
- **Static first, dynamic last.** `WithStatic` puts stable content (project rules, resources) into the system prompt; `WithPanel` puts per-Turn content (time, cwd, referenced files, state) into a `<panel>` appended to the tail Message. The panel is never committed to the Session and never disturbs the prefix.
- **Three formats, three jobs.** Markdown for prose the model reads (instructions, static blocks, summaries); JSON for structured data (tool schemas, arguments, results, `<state>` blocks); XML tags for boundaries and provenance (`<resource path="…">`, `<panel>`), so injected content is cheaply separated from user text.
- **Only prompt-relevant bytes reach the provider.** `gotato.ForModel` strips message IDs, usage, stop reasons, and runtime metadata; tools are sorted; `CacheBreakpoints` are placed after system, after tools, and before the tail. `context_built` reports `prefix_hash`; two consecutive Turns with the same hash present an identical cacheable prefix.
- **Forking is a state operation.** `session.Fork` copies state and records the parent Session ID. That is lineage of data, not of agents.
- **Tools are visible per Turn.** The agent asks every `ToolSource` for its tools at each Turn boundary and keeps that set for the whole Turn.

No relationship in this design creates a sub-agent.

## 5. The CLI

`cmd/gotato` is the official runtime interface for humans, shell automation, and coding agents:

```text
gotato run [--session ID] [--model echo|demo|gateway] [--panel time,cwd] [--compact-ceiling N] [--json | --events jsonl] "prompt"
gotato session create | list | show | fork | events | resume | delete
gotato context inspect | build | compact <session>
gotato tools list | describe | active | activate | deactivate
gotato events --session <id> [--jsonl | --json]
gotato doctor [--json]
```

The CLI composes `session.FileStore`, `modelctx`, `toolregistry`, `testkit` models, and the `gateway` provider exactly as an application would. It contains no agent semantics of its own. Stdout carries data, stderr carries diagnostics, exit codes are documented, and every command has a machine-readable form. The contract is [cmd/gotato/README.md](cmd/gotato/README.md).

`cmd/gotato-agent` (the HTTP reference service) is an optional service-layer executable, not the runtime interface.

## 6. Dependency Direction

```text
cmd/gotato, cmd/gotato-agent, adapter/grpc, host, orchestration
        |  may import anything below
        v
session, modelctx, toolregistry, testkit, gateway
        |  import the root package (plus stdlib and their own narrow deps)
        v
gotato (root)
        |  imports the standard library only
        v
Go
```

The direction is enforced by `layering_test.go`: the root package has no non-stdlib imports, and standard runtime packages never import the service layer or the CLI. Optional integrations never become mandatory dependencies of core packages.

## 7. Principles for Evolving the Runtime

1. **Additive first.** New capability arrives as an option, an interface, or a package. Existing constructors and the two-method `Agent` interface keep working.
2. **Deprecate before removing.** A symbol that conflicts with the constitution is marked `// Deprecated:` with a replacement named, and removed only with a documented break.
3. **Document every break** in [MIGRATION.md](MIGRATION.md) with a before/after example.
4. **Clarity wins.** A confused abstraction is not preserved forever to avoid a version bump (DESIGN G-D30).
5. **Admission questions before new concepts.** Every proposed concept answers the eight questions in DESIGN.md §Governance before it lands.
6. **Deterministic tests or it does not exist.** A feature without a fake-model test and, when user-visible, a CLI scenario, is not complete.

## 8. Direction

The runtime foundation described above is in place. FEATURES.md is the authoritative inventory; the open items there define the direction:

- **Context**: provider adapters that map `CacheBreakpoints` to explicit cache controls; token estimation from provider usage instead of the bytes/4 heuristic.
- **Events**: typed payload structs per kind; `reasoning_update` for streaming reasoning deltas.
- **Persistence**: a SQLite-backed `session.Store` in its own module so the root stays dependency-free.
- **Tools**: MCP client as a `ToolSet`/`ToolSource`; optional filesystem, shell, and HTTP tool packages.
- **Providers**: a second, non-OpenAI adapter to keep the model contract provider-neutral.
- **Testkit**: failure injection and context fixtures; a fixture-driven scenario runner.
- **Service layer**: `orchestration`, `host`, `adapter/grpc`, and `cmd/gotato-agent` regrouped under one `service/` umbrella with a wire `ContractVersion` bump that also removes the deprecated provenance fields from core types.
- **Repository**: examples for one-shot, persistent session, compaction, fork, dynamic tools, concurrent agents, and CLI automation; CI with `gofmt`, `vet`, and `-race` for both modules; a license.

## 9. What Belongs Where

| Concern | Belongs in |
|---|---|
| the loop, messages, model/tool contracts, events, extensions, limits | core (`gotato`) |
| continuity, persistence, fork, compaction records | `session` |
| what the model sees now, inspection, compaction operation | `modelctx` |
| tool identity, visibility, activation | `toolregistry`, `ToolSet` |
| provider wire formats and credentials | `gateway` and future provider packages |
| deterministic doubles | `testkit` |
| human/script/agent operation | `cmd/gotato` |
| routing between agents, admission, retirement, remote exposure | optional service layer |
| roles, task graphs, project state, UI, global scheduling | the application, never Gotato |
