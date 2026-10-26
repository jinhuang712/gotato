# Gotato

[![CI](https://github.com/jinhuang712/gotato/actions/workflows/ci.yml/badge.svg)](https://github.com/jinhuang712/gotato/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/jinhuang712/gotato.svg)](https://pkg.go.dev/github.com/jinhuang712/gotato)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

> **A minimal Go library that turns cloud services into cloud agents: distributed, cheap and stable from day 1.**

Gotato runs purpose-built agents inside the Go services you already operate. A Session is a small agent loop plus a runtime that saves it to your store and lets it go when it is idle, so any pod can run the next message.

## Why

- **Go all the way.** One language, one build and one deployment: the agent is a package in your service, not a second system beside it.
- **The conversation survives your pods.** State lives in your store. A rolling deploy, a scale-down or a crash does not lose it, and one writer per Session keeps two pods from running it at once.
- **No machine per conversation.** An idle Session costs a few kilobytes, so many fit in one process.

[PROPOSAL.md](PROPOSAL.md) tells the longer story.

## Try it

```go
s, err := gotato.NewSession(
    gotato.WithModel(testkit.EchoModel{}), // any gotato.Model; providers live in gateway/
    gotato.WithInstruction("You are a helpful assistant."),
)
if err != nil {
    return err
}
defer s.Close(ctx)

result, err := s.Prompt(ctx, gotato.UserMessage("hello"))
fmt.Println(gotato.TextOf(*result.FinalMessage)) // echo: hello
```

`Prompt` returns when the Run settles, and `ctx` carries its cancellation and deadline. This code is run as a test: [example_test.go](example_test.go).

## What you get

| You want to | Start here |
|---|---|
| Run an agent in your program | [Getting started](docs/getting-started.md) |
| Run it in a service on many pods | [Embed Gotato in a service](docs/embed-in-a-service.md) |
| Call tools, MCP servers, read large results | [FEATURES.md](FEATURES.md): Tool search, The MCP adapter, Reading large results |
| Keep long conversations small | [FEATURES.md](FEATURES.md): Compaction |
| Serve it over HTTP or gRPC, or from a CLI | `service/httpapi`, `adapter/grpc`, [cmd/gotato](cmd/gotato/README.md) |

## Install

Gotato has no release yet, so a version is a commit. Pin one you have tested:

```sh
go get github.com/jinhuang712/gotato@<commit>
```

Until the first release a name may change, with the before and after in [CHANGELOG.md](CHANGELOG.md). The root module needs Go 1.22; the gRPC, MCP and `bash` adapters are modules of their own.

## Documents

| Document | Role |
|---|---|
| [PROPOSAL.md](PROPOSAL.md) | why Gotato exists, and what it refuses to become |
| [PHILOSOPHY.md](PHILOSOPHY.md) | the beliefs behind it |
| [DESIGNS.md](DESIGNS.md) | design principles, and the decisions that follow from them |
| [GLOSSARY.md](GLOSSARY.md) | one name per thing, and its name in today's code |
| [GOALS.md](GOALS.md) | goals that pass or fail, and benchmarks tracked against a reference |
| [FEATURES.md](FEATURES.md) | what is built, with status markers, and what is not planned |
| [ROADMAP.md](ROADMAP.md) | what gets built next, and how each step is known to be done |
| [TODO.md](TODO.md) | open defects and decisions |
| [AGENTS.md](AGENTS.md), [GITFLOW.md](GITFLOW.md) | how to work in this repository |
| [CHANGELOG.md](CHANGELOG.md) | what changed, newest first |

## Origin

Inspired by [Pi](https://pi.dev)'s agent kernel (`@earendil-works/pi-agent-core`, created by Mario Zechner and contributors, MIT-licensed), redesigned as a Go-native runtime. Gotato is an independent design: it expresses Pi's loop semantics (Prompt/Continue, streaming, tool batches, steering and follow-up, abort, interception) through goroutines, channels, `context.Context` cancellation, and explicit extensions. Attribution is retained wherever derived material requires it.

- [Pi repository](https://github.com/earendil-works/pi)
- [`pi-agent-core` on npm](https://www.npmjs.com/package/@earendil-works/pi-agent-core)

## License

Gotato is licensed under the [Apache License 2.0](LICENSE).
