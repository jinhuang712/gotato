# Gotato

> **Gotato is a minimalistic, composable Go agent runtime.**

Gotato provides the standard runtime primitives needed to build agentic applications in Go without prescribing what those applications must become: **Agent, Session, Context, Model, Tool, Tool Registry, Event, Extension, Provider, Persistence, CLI, and Testing.** It is broader than a single agent loop and smaller than an application framework. It has no built-in UI and no built-in agent organization.

```text
Less is More.
Agents should be highly cheap and disposable.
No agent is a sub-agent. There are only agents.
Agent as a Goroutine.
Session is what happened. Context is what the model sees now.
The CLI is a first-class interface for humans, scripts, and coding agents.
```

## Quick start (library)

```go
model := testkit.EchoModel{}                      // any gotato.Model; providers live in gateway/
s := session.New()                                // what happened

agent, err := gotato.NewAgent(
    gotato.WithModel(model),
    gotato.WithInstruction("You are a helpful assistant."),
    gotato.WithTranscript(s),                     // the agent commits to the Session
    gotato.WithContextBuilder(modelctx.WithStatic(modelctx.FullHistory(), modelctx.Resource("AGENTS.md", rules))),
    gotato.WithExtension(modelctx.AutoCompact(s, modelctx.CompactPolicy{Ceiling: 60000})), // shrink only by compaction
    gotato.WithToolSource(toolregistry.New(tools...)),
    gotato.WithExtension(session.Record(s)),      // runs, events, usage into the Session
)
if err != nil {
    return err
}
defer agent.Close(context.Background())

result, err := agent.Prompt(ctx, gotato.UserMessage("inspect this repository"))
```

The two-line form still works: `gotato.NewAgent(gotato.WithModel(model))` runs against a private in-memory transcript with full-history context. Nothing requires a server, a daemon, or a database.

## Quick start (CLI)

```bash
go build -o bin/gotato ./cmd/gotato
bin/gotato doctor --json
id=$(bin/gotato session create --json | jq -r .id)
bin/gotato run --session "$id" --model demo --json "use-tool"
bin/gotato context inspect "$id" --json
bin/gotato context compact "$id" --keep 2 --json
bin/gotato events --session "$id" | jq -r .kind
bin/gotato tools list --json
bin/gotato serve --addr 127.0.0.1:8787        # the same runner as an HTTP service
```

Stdout is data, stderr is diagnostics, exit codes are documented. The full contract is in [cmd/gotato/README.md](cmd/gotato/README.md).

## The runtime

```text
Session (session.Session)              "what happened"
   |   implements gotato.Transcript
   v
ContextBuilder (modelctx.*)            "what should the model see now"
   v
ModelContext → AssembleRequest         system | tools | append-only history | tail + <panel>
   v
Agent --- Tool Registry (toolregistry.Registry, a gotato.ToolSource)
   v
Agentic Loop:  build context -> model -> [tool request -> execute -> observation]* -> final
   v
Transcript appends + structured Events (agent_start, context_built, turn_end, tool_*, agent_end)
```

- An **Agent** is one goroutine with one canonical loop. It owns reusable configuration (model, instruction, tools, context strategy, extensions, limits) and nothing else; mutable state belongs to the Run and the Session.
- A **Session** records messages, runs, usage, events, compactions, and application metadata. It is persisted through `session.Store` (`MemoryStore`, `FileStore`) and can be forked as a state operation.
- A **Context** is built per Turn and laid out for prompt caching: static system content first, tools next, the append-only history, and a dynamic `<panel>` on the tail. Within a Session the model always sees the whole history; it shrinks only through compaction, which rewrites a prefix into a summary and records exactly what was replaced. `context_built` reports a `prefix_hash` so cache-friendliness is observable.
- **Tools** are capabilities with identity, schema, execution, and structured results. The **Tool Registry** registers, lists, describes, activates, and deactivates them; the agent picks up changes at each Turn boundary. `ToolSet`s add model-driven staged activation.
- **Events** are structured facts on a Go-native stream; **Extensions** wrap the loop at bounded stages (context transform, pre/post tool, observer, turn stopper).
- **Testing** is deterministic: `testkit` provides fake and replay models, a fake tool, an event recorder, and session fixtures. CI never calls a paid model.
- **Service** is the runtime turned outward: a `service.Runner` owns a Session store and a set of `AgentSpec`s; every request loads a Session, builds an Agent, runs it, closes it, saves the Session. Agents are created and discarded per Run; continuity lives in the store, so any process holding the store can serve any Session. HTTP (`service/httpapi`) and gRPC (`adapter/grpc`) are thin adapters over it.

## Packages

| Layer | Package | Contents |
|---|---|---|
| core | `gotato` | Agent, loop, Message, Model, Tool, ToolSet, Transcript, ContextBuilder, ToolSource, Events, Extensions, Errors, Limits. Standard library only. |
| standard runtime | `session` | Session, Store, MemoryStore, FileStore, Fork, Recorder |
| | `modelctx` | FullHistory, WithStatic, WithPanel, blocks, Inspect, Compact, AutoCompact, summarizers |
| | `toolregistry` | Registry (register/unregister/lookup/list/describe/activate/deactivate, change hooks) |
| | `testkit` | FakeModel, ReplayModel, FakeTool, EventRecorder, session fixtures, EchoModel, DemoModel |
| providers | `gateway` | OpenAI-compatible Chat Completions and Responses adapters (API key), YAML config |
| service | `service` | `Runner`: a store of Sessions, an Agent created per Run and discarded; AgentSpecs, per-Session single flight, admission, cancellation, drain |
| | `service/httpapi` | HTTP adapter over the Runner (sessions, runs, SSE streaming, events, context, compaction) |
| | `adapter/grpc` (module) | gRPC adapter over the Runner (`SessionService` v2) and the `gotato-grpc` binary |
| CLI | `cmd/gotato` | `run`, `session`, `context`, `tools`, `events`, `doctor`, `serve` |

Dependency direction is enforced by a test: the core imports only the standard library; standard runtime packages never import the service or adapters. Library, CLI, HTTP, and gRPC all drive the same `service.Runner`.

## Governance

| Document | Role |
|---|---|
| [PHILOSOPHY.md](PHILOSOPHY.md) | the worldview |
| [DESIGN.md](DESIGN.md) | durable engineering rules |
| [GOALS.md](GOALS.md) | goals, non-goals, tradeoffs, compatibility |
| [FEATURES.md](FEATURES.md) | implementation inventory with status markers |
| [PROPOSAL.md](PROPOSAL.md) | target architecture and direction |
| [AGENTS.md](AGENTS.md) | instructions for coding agents working here |
| [GITFLOW.md](GITFLOW.md) | Git policy |
| [MIGRATION.md](MIGRATION.md) | breaking changes and how to move |

`docs/` and `specs/` hold the earlier design record; where they disagree with the documents above, the root documents win.

## Development

```bash
gofmt -l . && go vet ./... && go test -race ./...
(cd adapter/grpc && go test ./...)
go build -o bin/gotato ./cmd/gotato && bin/gotato doctor --json
```

## Origin

Inspired by [Pi's Agent Kernel](https://pi.dev), redesigned as a Go-native runtime. Details: [docs/shout-out.md](docs/shout-out.md).

## License

Not yet selected.
