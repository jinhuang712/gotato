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
    gotato.WithContextBuilder(modelctx.Window(20)), // what the model sees now
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
```

Stdout is data, stderr is diagnostics, exit codes are documented. The full contract is in [cmd/gotato/README.md](cmd/gotato/README.md).

## The runtime

```text
Session (session.Session)              "what happened"
   |   implements gotato.Transcript
   v
ContextBuilder (modelctx.*)            "what should the model see now"
   v
ModelContext                           this Turn's model view
   v
Agent --- Tool Registry (toolregistry.Registry, a gotato.ToolSource)
   v
Agentic Loop:  build context -> model -> [tool request -> execute -> observation]* -> final
   v
Transcript appends + structured Events (agent_start, context_built, turn_end, tool_*, agent_end)
```

- An **Agent** is one goroutine with one canonical loop. It owns reusable configuration (model, instruction, tools, context strategy, extensions, limits) and nothing else; mutable state belongs to the Run and the Session.
- A **Session** records messages, runs, usage, events, compactions, and application metadata. It is persisted through `session.Store` (`MemoryStore`, `FileStore`) and can be forked as a state operation.
- A **Context** is built per Turn by an explicit strategy (`FullHistory`, `Window`, `SummaryRecent`, `Chain`, or your own `gotato.ContextBuilder`) and is inspectable without running a model. Compaction rewrites a Session prefix into a summary and records exactly what was replaced.
- **Tools** are capabilities with identity, schema, execution, and structured results. The **Tool Registry** registers, lists, describes, activates, and deactivates them; the agent picks up changes at each Turn boundary. `ToolSet`s add model-driven staged activation.
- **Events** are structured facts on a Go-native stream; **Extensions** wrap the loop at bounded stages (context transform, pre/post tool, observer, turn stopper).
- **Testing** is deterministic: `testkit` provides fake and replay models, a fake tool, an event recorder, and session fixtures. CI never calls a paid model.

## Packages

| Layer | Package | Contents |
|---|---|---|
| core | `gotato` | Agent, loop, Message, Model, Tool, ToolSet, Transcript, ContextBuilder, ToolSource, Events, Extensions, Errors, Limits. Standard library only. |
| standard runtime | `session` | Session, Store, MemoryStore, FileStore, Fork, Recorder |
| | `modelctx` | FullHistory, Window, SummaryRecent, Chain, Inspect, Compact, summarizers |
| | `toolregistry` | Registry (register/unregister/lookup/list/describe/activate/deactivate, change hooks) |
| | `testkit` | FakeModel, ReplayModel, FakeTool, EventRecorder, session fixtures, EchoModel, DemoModel |
| providers | `gateway` | OpenAI-compatible chat completions and OpenAI Codex Responses adapters, YAML config |
| CLI | `cmd/gotato` | `run`, `session`, `context`, `tools`, `events`, `doctor` |
| optional service layer | `orchestration`, `host`, `adapter/grpc`, `cmd/gotato-agent` | multi-agent routing, admission and retirement; HTTP and gRPC exposure; reference daemon. Built on the runtime, never imported by it. |

Dependency direction is enforced by a test: the core imports only the standard library; standard runtime packages never import the service layer.

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
