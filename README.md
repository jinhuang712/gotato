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

## How Simple is Gotato?

```go
import (
    gotato "github.com/jinhuang712/gotato/gotato-core"
    "github.com/jinhuang712/gotato/testkit"
)

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

`Prompt` returns when the Run settles, and `ctx` carries its cancellation and deadline. This code is run as a test: [example_test.go](gotato-core/example_test.go).

## What you import

The core, `gotato-core`, is the package every program uses. It depends on the standard library alone, and it is complete on its own: a Session with an in-memory conversation, tools, and Events. The other packages add what a service needs.

| You want to | Import |
|---|---|
| Run an agent in your program | `gotato-core`, and a model: `gateway` for OpenAI or Anthropic, or your own `gotato.Model` |
| Keep conversations in a store and continue them on any pod | add `service` (the Runner) and `session` (the Store) |
| Keep long conversations small | `modelctx`; the Runner brings it |
| Call MCP servers, or read large results with a `bash` tool | `adapter/mcp`, `adapter/bash` (modules of their own) |
| Serve it over HTTP or gRPC | `service/httpapi`, `adapter/grpc` |
| Write tests with a fake model | `testkit` |

Without `session` a conversation lives in the memory of one process and is gone when it exits; that is the one thing to add before you run on many pods.

Guides: [Install](guides/install.md), [Getting started](guides/getting-started.md) and [Embed Gotato in a service](guides/embed-in-a-service.md). Each feature, with its status, is in [FEATURES.md](FEATURES.md).

## Install

Gotato has no release yet, so a version is a commit:

```sh
go get github.com/jinhuang712/gotato@<commit>
```

[Install](guides/install.md) has the details, including the adapters.

## Documents

| Document | Role |
|---|---|
| [PROPOSAL.md](PROPOSAL.md) | why Gotato exists, and what it refuses to become |
| [PHILOSOPHY.md](PHILOSOPHY.md) | the beliefs behind it |
| [DESIGNS.md](DESIGNS.md) | design principles, and the decisions that follow from them |
| [GLOSSARY.md](GLOSSARY.md) | one name per thing, and its name in today's code |
| [FEATURES.md](FEATURES.md) | what is built, with status markers, and what is not planned |
| [RUNBOOK.md](RUNBOOK.md) | what to do when a service built on Gotato misbehaves |
| [plan/GOALS.md](plan/GOALS.md) | goals that pass or fail, and benchmarks tracked against a reference |
| [plan/ROADMAP.md](plan/ROADMAP.md) | what gets built next, and how each step is known to be done |
| [plan/TODO.md](plan/TODO.md) | open defects and decisions |
| [AGENTS.md](AGENTS.md) | how to work in this repository, and the Git policy |
| [CHANGELOG.md](CHANGELOG.md) | what changed, newest first |

## License

Gotato is licensed under the [Apache License 2.0](LICENSE).
