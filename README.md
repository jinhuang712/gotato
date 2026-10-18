# Gotato

> **Gotato is a minimal, synchronous, Go-native agent runtime.**

## What Gotato is

1. **A minimal, synchronous, Go-native agent runtime.** One Loop per Session; `Prompt` returns when the Run settles; `context.Context` carries cancellation and deadlines.
2. **A valid project on its own.** A Go program gets an agent with sessions, context construction, tools, events, a CLI, and deterministic tests from Gotato alone.
3. **Embeddable in a running Go service, across instances.** Runs are cancellable, state lives behind `session.Store`, and multi-instance safety comes from one writer per Session across instances.

Every feature passes one test: *would a Go service embedding an agent want this on its own?* Roles, orchestration, memory systems, databases, and tool catalogs belong to the applications built on Gotato ([FEATURES.md → Not planned](FEATURES.md#not-planned)).

## Cost

An idle Session costs a few kilobytes. A working Session's memory follows its transcript, with little on top. Many Sessions fit in one process.

| Measurement: 100 concurrent Sessions, 25 tool rounds each, 4 KB of unique tool output per round | Value |
|---|---|
| Heap per idle Session | 4–6 KB |
| Heap per Session after the loop (100 KB of tool output each) | 216–237 KB |
| Process footprint with all 100 Sessions active (macOS `vmmap`) | 41 MB |
| Gotato time per model turn, including tool dispatch | under 50 µs |

For scale: one Claude Code session held 320–390 MB of private memory on the same machine. These numbers exclude model latency, provider clients, and tool subprocesses. Measured on 2026-10-09 with Go 1.26 on Apple silicon. Reproduce them with:

```sh
go test -run '^$' -bench SessionFootprint -benchtime 1x .
```

## Quick start (library)

```go
model := testkit.EchoModel{}                      // any gotato.Model; providers live in gateway/
record := session.New()                           // what the store keeps: what happened

s, err := gotato.NewSession(
    gotato.WithModel(model),
    gotato.WithInstruction("You are a helpful assistant."),
    gotato.WithTranscript(record),                // the Session commits to its Record and takes its ID
    gotato.WithContextBuilder(modelctx.WithStatic(modelctx.FullHistory(), modelctx.Resource("AGENTS.md", rules))),
    gotato.WithExtension(modelctx.AutoCompact(record, modelctx.CompactPolicy{Ceiling: 60000})),
    gotato.WithToolSource(toolregistry.MustNew(tools...)),
    gotato.WithExtension(session.NewRecorder(record)), // runs, events, usage into the Record
)
if err != nil {
    return err
}
defer s.Close(context.Background())

result, err := s.Prompt(ctx, gotato.UserMessage("inspect this repository"))
```

`gotato.NewSession(gotato.WithModel(model))` alone runs against a private in-memory transcript with full-history context.

To keep the Session in a store while it runs, store its Record first and give the Session the Writer instead: every step is written as it happens, and `Release` writes the end of the Run.

```go
writer, err := session.Create(ctx, store, record, "instance-1") // or session.OpenWriter for a stored Session
// … gotato.WithTranscript(writer) instead of gotato.WithTranscript(record) …
result, err := s.Prompt(ctx, gotato.UserMessage("inspect this repository"))
err = writer.Release(ctx)
```

## Embedding in a service

```go
store, err := session.NewFileStore("/var/lib/myservice/sessions") // or your own session.Store
if err != nil {
    return err
}
runner, err := service.New(service.Config{
    Store:  store,
    Agents: []service.Agent{{Name: "default", Model: model, Tools: tools}},
})
if err != nil {
    return err
}
mux.Handle("/agent/", http.StripPrefix("/agent", httpapi.New(runner))) // optional HTTP surface

out, err := runner.Run(ctx, service.RunRequest{Prompt: "summarize the open tickets"})
```

Each Run opens the Session's Record from its last Checkpoint, builds a live Session from the Agent, and writes every step of the Run as it happens, so a crash loses at most the step in flight. Instances that share a Store run a Session one at a time: another instance takes it over when it is idle or its instance is gone, and waits, then answers 409, while it runs elsewhere. When the service stops, `runner.Drain` lets every Run finish, save and release its Session, and `runner.Close` then removes the instance's Liveness row, so other instances take its Sessions over at once.

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
| Live Session and its Loop, Abort, limits, extensions | done |
| Steer and FollowUp | in the Go API on a Session you build yourself; through the Runner and the endpoints planned |
| Session Record as an append-only log, every step written as it happens, `session.Store` (memory, file) and its conformance suite (`testkit/storetest`), fork | done |
| Context: full history, static blocks, compaction, inspection | done |
| Tool contract, Tool Registry, staged `ToolSet` | done |
| Events and streaming | done, with gaps: messages on their way into a Session have no Events; stream resume planned |
| Providers: OpenAI Chat Completions and Responses, Anthropic Messages | done |
| Service: Runner, HTTP, gRPC, admission | done |
| Drain | done: marks the Liveness row draining, answers 503 on `/readyz`, lets every Run finish and save, and tells a stopped stream to reconnect |
| CLI: `run`, `session`, `context`, `tools`, `events`, `doctor`, `serve` | done |
| One writer per Session across instances | done; stream resume and the activation limit planned |
| Tool effect classification (`ToolSpec.Effect`), and an Effect cap per Agent (`WithMaxEffect`) | done |
| Measurement: the reference workload, the live run (`gotato-workload`), fault injection and a multi-instance harness | done |
| MCP, provider rate-limit information, typed Event payloads, streamed reasoning | later |

[FEATURES.md](FEATURES.md) is the authoritative inventory.

## Documents

| Document | Role |
|---|---|
| [PROPOSAL.md](PROPOSAL.md) | why Gotato exists, and what it refuses to become |
| [PHILOSOPHY.md](PHILOSOPHY.md) | the beliefs behind it |
| [DESIGNS.md](DESIGNS.md) | design principles, and the decisions that follow from them |
| [GLOSSARY.md](GLOSSARY.md) | one name per thing, and its name in today's code |
| [GOALS.md](GOALS.md) | goals that pass or fail, and benchmarks tracked against a reference |
| [FEATURES.md](FEATURES.md) | implementation inventory with status markers, and what is not planned |
| [ROADMAP.md](ROADMAP.md) | what gets built next, in what order, and how each step is known to be done |
| [TODO.md](TODO.md) | open defects and design decisions, verified against the code |
| [AGENTS.md](AGENTS.md) | instructions for coding agents working here |
| [GITFLOW.md](GITFLOW.md) | Git policy |
| [CHANGELOG.md](CHANGELOG.md) | changes per release, with before/after notes for breaking changes |

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
