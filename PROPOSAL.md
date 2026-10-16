<!-- what-we-build: drafted 2026-10-16 -->
# Gotato — Proposal

> **Agent as a service for Go.**
>
> A minimal Go library that turns cloud services into cloud agents: distributed, cheap and stable from day 1.

Proposed 2026-08-27, rewritten 2026-10-15. Why it is shaped this way: [PHILOSOPHY.md](PHILOSOPHY.md). How it is built: [DESIGNS.md](DESIGNS.md). Terms: [GLOSSARY.md](GLOSSARY.md).

## In short

Gotato runs purpose-built agents inside the Go services you already operate. It does one thing: it makes the agent session small, cheap and safe to run on many pods.

For most scenarios, a cloud-hosted full session, with a machine for each conversation, is a waste of resources. A purpose-built agent looks things up, decides and answers. For that, a session needs two parts:

- a **loop** that calls the model and the tools it asks for;
- a **runtime** around the loop that keeps the conversation's state, saves it to your store, and lets it go when it is idle.

> **A Gotato Session is a minimal agentic loop and a runtime.**

## Pain points

When a Go service needs an agent today, four things get in the way.

### 1. The agent stack is not Go

The established agent stacks are written in Python, TypeScript or Rust. A Go service that adopts one takes on:

- a second language and a second build;
- a second deployment;
- a second set of dependencies to keep up to date.

The agent ends up as a separate system beside the service it works for.

### 2. Graph frameworks are heavy for the work

Agents are often assembled on graph frameworks such as LangGraph, Deep Agents or Langflow, with a node or a branch for each case. These setups are expensive to run, slow, and hard to develop and debug. In our experience they do not do clearly better than one loop:

```text
call the model ──► run the tools it asked for ──► repeat until it answers
```

Time spent drawing and debugging the graph is time not spent on the parts only the team can build: auth, tool sets, MCP servers and monitoring.

### 3. The conversation lives in one process

A Go service on Kubernetes runs on many pods, which the autoscaler adds and removes. An agent stack that keeps the conversation in process memory loses it when its pod goes away:

```text
pod A: conversation in memory ──► rolling deploy ──► gone
next message ──► pod B ──► "never seen this conversation"
```

Pinning each conversation to one pod sends the next message to the right place, until that pod goes away. It also gives up much of what scaling is for.

### 4. A simple task carries a coding agent's weight

Coding-agent harnesses are built to write code, so each session brings a machine with it. Anthropic's hosting guide for its Agent SDK starts each session at:

```text
1 GiB RAM · 5 GiB disk · 1 CPU · one subprocess
```

The guide calls the 1 GiB "a floor, not the ceiling". A conversation is idle most of the time, so this cost runs while nothing happens.

## The shift

Each of these pains has started to ease, because six things changed, most of them recently.

**1. From one model call to an agent loop.** An AI feature used to be one prompt and one reply. Now an agent calls the model and its tools many times for one request, and keeps the conversation for the next one.

**2. From graph frameworks to a thin harness.** The labs settled on one shape, a loop around the model, its tools and its context, and ship it as a product. As models get better at planning and at recovering from errors, they take over the work that graph nodes used to do. [PHILOSOPHY.md](PHILOSOPHY.md) argues this.

> "The Agent SDK gives you the same tools, agent loop, and context management that power Claude Code." — Anthropic, *Agent SDK overview*

**3. From a machine to a loop and its state.** Early agent stacks gave each conversation a whole machine. The labs now keep the loop apart from the environment, and make the environment optional:

```text
before                               now
┌──────── one conversation ────┐     ┌──── one conversation ────┐
│ loop + state                 │     │ loop + state             │
│ filesystem · shell · sandbox │     └──────────────────────────┘
│ one process, 1 GiB or more   │       environment: optional
└──────────────────────────────┘
```

> "The harness can work without an environment, and your application can receive progress through streaming or webhooks." — OpenAI, *Agents API: architecture*

**4. From a laptop process to many pods.** Agents are moving from a developer's machine into services, and services already run on many pods.

**5. From Python, TypeScript and Rust to Go.** Agent tooling now arrives in the language that cloud services run on: Google ADK for Go, Eino, Genkit for Go. kagent measured the same minimal agent at 253 Mi idle on its Python runtime and 7 Mi on its Go runtime.

**6. From coding agents to purpose-built agents.** Coding agents are the best known. Most agent work inside a running system is narrower, and needs tools and data rather than a machine:

- the hard cases inside a pipeline, after rules and a single model call run out;
- investigations, started by an alert or a schedule, that end in a report;
- conversations with users that need the service's own tools and records.

[PHILOSOPHY.md](PHILOSOPHY.md) explains why this work needs no machine of its own.

## The idea

Gotato splits an agent in two. An **Agent** is a definition, written once. A **Session** is one conversation that runs it:

```text
Agent      a definition: model · instruction · tools · limits
  │ runs many
  ▼
Session    one conversation: a loop and a runtime
  ├─ loop        calls the model and the tools
  ├─ Transcript  everything that happened, saved to your store
  ├─ status      idle · running · failed
  └─ TTL         leaves memory after 60 idle minutes, by default
```

A few words in the diagram need a line each:

- a **Turn** is one model call and the tools it asked for;
- the **TTL** (time to live) is how long an idle Session stays in memory;
- a **Runner** is the part of your service that hosts Sessions.

Between messages, a Session is its record in your store: a log that only grows, one Turn at a time. A pod keeps it in memory while it is in use, and lets it go after its TTL. The conversation stays in your store, so any pod can load it again and continue.

A Session needs no process, shell or sandbox of its own. A Go service hosts Sessions the way it serves any other request.

## What we want

Gotato should behave like any other library in the service: easy to add, safe to scale, simple to follow. These are what Gotato is built toward. [DESIGNS.md](DESIGNS.md) marks the ones the code does not meet yet.

### It fits into your service

- **A library, not a new stack.** Gotato arrives with `go get`. It builds, tests and deploys with the service, on the CI/CD the team already runs.
- **A small footprint.** The core uses only the Go standard library. Gotato adds a few modules and a few megabytes to a service, and runs on Go 1.22.
- **Your storage and your telemetry.** Gotato defines stable shapes for a Session and for its Events, the facts it reports as it works. Your adapter maps them onto the database you run, such as MySQL, Postgres or Mongo, and onto the telemetry stack you run.
- **Any model provider.** A provider is configuration: an endpoint, a key, a wire API (the request format it speaks) and its models. Any vendor that speaks a supported wire API works without new code.
- **Common endpoints.** Gotato provides an HTTP handler, a gRPC service and a Go API. A bot, a job or another service talks to a Session like any other API.

### It runs on many pods

- **No lost conversations.** A Session saves every Turn it completes. When its pod goes away, another pod loads it and continues.
- **Any pod answers like the only one.** A request can reach any pod, and the caller cannot tell which one served it. No Session is bound to a pod, and the load balancer you already run decides where each request goes.
- **One pod at a time.** Two pods never run or write the same Session at once.
- **Idle costs almost nothing.** A waiting Session holds no process and no sandbox, and after its TTL it holds no memory either. Each request to the model is laid out so that the provider can reuse its cached prefix from one Turn to the next, which lowers the token bill.

### It stays simple

Gotato owns the loop and the Session. Everything that differs from team to team plugs in:

```text
Gotato:  loop ── Session ── Context
you:     store · model provider · tools · MCP servers · auth · telemetry
```

- **One loop, no workflow language.** The model plans the steps, and ordinary Go code decides when to call an agent.
- **You see what the model sees.** The Context is built from the Transcript on every Turn, and you can inspect it exactly as it is sent.
- **Safe tools without a sandbox.** Each tool declares its Effect: read, write local, write external or destructive. An Agent allows only the tools it names, up to the highest Effect it permits, and each tool checks its own arguments.

### It stays stable

- **Stable contracts.** The Go API, the HTTP and gRPC contracts and the shape of a saved Session change only through versioned breaks, with before-and-after notes in [CHANGELOG.md](CHANGELOG.md).

## Where Gotato fits

Several good options exist for building agents, and each makes a different choice. Gotato's choice is the smallest library that still runs on many pods.

- **Lab harness libraries**, such as the Claude Agent SDK, offer the full coding-agent harness in Python or TypeScript, with one subprocess per session. In Gotato, a Session is a goroutine while it works and a record in your store while it waits.
- **Lab-hosted agents**, such as Claude Managed Agents and the OpenAI Agents API, run the loop for you and keep the session on their side. Gotato runs the loop inside your service, with any provider.
- **Go composition frameworks**, such as Eino, offer components and graphs to assemble, and leave conversation storage to the application. Gotato offers one loop with Sessions built in.
- **Batteries-included Go toolkits**, such as Google ADK for Go, offer agents, workflows, sub-agents, A2A, database sessions and a REST server. Gotato offers one loop and one Session.

The footprint follows from that choice. A program that imports each library's agent, runner, session store and HTTP server, and does nothing else, measures:

```text
                                   Gotato    Google ADK for Go
modules in the build graph         3         128
non-standard-library packages      23        370
binary, stripped                   3.8 MB    14.8 MB
minimum Go version                 1.22      1.26.6
```

Measured 2026-10-15 on an Apple M3, with ADK for Go v2.6.0.

## What we don't want

- **A cloud coding agent.** Gotato leaves coding to developers' machines and to coding products. Sessions in the cluster investigate, decide and answer.
- **A remote dev environment.** Shells, browsers and build tools stay out of the pods. A Session acts only through the tools its Agent allows.
- **A general-purpose assistant.** Gotato is for purpose-built agents. A general agent needs people to step in, and a purpose-built one should not.
- **A workflow or graph language.** One loop covers the work, and ordinary Go code covers what comes before and after it.
- **An approval system.** The team chooses and writes the tools, and each tool checks its own arguments. A step that needs a person is a tool that hands the step over, for example by opening a ticket.
- **A separate platform.** A platform beside your services is a second system to deploy and operate.
- **Infrastructure.** Discovery, routing, autoscaling and deployment already exist where your services run.
- **Ready-made clients.** Bots, schedulers and front ends are the callers' own code. Gotato provides the endpoints they call.

The full list, with reasons: [FEATURES.md → Not planned](FEATURES.md#not-planned).

## Approach

- **Go**, with a core that uses only the standard library.
- **An Agent** defines, **a Session** runs it, and **a Runner** hosts Sessions inside your service.
- **The store is the truth.** A Session lives in your store as a log, and memory only keeps it warm. Any pod serves it, one at a time, wherever your load balancer sends the request.
- **Stable shapes** for a Session and its Events, which your adapters map onto your database and your telemetry.
- **Tools** are Go functions or MCP servers, each with an Effect.
- **Models** come through providers configured by wire API, or through your own client.

## Sources

- Anthropic, *Agent SDK overview* — https://code.claude.com/docs/en/agent-sdk/overview
- Anthropic, *Hosting the Agent SDK* — https://code.claude.com/docs/en/agent-sdk/hosting
- OpenAI, *Agents API: architecture* — https://developers.openai.com/api/docs/guides/agents-api/architecture
- kagent, *Go vs Python runtime* — https://kagent.dev/blog/go-vs-python-runtime
