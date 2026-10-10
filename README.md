# Gotato

> **Gotato is a minimal, synchronous, Go-native agent runtime.**

## What Gotato is

1. **A minimal, synchronous, Go-native agent runtime.** One loop per agent; `Prompt` returns when the Run settles; `context.Context` carries cancellation and deadlines.
2. **A valid project on its own.** A Go program gets an agent with sessions, context construction, tools, events, a CLI, and deterministic tests from Gotato alone.
3. **Embeddable in a running Go service, across instances.** Runs are cancellable, state lives behind `session.Store`, and multi-instance safety comes from a Session lease (planned).

Every feature passes one test: *would a Go service embedding an agent want this on its own?* Roles, orchestration, memory systems, databases, and tool catalogs belong to the applications built on Gotato ([GOALS.md §2](GOALS.md#2-boundaries)).

## Cost

An idle agent costs a few kilobytes. A working agent's memory follows its transcript, with little on top. Many agents fit in one process.

| Measurement: 100 concurrent agents, 25 tool rounds each, 4 KB of unique tool output per round | Value |
|---|---|
| Heap per idle agent | 4–6 KB |
| Heap per agent after the loop (100 KB of tool output each) | 216–237 KB |
| Process footprint with all 100 agents active (macOS `vmmap`) | 41 MB |
| Gotato time per model turn, including tool dispatch | under 50 µs |

For scale: one Claude Code session held 320–390 MB of private memory on the same machine. These numbers exclude model latency, provider clients, and tool subprocesses. Measured on 2026-10-09 with Go 1.26 on Apple silicon. Reproduce them with:

```sh
go test -run '^$' -bench AgentFootprint -benchtime 1x .
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
    gotato.WithExtension(modelctx.AutoCompact(s, modelctx.CompactPolicy{Ceiling: 60000})),
    gotato.WithToolSource(toolregistry.MustNew(tools...)),
    gotato.WithExtension(session.Record(s)),      // runs, events, usage into the Session
)
if err != nil {
    return err
}
defer agent.Close(context.Background())

result, err := agent.Prompt(ctx, gotato.UserMessage("inspect this repository"))
```

`gotato.NewAgent(gotato.WithModel(model))` alone runs against a private in-memory transcript with full-history context.

## Embedding in a service

```go
store, err := session.NewFileStore("/var/lib/myservice/sessions") // or your own session.Store
if err != nil {
    return err
}
runner, err := service.New(service.Config{
    Store: store,
    Specs: []service.AgentSpec{{Name: "default", Model: model, Tools: tools}},
})
if err != nil {
    return err
}
mux.Handle("/agent/", http.StripPrefix("/agent", httpapi.New(runner))) // optional HTTP surface

out, err := runner.Run(ctx, service.RunRequest{Prompt: "summarize the open tickets"})
```

Each Run loads the Session, builds an Agent, runs it, closes it, and saves the Session. Any instance holding the Store can serve any Session.

## Quick start (CLI)

```bash
go build -o bin/gotato ./cmd/gotato
bin/gotato doctor --json
id=$(bin/gotato session create --json | jq -r .id)
bin/gotato run --session "$id" --model demo --json "use-tool"
bin/gotato context inspect "$id" --json
bin/gotato serve --addr 127.0.0.1:8787        # the same runner over HTTP
```

Stdout is data, stderr is diagnostics, exit codes are documented: [cmd/gotato/README.md](cmd/gotato/README.md).

## Feature status

| Area | Status |
|---|---|
| Agent loop, control (Steer, FollowUp, Abort), limits, extensions | done |
| Session, `session.Store` (memory, file), fork | done |
| Context: full history, static blocks, panel, compaction, inspection | done |
| Tool contract, Tool Registry, staged `ToolSet` | done |
| Events and streaming | done; typed payloads and reasoning deltas planned |
| Providers: OpenAI Chat Completions and Responses, Anthropic Messages | done |
| Service: Runner, HTTP, gRPC, admission, drain | done |
| CLI: `run`, `session`, `context`, `tools`, `events`, `doctor`, `serve` | done |
| Session lease for multi-instance safety | planned |
| MCP through `ToolSet`/`ToolSource`, tool effect classification | planned |
| Provider rate-limit information | planned |

[FEATURES.md](FEATURES.md) is the authoritative inventory.

## Documents

| Document | Role |
|---|---|
| [PHILOSOPHY.md](PHILOSOPHY.md) | the worldview |
| [DESIGN.md](DESIGN.md) | durable engineering rules |
| [GOALS.md](GOALS.md) | goals, boundaries, tradeoffs, compatibility |
| [FEATURES.md](FEATURES.md) | implementation inventory with status markers |
| [PROPOSAL.md](PROPOSAL.md) | architecture, layers, and packages |
| [AGENTS.md](AGENTS.md) | instructions for coding agents working here |
| [GITFLOW.md](GITFLOW.md) | Git policy |
| [MIGRATION.md](MIGRATION.md) | breaking changes and how to move |

## Development

```bash
gofmt -l . && go vet ./... && go test -race ./...
(cd adapter/grpc && go test ./...)
```

## Origin

Inspired by [Pi](https://pi.dev)'s agent kernel (`@earendil-works/pi-agent-core`, created by Mario Zechner and contributors, MIT-licensed), redesigned as a Go-native runtime. Gotato is an independent design: it expresses Pi's loop semantics (Prompt/Continue, streaming, tool batches, steering and follow-up, abort, interception) through goroutines, channels, `context.Context` cancellation, and explicit extensions. Attribution is retained wherever derived material requires it.

- [Pi repository](https://github.com/earendil-works/pi)
- [`pi-agent-core` on npm](https://www.npmjs.com/package/@earendil-works/pi-agent-core)

## License

Gotato is licensed under the [Apache License 2.0](LICENSE).
