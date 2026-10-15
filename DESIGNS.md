<!-- what-we-build: drafted 2026-10-16 -->
# Gotato — Designs

Every capitalized term on this page is defined in [GLOSSARY.md](GLOSSARY.md), which also gives each term's name in today's code.

An **Agent** definition (its model, instruction, tools and limits) and messages from callers go in, through the Go API, HTTP or gRPC. Answers, a stream of **Events** and a **Session** record in your store come out. In between, a **Runner** inside your service hosts the Session. The Session runs one **Loop** over the model and the tools, saves what each **Turn** commits, and leaves memory when it has been idle for its TTL.

This page holds the choices behind that shape: what each one costs, and how a reviewer can check that it still holds. It describes the design we intend. Where the code does not hold a principle yet, the text says "not yet" and links where the gap is tracked: [TODO.md](TODO.md) for a defect, [FEATURES.md](FEATURES.md) for a planned capability, [GOALS.md](GOALS.md) for a check not yet met. Beliefs live in [PHILOSOPHY.md](PHILOSOPHY.md), and behavior lives in [FEATURES.md](FEATURES.md).

## TL;DR

The principles by group, with their status: ✅ holds, 🚧 holds with a gap named, ⬜ not yet. Then the decisions and the open questions. The details follow below, in the same order.

**Loop**

| Principle | What it means | Status |
|---|---|---|
| One loop, no graph | the Loop calls the model and its tools until it answers; no workflow types | ✅ |
| Limits end every Run | every cap on Turns, tool calls, bytes and time has a default | ✅ |
| `context.Context` is the only cancellation | every call and stream takes a `ctx` | ✅ |
| Extensions wrap the Loop at fixed stages | seven stage interfaces; a new stage is a reviewed change | ✅ |

**Agent and Session**

| Principle | What it means | Status |
|---|---|---|
| An Agent is a definition, and a Session runs it | an Agent holds no conversation; one Agent runs many Sessions | ✅ |
| Agents are flat | no sub-agents or roles; lineage lives in Session metadata | ✅ |
| A Session stays generic | an application's structure lives in metadata | ✅ |
| The Transcript and the Context are separate values | the model gets what a `ContextBuilder` builds, never the Transcript itself | ✅ |

**State and the store**

| Principle | What it means | Status |
|---|---|---|
| The store is the truth, and memory is a cache | any instance can rebuild a Session from the store | ⬜ |
| Save every Turn | a crash loses at most the Turn in flight | ⬜ |
| A save never overwrites a newer one | a save from a stale copy fails with a conflict | ⬜ |
| Gotato owns the shape, and adapters own the schema | a versioned Session shape, mapped onto your database by your adapter | ✅ |

**Context and cost**

| Principle | What it means | Status |
|---|---|---|
| Stable content first, changing content last | so the provider's prompt cache keeps working | ✅ |
| Cache markers are neutral in the core | each wire API maps them to its own form | ✅ |
| The Context is inspectable | see what the model would get, without calling it | ✅ |

**Tools and safety**

| Principle | What it means | Status |
|---|---|---|
| Every tool declares its Effect | read, write local, write external or destructive; none counts as destructive | ✅ |
| An Agent caps the Effect it allows | a Run refuses a tool above the cap | ⬜ |
| Tools check their own arguments | a tool refuses arguments that are unsafe for it | ✅ |
| No shell and no sandbox in Gotato | tools are Go functions or MCP servers; MCP is not built yet | 🚧 |

**Models**

| Principle | What it means | Status |
|---|---|---|
| Core types belong to no vendor | provider data the core does not understand passes through untouched | ✅ |
| Providers are configured by wire API | a new vendor is a configuration entry | ✅ |

**Dependencies and layering**

| Principle | What it means | Status |
|---|---|---|
| The core uses only the standard library | the root module requires only `gopkg.in/yaml.v3` | ✅ |
| Dependencies point inward | the core never imports the service, the adapters or the CLI | ✅ |
| Heavy dependencies live in their own module | gRPC is a separate Go module | ✅ |
| The Go version stays low | the root `go.mod` says `go 1.22` | ✅ |

**Service layer**

| Principle | What it means | Status |
|---|---|---|
| Library first | a small `main` runs an Agent with a fake model, with no server | ✅ |
| The service sits on top | the service composes the core the way an application would | ✅ |
| The CLI is a thin client | every CLI behavior is reachable through the packages | ✅ |
| Contracts carry a version | HTTP and gRPC report their contract version | ✅ |

**Events and telemetry**

| Principle | What it means | Status |
|---|---|---|
| Events have a stable shape, and bind to no telemetry framework | an observer maps them onto your stack | ✅ |
| Events and `Usage` carry enough to compute every goal and benchmark | a team gets the numbers from its own telemetry | ⬜ |

**Failure**

| Case | What happens | Status |
|---|---|---|
| A tool fails, or the model gets a tool call wrong | a failed tool result tells the model why, and the Run goes on | ✅ |
| The model fails | the Run fails, and keeps every Turn it committed | ✅ |
| A limit is reached | the Run settles with a status that names the limit | ✅ |
| A Session is busy, or the Runner is full | the new Run waits or is rejected | ✅ |
| A save fails | the Run's error says the result was not persisted | ✅ |
| An instance stops gently | it drains its Runs | ✅ |
| An instance dies mid-Run | at most the Turn in flight is lost | ⬜ |
| A request reaches an instance that is not running the Session | still open | open |

**Testing**

| Principle | What it means | Status |
|---|---|---|
| Tests never call a network model | fakes and recorded replies cover the Loop | ✅ |

**Compatibility**

| Principle | What it means | Status |
|---|---|---|
| The contract is named | exported identifiers, CLI output, Event kinds, and the HTTP and gRPC contracts | ✅ |
| Breaks are rare, versioned and written down | every break has a before-and-after entry in the changelog | ✅ |

**When principles collide**

| Rule | What it says | Status |
|---|---|---|
| The order of priority | never lose a committed Turn, then stay minimal, then stay cheap; the Effect cap is never traded | — |

**Decisions**

| Decision | What we chose | Date |
|---|---|---|
| An Agent is a definition, and a Session is the live unit | the meaning the labs' agent APIs give both words | 2026-10-15 |
| The store is the truth, and memory is a cache | a live Session kept warm, with the store as the truth | 2026-10-15 |
| No approval system | the Effect cap, plus tools that check their own arguments | 2026-10-15 |
| Gotato owns the shapes, and adapters own storage and telemetry | stable shapes for a Session and its Events, mapped by adapters | 2026-10-15 |
| Providers are configured by wire API | one implementation per wire API | 2026-09-17 |

**Open questions**

| Question | What is open | Status |
|---|---|---|
| Ownership across instances | how one instance runs a Session at a time | open |
| What a warm Session holds | and what happens to a message that arrives mid-Run | open |
| How a Turn is saved | an append of the new Turn, or a rewrite of the whole Session | open |
| How an adapter proves it meets the store contract | for example, with a conformance suite | open |
| A model catalog | model names, context sizes and prices next to the providers | open |
| Compaction | when it becomes part of an Agent's definition | open |
| What the stored record is called | its name once Session means the live unit | open |

## Loop

### One loop, no graph

The Loop calls the model, runs the tools it asked for, and repeats until the model answers or a limit stops it (reason: [PHILOSOPHY.md → 1](PHILOSOPHY.md#1-what-actually-changed)). There is no drawn flow and no control per step, so a fixed sequence is written in Go around the Loop. This holds while the root package has one execution path, and no workflow, node or edge type exists anywhere in the module.

### Limits end every Run

`CoreLimits` caps Turns, tool calls, bytes and time, and every cap has a default. The cost is that a long task must be split, or must raise its limits on purpose. In return, every Run settles with a result and a status, including a Run that a limit stopped.

### `context.Context` is the only cancellation

Every model call, tool call, store call and stream takes a `ctx`, and the limit deadlines are contexts derived from it. The cost is that every API takes a `ctx`, even where a caller would rather not pass one.

### Extensions wrap the Loop at fixed stages

The stages are `RunPreparer`, `ContextTransformer`, `MessageConverter`, `PreToolUse`, `PostToolUse`, `EventObserver` and `TurnStopper`. Each stage is one interface (`RunPreparer` in `context.go`, the rest in `extensions.go`), and adding one is a reviewed change to the root package. The cost is that a need that fits no stage waits for a new stage instead of a fork, and that every stage runs on the Run's own goroutine, so a slow extension slows the Run.

## Agent and Session

### An Agent is a definition, and a Session runs it

An Agent holds no conversation, no state and no status, and one Agent runs many Sessions. The Agent definition has no field that belongs to one conversation. The cost is that callers learn two nouns instead of one.

### Agents are flat

No Gotato type is a sub-agent, a parent, a worker or a supervisor, and no type in the module carries a role or a parent Agent. Roles and lineage live in Session metadata, so a team that wants a hierarchy builds it in its own code.

### A Session stays generic

Task graphs, project state and product workflow live in a Session's metadata, and the Session has no field named after one application's concept. An application keeps its own structure in metadata, without typed fields.

### The Transcript and the Context are separate values

The Loop commits to the Transcript, and the model receives what a `ContextBuilder` builds from it. No code path sends the Transcript to a model without going through a `ContextBuilder`. The cost is that the Context is built again on every Turn.

## State and the store

### The store is the truth, and memory is a cache

A Session may stay warm in its Runner's memory while it is in use, but correctness never depends on it: any instance can rebuild the Session from the store. The TTL (60 idle minutes unless the Agent sets another) only removes the Session from memory, and the conversation stays in the store. The cost is a write for every committed Turn, and a load before a cold Session answers. The principle holds when killing an instance loses no committed Turn, and two instances never both run one Session. Not yet: neither check exists ([GOALS.md](GOALS.md) G-G01 and G-G08), and today the Runner keeps nothing in memory between Runs ([FEATURES.md](FEATURES.md) G-F27, "Warm Session and TTL").

### Save every Turn

Saving after each committed Turn means a crash in the middle of a Run loses at most the Turn in flight. That is a write per Turn, so a save must cost the size of the new Turn, not the size of the whole Session. Not yet: the Runner saves once, after the Run settles ([TODO.md](TODO.md)).

### A save never overwrites a newer one

A save made from a stale copy fails with a conflict, so two saves from the same starting copy cannot both succeed, and callers handle a conflict error. Not yet: `Store.Save` replaces the whole document with no revision check ([TODO.md](TODO.md)).

### Gotato owns the shape, and adapters own the schema

A saved Session has a stable, versioned shape (`schema_version`). An adapter in the application, or in an extension package, maps that shape onto MySQL, Postgres, Mongo or any other database, and owns its tables, collections and migrations. Teams write an adapter, because Gotato ships only a memory store and a file store. The root module has no database driver, and loading a Session with a newer `schema_version` fails with `ErrNotSupported`.

## Context and cost

### Stable content first, changing content last

Each Context is laid out as the instruction and the Static blocks (fixed reference text), then the tools sorted by ID, then the Transcript, then the Panel (small content that changes every Turn) on the last message. The Panel is never saved. The cost is that content that changes every Turn can only go at the end, and any change to the order breaks the provider's cache. `TestPrefixHashStableAcrossAppendOnlyTurns` keeps the prefix hash unchanged when only the Panel changes, and checks the cache breakpoints after the system part, the tools and the history.

### Cache markers are neutral in the core

The core marks where a provider may cache, and each wire API maps the marks to its own form, such as Anthropic's `cache_control`. The root package names no provider, so a provider feature with no neutral form waits for one.

### The Context is inspectable

Anyone can ask what the model would see for a Session now, without calling the model, and get the prefix hash with it. An agent's action is explained by what it saw, so this view is the first tool for debugging. `gotato context inspect` and `GET /v1/sessions/{id}/context` return the built Context. The cost is that a `ContextBuilder` must build without side effects.

## Tools and safety

### Every tool declares its Effect

The levels are read, write local, write external and destructive. A tool that declares nothing ranks as destructive, as `ToolEffect` in `tool.go` ranks it. The cost is that tool authors classify each tool.

### An Agent caps the Effect it allows

An Agent names its tools and the highest Effect any of them may have, so a read-only Agent cannot change anything, and a write tool needs an Agent that is allowed to write. A Run refuses a tool above its Agent's cap before calling it. Not yet: the Effect is recorded on each tool call, but no cap exists or is enforced ([FEATURES.md](FEATURES.md) G-F09, "Effect cap per Agent").

### Tools check their own arguments

A write that is safe for some arguments and not others is refused by the tool itself (see [No approval system](#no-approval-system--2026-10-15)). Tools are written with care, because the tools are the bounds.

### No shell and no sandbox in Gotato

Tools are Go functions in the service, or tools reached through MCP servers (reason: [PHILOSOPHY.md → 2](PHILOSOPHY.md#2-what-belongs-in-a-cloud-session)), and the module ships no tool that runs commands or code. Work that needs a machine is done elsewhere. Not yet: MCP support is on the *Later* list ([FEATURES.md](FEATURES.md) → Later, G-F12).

## Models

### Core types belong to no vendor

The model contract uses Gotato's own request and event types, and provider data that the core does not understand, such as reasoning signatures, travels through it untouched. Every wire API maps to and from these types, and the root package imports only the standard library.

### Providers are configured by wire API

A provider is configuration, and Gotato implements each wire API once. See [the decision](#providers-are-configured-by-wire-api--2026-09-17).

## Dependencies and layering

### The core uses only the standard library

The root package imports only the standard library, and the root module requires only `gopkg.in/yaml.v3`. So Gotato writes its own HTTP client for each wire API, instead of using vendor SDKs. `layering_test.go` fails on any non-standard import in the root package, and `go.mod` lists one requirement.

### Dependencies point inward

The core is at the center. `session`, `modelctx`, `toolregistry` and `testkit` build on it, `service` and `service/httpapi` build on those, and `cmd/gotato` and the gRPC adapter sit outside. A feature that needs the service layer cannot live in the core. `layering_test.go` fails when a package outside `cmd/` and `adapter/` imports `service`, `adapter` or `cmd`, apart from `service` itself.

### Heavy dependencies live in their own module

The gRPC adapter is a separate Go module (`adapter/grpc/go.mod`), so gRPC and protobuf never enter a service that does not use them, and the root `go.mod` does not mention gRPC. The cost is two modules to version and release.

### The Go version stays low

Embedding Gotato never forces a service to upgrade Go. The root `go.mod` says `go 1.22`, and newer language features wait.

## Service layer

### Library first

A small `main` runs an Agent with a fake model in-process, with no server, daemon or database. The root package's tests do exactly that, with `testkit` fakes, no network and no store beyond memory. The cost is that the service layer cannot add anything the library needs to work.

### The service sits on top

`service`, `service/httpapi` and the gRPC adapter compose the core packages the way an application would, and the layering test above keeps it so. The service layer repeats some wiring that a framework would hide.

### The CLI is a thin client

Every CLI behavior is reachable through the packages, and `cmd/gotato` holds presentation only. Its `--json` and `--jsonl` output and its exit codes are part of the contract, and `cmd/gotato/cli_test.go` asserts on JSON. A CLI feature first needs a package API.

### Contracts carry a version

The HTTP and gRPC contracts each report a contract version, currently `"2"`, on `GET /healthz` and through the gRPC contract call. The cost is that a breaking change to a contract needs a version bump and a [CHANGELOG.md](CHANGELOG.md) entry.

## Events and telemetry

### Events have a stable shape, and bind to no telemetry framework

An observer in the application, or in an extension package, maps Events onto OpenTelemetry or whatever stack the team already runs. The root module imports no telemetry package, so there is no tracing out of the box.

### Events and `Usage` carry enough to compute every goal and benchmark

Cache hits, token counts, tool results with their cause, stop reasons and timings all travel with Events or `Usage`, so a team gets the numbers in [GOALS.md](GOALS.md) from its own telemetry, not from Gotato's internal state. The cost is that Events carry fields some teams never read. The check is that the reference workload computes its numbers from recorded Events alone. Not yet: the reference workload does not exist.

## Failure

### A tool fails, or the model gets a tool call wrong

The call settles as a failed tool result that tells the model why, and the Run goes on. The Turn and tool-call limits bound a model that keeps getting it wrong.

### The model fails

The Run settles as failed, with the error. The Transcript keeps every Turn committed before the failure, and the caller can continue the Session.

### A limit is reached

The Run settles with a status that names the limit.

### A Session is busy, or the Runner is full

When a Session is busy, the Runner either rejects the new Run as busy or makes it wait, as the Runner's admission policy says. When the Runner is full, it rejects the Run with a capacity error.

### A save fails

The Run's error says the result was not persisted. The caller retries or reports it.

### An instance stops gently

`Drain` stops new Runs, waits for the Runs in flight, and cancels them when its deadline passes.

### An instance dies mid-Run

The intended result is that at most the Turn in flight is lost. Not yet: today the whole Run is lost ([TODO.md](TODO.md)).

### A request reaches an instance that is not running the Session

What happens to a message, a cancel or a stream request there is still open. Today, cancellation and streams work only on the instance that runs the Session (see [Open questions](#open-questions)).

## Testing

### Tests never call a network model

Fake and replay models, a fake tool and an event recorder cover the Loop, and CI runs with no provider credentials. The cost is that provider behavior is checked against recorded replies, not live ones.

## Compatibility

### The contract is named

The contract is the exported identifiers of the root package and the standard runtime packages; the CLI's commands, flags, JSON fields, JSONL event shapes and exit codes; the Event kinds and their documented payload keys; and the HTTP and gRPC contracts, which carry their own version. Every change to these is reviewed as a contract change. Additions are free, and consumers tolerate unknown Event kinds. A rename or a removal goes through a release with a `// Deprecated:` marker when that is cheap.

### Breaks are rare, versioned and written down

A confusing abstraction can still be fixed by a planned break, so some fixes wait for that break. Every break has a before-and-after entry in [CHANGELOG.md](CHANGELOG.md) (GOALS.md G-G06).

## When principles collide

Never lose a committed Turn, then stay minimal, then stay cheap. A lost conversation breaks trust in the service. Weight is paid by every service that embeds Gotato, for as long as it does. Cost can be tuned later. The Effect cap is not part of the trade: an Agent never runs a tool above its cap to save a Turn or a write.

## Decisions

### An Agent is a definition, and a Session is the live unit — 2026-10-15

The words now match how the labs' agent APIs use them, so a reader who knows those APIs reads Gotato correctly without a note.

| Option | For | Against |
|---|---|---|
| **Agent = definition, Session = live unit** (chosen) | Matches Claude Managed Agents and the OpenAI Agents API. One Agent running many Sessions reads naturally. | The code uses the old names until a planned rename. |
| Agent = live unit, with no term "session" | One noun for callers. | The reverse of the labs' usage, so every reader needs a note. |
| Keep today's code names: Agent = the loop for one Run, Session = the stored record | No rename. | The thing a caller talks to has no name of its own. |

### The store is the truth, and memory is a cache — 2026-10-15

A Session behaves like a live unit while it is in use, and like a record while it waits.

| Option | For | Against |
|---|---|---|
| **A live unit kept warm, with the store as the truth** (chosen) | Holds a Session's queue, stream and status while it works. Survives the loss of any instance. The TTL only frees memory. | A write per Turn. Machinery to load and to let go. |
| Nothing in memory: load and save around every Run (today's code) | The fewest moving parts. | The Session has no queue of its own, so a message that arrives mid-Run can only wait or be rejected. |
| A long-lived unit in memory that owns its state | Fast, and natural for queues and streams. | Lost with its instance. Pins each conversation to one instance. |

An earlier version of Gotato kept agents in memory for their whole life, and a later one replaced them with a stored record. This decision brings a long-lived unit back. It is safe this time, because a Session's identity is its record, not the memory that holds it.

### No approval system — 2026-10-15

Gotato is for purpose-built agents, whose tools the team chooses and writes (reason: [PHILOSOPHY.md → 3](PHILOSOPHY.md#3-how-is-a-cloud-session-different-from-a-local-one)).

| Option | For | Against |
|---|---|---|
| **The Effect cap, plus tools that check their own arguments** (chosen) | No waiting state in the Loop. The bounds are visible in the Agent and the tools. | Tools must be written with care. |
| Built-in approval that pauses a Run for a person | Familiar from general assistants. | Treats a broken stack as normal. A paused Run must survive restarts, which adds a waiting state to every Session. |
| Leave it to extensions only | No new concept. | No bounds by default. |

A team that still wants an extra check can add one with the `PreToolUse` extension. A step that needs a person by design is a tool that hands it over, for example by opening a ticket.

### Gotato owns the shapes, and adapters own storage and telemetry — 2026-10-15

Databases model schemas differently, and teams run different telemetry stacks, so neither belongs in the core.

| Option | For | Against |
|---|---|---|
| **Stable shapes for a Session and its Events, mapped by adapters** (chosen) | Works with any database and any telemetry stack. Adds no dependency to the service. | Each team writes or picks an adapter. |
| Ship a SQL store in the core | Works on day one for SQL users. | Drivers, migrations and one schema for everyone. No help for Mongo. |
| Ship OpenTelemetry in the core | Tracing on day one. | A heavy dependency for every service, including those on another stack. |

### Providers are configured by wire API — 2026-09-17

A provider is configuration: an endpoint, a key, a wire API and its models. Gotato implements each wire API once (`openai-chat-completions`, `openai-responses` and `anthropic-messages`), and any vendor that speaks one of them works without new code.

| Option | For | Against |
|---|---|---|
| **One implementation per wire API** (chosen) | A new vendor is a config entry. No vendor SDK in the service. | A vendor with a new wire API waits for its implementation. |
| One adapter per vendor | Each vendor's full feature set. | Code for every vendor, and the list never ends. |
| Vendor SDKs | Maintained by the vendors. | Their dependencies land in every service. |

## Open questions

### Ownership across instances

This gets its own design discussion before anything is built. One requirement is already fixed: whatever the mechanism, a second instance stops before it calls the model, so it never spends tokens on a Turn it cannot commit. A conflict found only when saving is too late, because both instances have already paid for the model call. The discussion must answer these questions:

| Question | Options on the table |
|---|---|
| How is ownership of a Session expressed? | a lease with an expiry; a revision compared on save; both |
| Where does ownership live? | in the store; in a separate lock service |
| What happens when a message reaches an instance that does not own the Session? | forward it to the owner; wait; reject it as busy; take over |
| How is a crashed owner detected? | the lease expires; a heartbeat |
| How is a stale owner stopped from writing after it lost ownership? | fencing tokens: each new owner gets a higher number, and older numbers are refused |
| How do cancellation and live streams reach the owner? | today both work only on the instance that runs the Session |
| How does ownership work with the TTL? | the owner lets go when the TTL frees the Session's memory, or earlier |

### What a warm Session holds

What a warm Session keeps in memory is open, and so is what happens to a message that arrives while a Run is in progress: it is queued, steered into the Run, or rejected as busy.

### How a Turn is saved

A save is either an append of the new Turn or a rewrite of the whole Session. Today each save also carries up to 1,000 Events inside the Session, so every save grows with the Session's history.

### How an adapter proves it meets the store contract

One way is a conformance suite in `testkit`.

### A model catalog

A catalog next to the providers would list model names, context sizes and prices.

### Compaction

Compaction exists in the code, and it is not part of an Agent's definition until a later phase.

### What the stored record is called

Today `gotato.Agent` is the Loop, `service.AgentSpec` is the Agent, and `session.Session` is the stored record. Once the name Session belongs to the live unit, the record needs a name of its own. The Agent and the Loop take their glossary names in Phase 3, and the record takes its own in Phase 6, each in a planned break with a [CHANGELOG.md](CHANGELOG.md) entry ([ROADMAP.md](ROADMAP.md)).

Smaller open decisions about Events and extensions are tracked in [TODO.md](TODO.md) → Decisions.
