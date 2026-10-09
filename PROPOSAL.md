# Gotato Proposal

**Constitution:** [PHILOSOPHY.md](PHILOSOPHY.md) · [DESIGN.md](DESIGN.md) · [GOALS.md](GOALS.md)
**Inventory:** [FEATURES.md](FEATURES.md) · **Open work:** [TODO.md](TODO.md)

---

## 1. What Gotato Is

> **Gotato is a minimal, synchronous, Go-native agent runtime.**

It is useful on its own and embeds into running Go services deployed across instances; the three defining criteria and the feature test are in [GOALS.md](GOALS.md).

Gotato provides the runtime primitives an agentic Go program needs: **Agent, Session, Context, Model, Tool, Tool Registry, Event, Extension, Provider, Persistence, CLI, and Testing.** It is broader than a single agent loop and smaller than an application framework. UI, agent organization, and product semantics live in applications; inside Gotato every agent is a peer.

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

| Layer | Package | Contents |
|---|---|---|
| core | `gotato` | Agent, loop, Message, Model, Tool, ToolSet, Transcript, ContextBuilder, ToolSource, Events, Extensions, Errors, Limits. Standard library only. |
| standard runtime | `session` | Session, Store, MemoryStore, FileStore, Fork, Recorder |
| | `modelctx` | FullHistory, WithStatic, WithPanel, blocks, Inspect, Compact, AutoCompact, summarizers |
| | `toolregistry` | Registry (register, unregister, lookup, list, describe, activate, deactivate, change hooks) |
| | `testkit` | FakeModel, ReplayModel, FakeTool, EventRecorder, session fixtures, EchoModel, DemoModel |
| providers | `gateway` | OpenAI-compatible Chat Completions and Responses adapters (API key), YAML config |
| service | `service` | `Runner`: Session store, Agent per Run, AgentSpecs, per-Session single flight, admission, cancellation, drain |
| | `service/httpapi` | HTTP adapter over the Runner |
| | `adapter/grpc` (module) | gRPC adapter over the Runner and the `gotato-grpc` binary |
| CLI | `cmd/gotato` | `run`, `session`, `context`, `tools`, `events`, `doctor`, `serve` |

Applications sit above all layers. Dependencies point inward (DESIGN.md G-D27).

### Package naming

The context package is **`modelctx`** ("what the model sees now") and the testing package is **`testkit`**, so neither collides with the standard library's `context` or `testing` in files that need both; the registry package is **`toolregistry`**.

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

- **The agent commits to a Transcript.** `gotato.WithTranscript(session)` makes the agent append every committed message to the Session. Without the option the agent uses a private in-memory transcript, so the two-line embedded path stays two lines.
- **Within a Session, history is append-only and the Model sees all of it.** Full history is the one built-in selection strategy because it keeps the request prefix stable for provider prompt caches; other projections are application `ContextBuilder`s.
- **History shrinks only by compaction.** `modelctx.Compact` rewrites a Session prefix into one summary and records a `session.Compaction` naming what was replaced and what replaced it. `modelctx.AutoCompact` applies a token budget (`CompactPolicy{Ceiling, Floor}`) at the start of a Run, through the `RunPreparer` extension stage, the one point where no Turn is using the Transcript. A compaction costs one cache miss; every Turn until the next one hits.
- **Static first, dynamic last.** `WithStatic` puts stable content (project rules, resources) into the system prompt; `WithPanel` puts per-Turn content (time, cwd, referenced files, state) into a `<panel>` appended to the tail Message. The panel lives only in the request, so the Session and the prefix stay unchanged.
- **Three formats, three jobs.** Markdown for prose the model reads (instructions, static blocks, summaries); JSON for structured data (tool schemas, arguments, results, `<state>` blocks); XML tags for boundaries and provenance (`<resource path="…">`, `<panel>`), so injected content is cheaply separated from user text.
- **Only prompt-relevant bytes reach the provider.** `gotato.ForModel` strips message IDs, usage, stop reasons, and runtime metadata; tools are sorted; `CacheBreakpoints` are placed after system, after tools, and before the tail. `context_built` reports `prefix_hash`; two consecutive Turns with the same hash present an identical cacheable prefix.
- **Forking is a state operation.** `session.Fork` copies state and records the parent Session ID. Lineage belongs to data; agents stay peers.
- **Tools are visible per Turn.** The agent asks every `ToolSource` for its tools at each Turn boundary and keeps that set for the whole Turn.

Every agent in this design is a peer.

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

The CLI composes `session.FileStore`, `modelctx`, `toolregistry`, `testkit` models, and the `gateway` provider exactly as an application would; agent semantics live in the packages. Stdout carries data, stderr carries diagnostics, exit codes are documented, and every command has a machine-readable form. The contract is [cmd/gotato/README.md](cmd/gotato/README.md).

`gotato serve` runs the same `service.Runner` behind the HTTP adapter; `gotato-grpc` (in the `adapter/grpc` module) behind gRPC. There is one code path from library to CLI to service.

## 5a. The Service

Hosted, Gotato is a store of Sessions and a supply of disposable Agents:

```text
POST /v1/sessions/{id}/runs {"prompt": "…"}
        │
        v
 service.Runner
   ① store.Get(id)                      the Session is the unit of identity
   ② lock(id)                           one Run per Session at a time (reject or wait)
   ③ agent := NewAgent(spec, WithTranscript(s), WithContextBuilder(…), session.Record(s), AutoCompact(s, …))
   ④ result := agent.Prompt(ctx, msg)   or StreamRun → SSE / gRPC stream
   ⑤ agent.Close(); store.Save(s)       the Agent is discarded, the Session persists
   ⑥ unlock(id)
```

An `AgentSpec` is reusable configuration (model, instruction, tools, context builder, extensions, limits, compaction budget); a Session chooses its Spec by name and may override instruction, panel, compaction ceiling, and tool activation through metadata. Because continuity lives in the Store, any process holding the Store can serve any Session; a live Agent is an optimization; identity is the Session. A derived line of work is `session.Fork` plus another Run, with lineage in metadata.

## 6. Dependency Direction

```text
cmd/gotato, adapter/grpc (module), service/httpapi
        |
        v
service
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

The direction is enforced by `layering_test.go`: the root package imports only the standard library, and standard runtime packages import only inward. Optional integrations stay optional for core packages.

## 7. Principles for Evolving the Runtime

1. **Additive first.** New capability arrives as an option, an interface, or a package. Existing constructors and the two-method `Agent` interface keep working.
2. **Deprecate before removing.** A symbol that conflicts with the constitution is marked `// Deprecated:` with a replacement named, and removed only with a documented break.
3. **Document every break** in [MIGRATION.md](MIGRATION.md) with a before/after example.
4. **Clarity wins.** A well-documented break beats preserving a confused abstraction (DESIGN G-D30).
5. **Admission questions before new concepts.** Every proposed concept answers the eight questions in DESIGN.md §Governance before it lands.
6. **Deterministic tests define done.** A feature is complete when it has a fake-model test and, when user-visible, a CLI scenario.

## 8. Direction

The runtime foundation described above is in place. FEATURES.md is the authoritative inventory; the open items there define the direction:

- **Context**: provider adapters that map `CacheBreakpoints` to explicit cache controls; token estimation from provider usage instead of the bytes/4 heuristic.
- **Events**: typed payload structs per kind; `reasoning_update` for streaming reasoning deltas.
- **Tools**: MCP client as a `ToolSet`/`ToolSource`.
- **Providers**: a second, non-OpenAI adapter to keep the model contract provider-neutral.
- **Testkit**: failure injection and context fixtures; a fixture-driven scenario runner.
- **Service**: a Store-level Session lease for multi-replica deployments; request IDs and idempotency keys on the HTTP/gRPC adapters.
- **Repository**: examples for one-shot, persistent session, compaction, fork, dynamic tools, concurrent agents, and CLI automation; CI with `gofmt`, `vet`, and `-race` for both modules.

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
| Session store + Agent per Run, admission, cancellation, remote exposure | `service`, `service/httpapi`, `adapter/grpc` |
| roles, task graphs, scheduling, orchestration, project state, UI, memory systems | the application (GOALS §2) |
| database-backed stores, concrete tool packages, skill adapters, other context strategies | the application, through `session.Store`, `Tool`/`ToolSet`, and `ContextBuilder` |
