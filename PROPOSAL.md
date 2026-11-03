<!-- what-we-build: drafted 2026-11-03 -->
# Gotato — Proposal

> **Agent-as-a-Service for Go.**
>
> A minimal Go library that turns cloud services into cloud agents: distributed, efficient and stable from day 1.

## In short

A Go service that answers customers about their orders meets a question its rules cannot settle: a double charge, a canceled flight, a refund outside the policy. A model with three tools could settle it by looking up the order, reading the policy and answering. The agent stacks on offer arrive in another language, bring a machine for each conversation and keep the conversation in one process, while the service runs on many pods that come and go.

Gotato runs purpose-built agents inside the Go services you already operate. It does one thing: it makes the agent session small, efficient and safe to run on many pods. A purpose-built agent looks things up, decides and answers, and for that a cloud-hosted full session with a machine for each conversation is a waste of resources. A session needs two parts:

- a **loop** that calls the model and the tools it asks for;
- a **runtime** around the loop that keeps the conversation's state in your store and lets it go when it is idle.

> **A Gotato Session is a minimal agentic loop and a runtime.**

## Why it should exist

Agents are moving into the services they serve, and those services are Go services on many pods. An agent should be as easy to add to such a service as any other library: it builds, deploys and scales with the service.

Take an order service written in Go that wants an agent to answer "where is my order?". The agent needs the service's own lookup tools, its own database and its own authentication. Written as a separate Python service, it needs all three again, over the network. Written as a package inside the service, it uses them directly.

### Pain points

When a Go service needs an agent today, four things get in the way.

#### The agent stack is not Go

The established agent stacks are written in Python, TypeScript or Rust. A Go service that adopts one takes on:

- a second language and a second build;
- a second deployment;
- a second set of dependencies to keep up to date.

The agent ends up as a separate system beside the service it works for.

#### Graph frameworks are heavy for the work

Agents are often assembled on graph frameworks such as LangGraph, Deep Agents or Langflow, with a node or a branch for each case. These setups are expensive to run, slow, and hard to develop and debug. We have not found them clearly better than one loop:

```text
call the model ──► run the tools it asked for ──► repeat until it answers
```

Time spent drawing and debugging the graph is time not spent on the parts only the team can build: authentication, tool sets, MCP servers and monitoring.

#### The conversation lives in one process

A Go service on Kubernetes runs on many pods, which the autoscaler adds and removes. An agent stack that keeps the conversation in process memory loses it when its pod goes away:

```text
pod A: conversation in memory ──► rolling deploy ──► gone
next message ──► pod B ──► "never seen this conversation"
```

Pinning each conversation to one pod sends the next message to the right place until that pod goes away. It also gives up much of what scaling is for.

Katelyn Lesse makes the same point from the infrastructure side: Kubernetes was built for stateless applications, while an agent holds state and spends much of its time idle, waiting on a tool, a model or a person (Lesse, *The new set of problems for engineers to solve*).

#### A simple task carries a coding agent's weight

Coding-agent harnesses are built to write code, so each session brings a machine with it. Anthropic's hosting guide for its Agent SDK gives this starting point for each session:

```text
1 GiB RAM · 5 GiB disk · 1 CPU · one subprocess
```

The guide calls the 1 GiB "a floor, not the ceiling". A conversation is idle most of the time, so this cost runs while nothing happens.

## Why now

Six things changed, and each eases one of these pains.

**1. From one model call to an agent loop.** An AI feature used to be one prompt and one reply. Now an agent calls the model and its tools many times for one request, and keeps the conversation for the next one.

**2. From graph frameworks to a thin harness.** The labs settled on one shape, a loop around the model, its tools and its context, and ship it as a product. As models get better at planning and at recovering from errors, they take over the work that graph nodes used to do.

> "The Agent SDK gives you the same tools, agent loop, and context management that power Claude Code, programmable in Python and TypeScript." — Anthropic, *Agent SDK overview*

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

**5. From Python, TypeScript and Rust to Go.** Agent tooling now arrives in the language that cloud services run on: Google ADK for Go, Eino and Genkit for Go. kagent measured the same minimal agent at 253 Mi idle on its Python runtime and 7 Mi on its Go runtime.

**6. From coding agents to purpose-built agents.** Coding agents are the best known. Most agent work inside a running system is narrower, and needs tools and data rather than a machine:

- the hard cases inside a pipeline, after rules and a single model call run out;
- investigations that an alert or a schedule starts and a report ends;
- conversations with users that need the service's own tools and records.

## The idea

Gotato keeps each conversation as a record in your store and runs it as a small loop on whichever pod gets the next message.

It splits an agent in two. An **Agent** is a definition, written once. A **Session** is one conversation that runs it:

```text
Agent      a definition: model · instruction · tools · limits
  │ runs many
  ▼
Session    one conversation: a loop and a runtime
  ├─ loop        calls the model and the tools
  └─ Transcript  everything that happened, kept in your store
```

Two more terms are used below. A **Turn** is one model call and the tools it asked for. A **Runner** is the part of your service that hosts Sessions.

Between messages, a Session is its record in your store: a log that only grows, one step at a time. A pod keeps it in memory while it is in use and lets it go once it has been idle for a set time. The conversation stays in your store, so any pod can load it again and continue.

A Session needs no process, shell or sandbox of its own. A Go service hosts Sessions the way it serves any other request.

## Where Gotato fits

Several good options exist for building agents, and each makes a different choice. Gotato's choice is the smallest library that still runs on many pods.

| Option | What it offers | What Gotato does instead |
|---|---|---|
| Lab harness libraries, such as the Claude Agent SDK | the full coding-agent harness in Python or TypeScript, with one subprocess per session | a Session is a goroutine while it works and a record in your store while it waits |
| Lab-hosted agents, such as Claude Managed Agents and the OpenAI Agents API | the loop run for you, with the session kept on their side | the loop runs inside your service, with any provider |
| Go composition frameworks, such as Eino | components and graphs to assemble, with conversation storage left to the application | one loop with Sessions built in |
| Batteries-included Go toolkits, such as Google ADK for Go | agents, workflows, sub-agents, A2A, database sessions and a REST server | one loop and one Session |

The footprint follows from that choice. A program that imports each library's agent, runner, session store and HTTP server, and does nothing else, measures:

```text
                                   Gotato        Google ADK for Go
modules in the build graph         3 (4 listed)  128
non-standard-library packages      15            370
binary, stripped                   9.0 MB        14.8 MB
minimum Go version                 1.24          1.26.6
```

Gotato was measured on 2026-11-03 with `bench/refservice/footprint.sh` (Go 1.27.2, Apple M3) on the reference service, which also imports the model bridge. ADK for Go v2.6.0 was measured on 2026-10-15 on an Apple M3.

## What we want

Gotato should behave like any other library in the service: easy to add, safe to scale and simple to follow.

### It fits into your service

- **One build and one deploy:** Gotato arrives with `go get` and goes through the CI/CD the team already runs.
- **A small footprint:** the core depends on the Go standard library alone, and the figures above show what the rest adds.
- **Your storage and your telemetry:** Gotato defines stable shapes for a Session and the Events it reports, and an adapter maps them onto the database and the telemetry stack you run.
- **Any model provider:** a vendor that speaks a supported wire API works through configuration alone.
- **Common endpoints:** a Go API and an optional HTTP API let a bot, a job or another service talk to a Session like any other API.

### It runs on many pods

- **No lost conversations:** a Session saves every step as it happens, so when its pod goes away another pod loads it and continues.
- **Any pod answers like the only one:** a request can reach any pod and no Session is bound to one, so the load balancer you already run decides where each request goes.
- **One pod at a time:** two pods never run or write the same Session at once.
- **Idle costs almost nothing:** a waiting Session holds no process or sandbox, and after an idle time it holds no memory.
- **A lower token bill:** each request to the model is laid out so that the provider can reuse its cached prefix from one Turn to the next.

### It stays simple

Gotato owns the loop and the Session, and everything that differs from team to team plugs in:

```text
Gotato:  loop ── Session
you:     store · model provider · tools · MCP servers · auth · telemetry
```

- **One loop and no workflow language:** the model plans the steps and ordinary Go code decides when to call an agent.
- **You see what the model sees:** you can inspect each request exactly as it is sent.
- **Safe tools without a sandbox:** an Agent can use only the tools it names and none above its highest allowed Effect, which says how much a tool can change.
- **Tools that check themselves:** each tool checks its own arguments, since no approval step stands in front of it.

### It stays stable

- **Stable contracts:** the Go API, the HTTP contract and the shape of a saved Session change only in a versioned break that comes with a before-and-after note.

## What we don't want

- **A cloud coding agent:** coding stays on developers' machines and in coding products, since Sessions in the cluster investigate and decide.
- **A remote dev environment:** shells, browsers and build tools stay out of the pods so that a Session acts only through its Agent's tools.
- **A general-purpose assistant:** Gotato is for purpose-built agents, because a general agent needs people to step in and a purpose-built one should not.
- **A workflow or graph language:** one loop covers the work and ordinary Go code covers what comes before and after it.
- **An approval system:** the team writes the tools and each tool checks its own arguments, so a step that needs a person is a tool that opens a ticket for one.
- **A separate platform:** a platform beside your services is a second system to deploy and operate.
- **Infrastructure:** routing and discovery, autoscaling and deployment already exist where your services run.
- **Ready-made clients:** bots, schedulers and front ends are the callers' own code that calls Gotato's endpoints.

## Who it is for

| Who | What they get |
|---|---|
| The team that builds a Go service | an agent that is a package in its own code, with tools written in Go and the CI and deploy it already has |
| The team that runs the service | no new system to operate, and no conversation lost when a pod is replaced |
| Callers such as a bot, a job or another service | an endpoint to start and talk to a Session, like any other API |
| The people who talk to the agent | a conversation that goes on across messages, pods and deploys |

## Risks

- **Models stop getting better at planning:** a thin loop would then do less than a graph did, and more care in the tools and the instruction reduces it without a change in Gotato.
- **A small deployment pays for a large design:** a team with a handful of conversations carries a store write for every step, and a Session built with the Go API alone keeps its record in memory and writes nothing.
- **A purpose-built agent grows general:** teams keep adding tools until it needs people again, and a new job as a new Agent with its own tools and Effect cap reduces it.
- **A resumed conversation pays for its prefix again:** a provider keeps a cached prefix for minutes or an hour at most, and Gotato compacts a waiting Session before its cache expires so that the first request back is smaller (not yet measured).
- **Provider behavior is mostly checked on fakes:** CI has no provider credentials, and what reduces it is a live run of the reference workload that a team can make against its own endpoint.
- **Larger toolkits may add what Gotato offers:** the footprint is measured against Google ADK for Go, and what reduces it is that Gotato keeps to one loop and one Session.

## Sources

- Anthropic, *Agent SDK overview* — https://code.claude.com/docs/en/agent-sdk/overview
- Anthropic, *Hosting the Agent SDK* — https://code.claude.com/docs/en/agent-sdk/hosting
- OpenAI, *Agents API: architecture* — https://developers.openai.com/api/docs/guides/agents-api/architecture
- kagent, *Go vs Python runtime* — https://kagent.dev/blog/go-vs-python-runtime
- Katelyn Lesse, *The new set of problems for engineers to solve* — https://x.com/katelyn_lesse/status/2114542280535318557
