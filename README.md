<div align="center">

<img src="assets/gotato-logo.webp" alt="Gotato" width="560">

<img src="assets/tagline.svg" alt="A minimal Go library that turns cloud services into cloud agents: distributed, efficient and stable from day 1." width="860">

🎯 **Our mission: Agent-as-a-Service.** Ultimately, any service can easily become an agent. The agent becomes the cloud service itself.

[![CI](https://github.com/jinhuang712/gotato/actions/workflows/ci.yml/badge.svg)](https://github.com/jinhuang712/gotato/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/jinhuang712/gotato.svg)](https://pkg.go.dev/github.com/jinhuang712/gotato)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
![Go](https://img.shields.io/badge/Go-1.22%2B-00ADD8?logo=go&logoColor=white)
![Status](https://img.shields.io/badge/status-pre--release-orange)
![Core](https://img.shields.io/badge/core-standard%20library%20only-brightgreen)

</div>

## 🤔 Why Gotato?

- **☁️ Why agents as a cloud service?** A machine per conversation does not scale: a million is about a petabyte, and at a few kilobytes idle it is a few gigabytes.
- **🐹 Why Go?** Your services already are. No second language, no second deployment: the agent is a package, not a system beside yours.
- **🕸️ Why not a graph?** Models plan on their own now. One loop does what the nodes did, with less to run and debug.
- **📦 Why not an existing SDK?** Each brings a subprocess per session, a conversation held elsewhere, or a whole toolkit. None is the [smallest thing that runs on many pods](PROPOSAL.md#where-gotato-fits).
- **🪶 Why minimalistic?** The model keeps taking over the harness, so the harness stays thin: a standard-library core, with your store, tools and telemetry plugged in.

## 💡 The idea

An **Agent** is a definition: model, instruction, tools, limits. A **Session** is one conversation running it: a small loop, plus a runtime that saves every step to your store.

Between messages a Session is only a record in your store. Any pod can load it and carry on, and one writer per Session keeps two pods apart. No process, no shell, no sandbox, and a few kilobytes while idle.

A request can reach any pod. Only one of them runs a given Session at a time, and the Session itself lives in your store:

```text
                request
                   │
      ┌────────────▼────────────┐
      │      load balancer      │   the one you already run
      └───┬───────────┬───────┬─┘
          │           │       │
      ┌───▼───┐   ┌───▼───┐ ┌─▼─────┐
      │ pod A │   │ pod B │ │ pod C │   each pod: a Runner hosting Sessions
      └───┬───┘   └───┬───┘ └─┬─────┘
          └───────────┼───────┘
      ┌───────────────▼─────────────┐
      │         your store          │   Session S = its record, a log that only grows
      └─────────────────────────────┘

  pod A runs S ──► pod A goes away ──► the next message reaches pod B, which loads S and goes on
```

[PROPOSAL.md](PROPOSAL.md) tells the longer story.

## ✨ What you get

- 🔌 **Fits your service.** A library, not a stack, with a standard-library core.
- 🌐 **Runs on many pods.** Every step is saved, and one pod runs a Session at a time.
- 🧩 **Stays simple.** One loop, no workflow language, and you see what the model sees.
- 🔒 **Stays stable.** Breaks are versioned and come with before-and-after notes, from the first release on.

📏 A program with the agent, runner, store and HTTP server pulls 3 modules and builds a 3.8 MB binary on Go 1.22 ([measured](PROPOSAL.md#where-gotato-fits)).

## 🚫 What it is not

Not a coding agent, a workflow language, a platform or infrastructure. Gotato runs purpose-built agents inside your service, and your service owns the rest ([the full list](PROPOSAL.md#what-we-dont-want)).

## 🧰 Capabilities

| Capability | What you get | |
|---|---|---|
| **Agent loop** | `Prompt`, `Continue` and `Abort`; steer, follow up and interrupt a Run in flight | ✅ |
| **Context management** | built every Turn, your own builder, inspection; automatic compaction into Segments the model can read back | ✅ |
| **Tools** | Go functions with an Effect; tool search; large results spilled and read back in pages | ✅ |
| **MCP** | MCP servers as tool sets, over streamable HTTP or stdio | ✅ |
| **Extensions** | six stages around the loop, from `TurnPreparer` to `TurnStopper` | ✅ |
| **Models** | OpenAI Chat Completions, OpenAI Responses, Anthropic Messages, or your own | ✅ |
| **Storage** | memory, file, or your own database behind eight calls, with a conformance suite | ✅ |
| **Many instances** | one writer per Session, takeover, drain, `not_writer` forwarding | ✅ |
| **Events** | every step as a Go type, streamed, resumable on any instance | ✅ |
| **Endpoints** | Go API and HTTP | ✅ |
| **Testing** | fake models, a fake gateway for each wire API, a reference workload | ✅ |
| **Telemetry** | OpenTelemetry mapping of Events | later |

✅ built, 🚧 built with a gap named, and later. [FEATURES.md](FEATURES.md) lists every feature and its status.

## ⚡ How Simple is Gotato?

A real model, one tool, one question:

```go
import (
    "github.com/jinhuang712/gotato/gateway"
    "github.com/jinhuang712/gotato/gotato"
)

// 1. A model. A provider is configuration: a wire API, a model and a key.
model, _ := gateway.New(gateway.Config{
    API:    gateway.APIAnthropicMessages, // or OpenAI Chat Completions, or Responses
    Model:  "claude-sonnet-5-5",
    APIKey: os.Getenv("ANTHROPIC_API_KEY"),
})

// 2. A tool is a Go function. Its input type becomes the schema the model sees.
weather, _ := gotato.NewFuncTool("weather", "Returns the weather of a city.", func(_ context.Context, in struct {
    City string `json:"city" description:"the city to look up"`
}) (string, error) {
    return "sunny in " + in.City, nil
})

// 3. A Session is one conversation: the model, an instruction and the tools.
s, _ := gotato.NewSession(
    gotato.WithModel(model),
    gotato.WithInstruction("You are a helpful assistant."),
    gotato.WithTools(weather),
)
defer s.Close(ctx)

// 4. Ask. Prompt runs the loop (model, tools, model, ...) and returns the final reply.
result, _ := s.Prompt(ctx, gotato.UserMessage("Do I need an umbrella in Paris?"))
fmt.Println(gotato.TextOf(*result.FinalMessage))
```

`Prompt` returns when the Run settles, and `ctx` carries its cancellation and deadline. The model asks for the tool, Gotato runs it and goes on until the model answers. Errors are left out here. This code is run as a test, against a fake gateway that speaks the same wire API: [Example_gatewayAndTool](gotato/example_test.go).

## 🧪 Example

**A chat bot on several pods.** A Lark bot built on Gotato: one Session per thread, any pod can answer, and a killed or replaced pod does not end a conversation. It runs on your machine with a fake chat platform and no secrets, and on a local Kubernetes cluster through a rolling deploy, a killed writer and a scale-down. See [`examples/larkbot`](examples/larkbot) and its [guide](guides/example-chat-bot.md).

## 📖 Guides

| Guide | You learn |
|---|---|
| [Install](guides/install.md) | how to add Gotato and the MCP adapter to your `go.mod` |
| [Which packages do I import?](guides/packages.md) | what each package is for, and what leaving one out costs |
| [Getting started](guides/getting-started.md) | a Session, a tool and the Events of a Run |
| [Embed Gotato in a service](guides/embed-in-a-service.md) | the Runner, many pods and shutting down |
| [Example: a chat bot](guides/example-chat-bot.md) | several instances behind a chat platform |

Each feature, with its status, is in [FEATURES.md](FEATURES.md).

## 🚀 Install

> [!WARNING]
> Gotato has no release yet. A version is a commit, and until the first release a name may change.

Pin a commit you have tested:

```sh
go get github.com/jinhuang712/gotato@<commit>
```

[Install](guides/install.md) has the details, including the MCP adapter.

## 📚 Documents

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

## 📄 License

Gotato is licensed under the [Apache License 2.0](LICENSE).
