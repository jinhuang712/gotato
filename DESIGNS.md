<!-- what-we-build: drafted 2026-10-16 -->
# Gotato — Designs

Every capitalized term on this page is defined in [GLOSSARY.md](GLOSSARY.md), which also gives each term's name in today's code.

An **Agent** definition (its model, instruction, tools and limits) and messages from callers go in, through the Go API, HTTP or gRPC. Answers, a stream of **Events** and a **Session** record in your store come out. In between, a **Runner** inside your service hosts the Session. The Session runs one **Loop** over the model and the tools, saves what each **Turn** commits, and leaves memory when it has been idle for its TTL.

This page holds the choices behind that shape: what each one costs, and how a reviewer can check that it still holds. It describes the design we intend, not the state of the code: what is built is tracked in [FEATURES.md](FEATURES.md), open defects in [TODO.md](TODO.md), and the checks in [GOALS.md](GOALS.md). Beliefs live in [PHILOSOPHY.md](PHILOSOPHY.md), and behavior lives in [FEATURES.md](FEATURES.md).

## TL;DR

The principles by group, then the decisions and the open questions. The details follow below, in the same order.

**Loop**

| Principle | What it means |
|---|---|
| One loop, no graph | the Loop calls the model and its tools until it answers; no workflow types |
| Limits end every Run | every cap on Turns, tool calls, bytes and time has a default |
| `context.Context` is the only cancellation | every call and stream takes a `ctx` |
| Extensions wrap the Loop at fixed stages | seven stage interfaces; a new stage is a reviewed change |

**Agent and Session**

| Principle | What it means |
|---|---|
| An Agent is a definition, and a Session runs it | an Agent holds no conversation; one Agent runs many Sessions |
| Agents are flat | no sub-agents or roles; lineage lives in Session metadata |
| A Session stays generic | an application's structure lives in metadata |
| The Transcript and the Context are separate values | the model gets what a `ContextBuilder` builds, never the Transcript itself |

**State and the store**

| Principle | What it means |
|---|---|
| The store is the truth, and memory is a cache | any instance can rebuild a Session from the store |
| Save every Turn | a crash loses at most the Turn in flight |
| A save never overwrites a newer one | a save from a stale copy fails with a conflict |
| Gotato owns the shape, and adapters own the schema | a versioned Session shape, mapped onto your database by your adapter |

**Context and cost**

| Principle | What it means |
|---|---|
| Stable content first, changing content last | so the provider's prompt cache keeps working |
| Cache markers are neutral in the core | each wire API maps them, and the Session's cache key, to its own form |
| The Context is inspectable | see what the model would get, without calling it |

**Tools and safety**

| Principle | What it means |
|---|---|
| Every tool declares its Effect | read, write local, write external or destructive; none counts as destructive |
| An Agent caps the Effect it allows | a Run refuses a tool above the cap |
| Tools check their own arguments | a tool refuses arguments that are unsafe for it |
| No shell and no sandbox in Gotato | tools are Go functions or MCP servers; MCP is not built yet |

**Models**

| Principle | What it means |
|---|---|
| Core types belong to no vendor | provider data the core does not understand passes through untouched |
| Providers are configured by wire API | a new vendor is a configuration entry |

**Dependencies and layering**

| Principle | What it means |
|---|---|
| The core uses only the standard library | the root module requires only `gopkg.in/yaml.v3` |
| Dependencies point inward | the core never imports the service, the adapters or the CLI |
| Heavy dependencies live in their own module | gRPC is a separate Go module |
| The Go version stays low | the root `go.mod` says `go 1.22` |

**Service layer**

| Principle | What it means |
|---|---|
| Library first | a small `main` runs an Agent with a fake model, with no server |
| The service sits on top | the service composes the core the way an application would |
| The CLI is a thin client | every CLI behavior is reachable through the packages |
| Contracts carry a version | HTTP and gRPC report their contract version |

**Events and telemetry**

| Principle | What it means |
|---|---|
| Events have a stable shape, and bind to no telemetry framework | an observer maps them onto your stack |
| Events and `Usage` carry enough to compute every number about a Run | a team gets those numbers from its own telemetry |

**Failure**

| Case | What happens |
|---|---|
| A tool fails, or the model gets a tool call wrong | a failed tool result tells the model why, and the Run goes on |
| The model fails | the Run fails, and keeps every Turn it committed |
| A limit is reached | the Run settles with a status that names the limit |
| A Session is busy, or the Runner is full | the new Run waits or is rejected |
| A save fails | the Run's error says the result was not persisted |
| An instance stops gently | it drains its Runs |
| An instance dies mid-Run | at most the Turn in flight is lost |
| A request reaches an instance that is not running the Session | still open |

**Testing**

| Principle | What it means |
|---|---|
| Tests never call a network model | fakes and recorded replies cover the Loop |

**Compatibility**

| Principle | What it means |
|---|---|
| The contract is named | exported identifiers, CLI output, Event kinds, and the HTTP and gRPC contracts |
| Breaks are rare, versioned and written down | every break has a before-and-after entry in the changelog |

**When principles collide**

| Rule | What it says |
|---|---|
| The order of priority | never lose a committed Turn, then stay minimal, then stay cheap; the Effect cap is never traded |

**Decisions**

| Decision | What we chose | Date |
|---|---|---|
| An Agent is a definition, and a Session is the live unit | the meaning the labs' agent APIs give both words | 2026-10-15 |
| The store is the truth, and memory is a cache | a live Session kept warm, with the store as the truth | 2026-10-15 |
| No approval system | the Effect cap, plus tools that check their own arguments | 2026-10-15 |
| Gotato owns the shapes, and adapters own storage and telemetry | stable shapes for a Session and its Events, mapped by adapters | 2026-10-15 |
| Providers are configured by wire API | one implementation per wire API | 2026-09-17 |
| Adapters prove the store contract with a conformance suite | one suite in `testkit`, shipped with the Phase 4 store contract | 2026-10-16 |

**Open questions**

| Question | What is open |
|---|---|
| Ownership across instances | how one instance runs a Session at a time |
| What a warm Session holds | what happens to a message that arrives mid-Run, and which Events report it |
| How a Turn is saved | an append of the new Turn, or a rewrite of the whole Session |
| A model catalog | model names, context sizes and prices next to the providers |
| Compaction | how a Session is compacted, before its TTL and otherwise, and when it becomes part of an Agent's definition |
| What the stored record is called | its name once Session means the live unit |

## Loop

### One loop, no graph

The Loop calls the model, runs the tools it asked for, and repeats until the model answers or a limit stops it:

```text
call the model ──► run the tools it asked for ──► repeat until it answers or a limit stops it
```

- **Why:** [PHILOSOPHY.md → 1](PHILOSOPHY.md#1-what-actually-changed).
- **Cost:** there is no drawn flow and no control per step, so a fixed sequence is written in Go around the Loop.
- **Check:** the root package has one execution path, and no workflow, node or edge type exists anywhere in the module.

### Limits end every Run

`CoreLimits` caps Turns, tool calls, bytes and time, and every cap has a default.

- **Cost:** a long task must be split, or must raise its limits on purpose.
- **Check:** every Run settles with a result and a status, including a Run that a limit stopped.

### `context.Context` is the only cancellation

Cancellation travels through `context.Context`.

- **Cost:** every API takes a `ctx`, even where a caller would rather not pass one.
- **Check:** every model call, tool call, store call and stream takes a `ctx`, and limit deadlines are contexts derived from it.

### Extensions wrap the Loop at fixed stages

The stages are `RunPreparer`, `ContextTransformer`, `MessageConverter`, `PreToolUse`, `PostToolUse`, `EventObserver` and `TurnStopper`.

- **Cost:** a need that fits no stage waits for a new stage instead of a fork. Every stage runs on the Run's own goroutine, so a slow extension slows the Run.
- **Check:** each stage is one interface (`RunPreparer` in `context.go`, the rest in `extensions.go`), and adding one is a reviewed change to the root package.

## Agent and Session

### An Agent is a definition, and a Session runs it

An Agent holds no conversation, no state and no status. One Agent runs many Sessions:

```text
Agent (a definition) ──► Session A
                     ──► Session B
                     ──► Session C
```

- **Cost:** callers learn two nouns instead of one.
- **Check:** the Agent definition has no field that belongs to one conversation.

### Agents are flat

No Gotato type is a sub-agent, a parent, a worker or a supervisor. Roles and lineage live in Session metadata.

- **Cost:** a team that wants a hierarchy builds it in its own code.
- **Check:** no type in the module carries a role or a parent Agent.

### A Session stays generic

Task graphs, project state and product workflow live in a Session's metadata.

- **Cost:** an application keeps its own structure in metadata, without typed fields.
- **Check:** the Session has no field named after one application's concept.

### The Transcript and the Context are separate values

The Loop commits to the Transcript, and the model receives what a `ContextBuilder` builds from it:

```text
Transcript (everything committed) ──► ContextBuilder ──► Context (what the model sees this Turn)
```

- **Cost:** the Context is built again on every Turn.
- **Check:** no code path sends the Transcript to a model without going through a `ContextBuilder`.

## State and the store

### The store is the truth, and memory is a cache

A Session may stay warm in its Runner's memory while it is in use, but correctness never depends on it:

```text
caller ──► any instance ──► warm Session in memory (a cache)
                                   │ saves every committed Turn
                                   ▼
                             store (the truth)
```

- Any instance can rebuild the Session from the store.
- The TTL (60 idle minutes unless the Agent sets another) removes the Session from memory. The conversation stays in the store.
- Before it leaves, the Session is compacted, unless a setting turns this off. By then the provider's prompt cache has expired, so the next Run starts cold either way, and a shorter history costs less to send again.
- **Cost:** a write for every committed Turn, and a load before a cold Session answers. Compacting before the TTL loses detail, and it costs a summary for every Session that leaves, including one that never comes back.
- **Check:** killing an instance loses no committed Turn ([GOALS.md](GOALS.md) G-G01), and two instances never both run one Session (G-G08).

### Save every Turn

A Session is saved after each Turn it commits.

- **Cost:** a write per Turn, so a save must cost the size of the new Turn, not the size of the whole Session.
- **Check:** a crash in the middle of a Run loses at most the Turn in flight.

### A save never overwrites a newer one

A save made from a stale copy fails with a conflict.

- **Cost:** callers handle a conflict error.
- **Check:** two saves from the same starting copy cannot both succeed.

### Gotato owns the shape, and adapters own the schema

A saved Session has a stable, versioned shape (`schema_version`). An adapter in the application, or in an extension package, maps that shape onto MySQL, Postgres, Mongo or any other database, and owns its tables, collections and migrations.

- **Cost:** teams write an adapter, because Gotato ships only a memory store and a file store.
- **Check:** the root module has no database driver, and loading a Session with a newer `schema_version` fails with `ErrNotSupported`.

## Context and cost

### Stable content first, changing content last

Each Context is laid out in this order, from the most stable part to the part that grows:

```text
instruction · Static blocks  →  tools, sorted by ID  →  Transcript
```

- Static blocks are fixed reference text.
- Nothing that changes every Turn is added. Such content is never cached, so it would cost its full price on every Turn. A fact the model needs now, such as the current time, comes from a tool it calls.
- **Cost:** the model learns a changing fact only by calling a tool, and any change to the order breaks the provider's cache.
- **Check:** `TestPrefixHashStableAcrossAppendOnlyTurns` keeps the prefix hash unchanged when only runtime fields such as message IDs change, and checks the cache breakpoints after the system part, the tools and the history.

### Cache markers are neutral in the core

The core marks where a provider may cache, and each wire API maps the marks to its own form, such as Anthropic's `cache_control`. A cache key, one per Session, tells a provider which requests belong to one cache; the gateway's configuration says whether it travels as a body field such as `prompt_cache_key` or as a header.

- **Cost:** a provider feature with no neutral form waits for one.
- **Check:** the root package names no provider.

### The Context is inspectable

Anyone can ask what the model would see for a Session now, without calling the model, and get the prefix hash with it. An agent's action is explained by what it saw, so this view is the first tool for debugging. The Agent builds the view with the code its Turns run, so the two cannot drift apart.

- **Cost:** a `ContextBuilder`, the context extensions and every tool source must build without side effects, because inspection runs them too.
- **Check:** the reference workload compares every Turn's prefix hash with an inspection of that Turn's Transcript ([GOALS.md](GOALS.md) G-G03).

## Tools and safety

### Every tool declares its Effect

The levels, from the least impact to the most:

```text
read  <  write local  <  write external  <  destructive
```

- A tool that declares nothing ranks as destructive.
- **Cost:** tool authors classify each tool.
- **Check:** `ToolEffect` in `tool.go` ranks an unspecified Effect with destructive.

### An Agent caps the Effect it allows

An Agent names its tools and the highest Effect any of them may have, so a read-only Agent cannot change anything.

- **Cost:** a write tool needs an Agent that is allowed to write.
- **Check:** a Run refuses a tool above its Agent's cap before calling it.

### Tools check their own arguments

A write that is safe for some arguments and not others is refused by the tool itself (see [No approval system](#no-approval-system--2026-10-15)).

- **Cost:** tools are written with care, because the tools are the bounds.

### No shell and no sandbox in Gotato

Tools are Go functions in the service, or tools reached through MCP servers.

- **Why:** [PHILOSOPHY.md → 2](PHILOSOPHY.md#2-what-belongs-in-a-cloud-session).
- **Cost:** work that needs a machine is done elsewhere.
- **Check:** the module ships no tool that runs commands or code.

## Models

### Core types belong to no vendor

The model contract uses Gotato's own request and event types. Provider data that the core does not understand, such as reasoning signatures, travels through it untouched.

- **Cost:** every wire API maps to and from these types.
- **Check:** the root package imports only the standard library.

### Providers are configured by wire API

A provider is configuration, and Gotato implements each wire API once. See [the decision](#providers-are-configured-by-wire-api--2026-09-17).

## Dependencies and layering

### The core uses only the standard library

The root package imports only the standard library, and the root module requires only `gopkg.in/yaml.v3`.

- **Cost:** Gotato writes its own HTTP client for each wire API, instead of using vendor SDKs.
- **Check:** `layering_test.go` fails on any non-standard import in the root package, and `go.mod` lists one requirement.

### Dependencies point inward

The core is at the center, and each layer builds only on the layers inside it:

```text
cmd/gotato, gRPC adapter  ──►  service, service/httpapi  ──►  session, modelctx, toolregistry, testkit  ──►  core
```

- **Cost:** a feature that needs the service layer cannot live in the core.
- **Check:** `layering_test.go` fails when a package outside `cmd/` and `adapter/` imports `service`, `adapter` or `cmd`, apart from `service` itself.

### Heavy dependencies live in their own module

The gRPC adapter is a separate Go module, so gRPC and protobuf never enter a service that does not use them.

- **Cost:** two modules to version and release.
- **Check:** `adapter/grpc/go.mod` exists, and the root `go.mod` does not mention gRPC.

### The Go version stays low

Embedding Gotato never forces a service to upgrade Go.

- **Cost:** newer language features wait.
- **Check:** the root `go.mod` says `go 1.22`.

## Service layer

### Library first

A small `main` runs an Agent with a fake model in-process, with no server, daemon or database.

- **Cost:** the service layer cannot add anything the library needs to work.
- **Check:** the root package's tests build and run Agents in-process with `testkit` fakes, with no network and no store beyond memory.

### The service sits on top

`service`, `service/httpapi` and the gRPC adapter compose the core packages the way an application would.

- **Cost:** the service layer repeats some wiring that a framework would hide.
- **Check:** the layering test above.

### The CLI is a thin client

Every CLI behavior is reachable through the packages. `--json` and `--jsonl` output and the exit codes are part of the contract.

- **Cost:** a CLI feature first needs a package API.
- **Check:** `cmd/gotato` holds presentation only, and `cmd/gotato/cli_test.go` asserts on JSON.

### Contracts carry a version

The HTTP and gRPC contracts each report a contract version, currently `"3"`. It counts breaking changes to what the service returns, JSON payloads included; the protobuf package stays `gotato.v2` until its schema itself breaks.

- **Cost:** a breaking change to a contract needs a version bump and a [CHANGELOG.md](CHANGELOG.md) entry.
- **Check:** `GET /healthz` and the gRPC contract call return the version.

## Events and telemetry

### Events have a stable shape, and bind to no telemetry framework

An observer in the application, or in an extension package, maps Events onto OpenTelemetry or whatever stack the team already runs.

- **Cost:** there is no tracing out of the box.
- **Check:** the root module imports no telemetry package.

### Events and `Usage` carry enough to compute every number about a Run

Cache hits, token counts, tool results with their cause, stop reasons and timings all travel with Events or `Usage`, down to the store's time to load and save a Session. So a team gets the numbers about a Run in [GOALS.md](GOALS.md) from its own telemetry, not from Gotato's internal state. Memory, startup time and footprint describe the process, not a Run, and come from benchmarks.

- **Cost:** Events carry fields that some teams never read.
- **Check:** the reference workload computes its numbers about a Run from recorded Events alone.

## Failure

### A tool fails, or the model gets a tool call wrong

The call settles as a failed tool result that tells the model why, and the Run goes on. The Turn and tool-call limits bound a model that keeps getting it wrong.

### The model fails

The Run settles as failed, with the error. The Transcript keeps every Turn committed before the failure, and the caller can continue the Session.

### A limit is reached

The Run settles with a status that names the limit.

### A Session is busy, or the Runner is full

- A busy Session: the Runner either rejects the new Run as busy or makes it wait, as the Runner's admission policy says.
- A full Runner: it rejects the Run with a capacity error.

### A save fails

The Run's error says the result was not persisted. The caller retries or reports it.

### An instance stops gently

`Drain` stops new Runs, waits for the Runs in flight, and cancels them when its deadline passes.

### An instance dies mid-Run

At most the Turn in flight is lost.

### A request reaches an instance that is not running the Session

What happens to a message, a cancel or a stream request there is still open (see [Open questions](#open-questions)).

## Testing

### Tests never call a network model

Fake and replay models, a fake tool and an event recorder cover the Loop.

- **Cost:** provider behavior is checked against recorded replies, not live ones.
- **Check:** CI runs with no provider credentials.

## Compatibility

### The contract is named

The contract is:

- the exported identifiers of the root package and the standard runtime packages;
- the CLI's commands, flags, JSON fields, JSONL event shapes and exit codes;
- the Event kinds and their documented payload keys;
- the HTTP and gRPC contracts, which carry their own version.

Every change to these is reviewed as a contract change. Additions are free, and consumers tolerate unknown Event kinds. A rename or a removal goes through a release with a `// Deprecated:` marker when that is cheap.

- **Check:** today a reviewer reads each change. When mass testing begins, CI also compares the exported identifiers of the root package and the standard runtime packages with the last release, and flags a removed or changed one that no changelog entry names (ROADMAP Phase 8).

### Breaks are rare, versioned and written down

A confusing abstraction can still be fixed by a planned break.

- **Cost:** some fixes wait for that break.
- **Check:** every break has a before-and-after entry in [CHANGELOG.md](CHANGELOG.md) (GOALS.md G-G06).

## When principles collide

When two principles pull apart, this order decides:

1. **Never lose a committed Turn.** A lost conversation breaks trust in the service.
2. **Stay minimal.** Weight is paid by every service that embeds Gotato, for as long as it does.
3. **Stay cheap.** Cost can be tuned later.

The Effect cap is not part of the trade: an Agent never runs a tool above its cap to save a Turn or a write.

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

### Adapters prove the store contract with a conformance suite — 2026-10-16

The store is the truth, and each team backs it with its own adapter, so a team needs a way to know its adapter keeps what Gotato relies on.

| Option | For | Against |
|---|---|---|
| **A conformance suite in `testkit`, shipped with the store contract** (chosen) | An adapter runs one set of checks and learns where it falls short. The memory and file stores run the same suite. | The suite is part of the contract, so it changes with it. |
| The contract in prose only | Nothing to maintain. | Each adapter reads the prose its own way, and a wrong one shows up as a lost Turn. |
| Ship a database adapter in the core | Works on day one for one database. | Rejected with "Gotato owns the shapes, and adapters own storage and telemetry": drivers, migrations and one schema for everyone. |

The suite is written against the store contract that Phase 4 ships, with the check for a stale save and what ownership needs, so it is built once.

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

The Events that report a message's way into a Session are open with it, because their names and fields follow from that choice. Today only the moment the Loop commits a message has one. This table shows each step of the way in:

| Step | What happens | Event |
|---|---|---|
| A `Steer` or `FollowUp` reaches the Loop | it waits in a bounded buffer | none |
| The buffer is full | the caller gets `limit_exceeded` | none |
| The Loop commits it | a Steer between Turns, a FollowUp where the Run would settle | `message_start` and `message_end`, with `source` |
| The Run ends before the Loop reads it | it is dropped | none |
| A new Run reaches a busy Session | it is rejected as busy, or waits for the lock | none |
| The Runner is full | the Run is rejected | none |
| A caller asks to cancel | the Run settles as cancelled | only `agent_end`, so the time from the request to the cancel is unknown |

Some of these steps happen before any Run starts, so their Events cannot belong to a Run. They may need a stream of the Runner's own, which ties this question to [Ownership across instances](#ownership-across-instances).

### How a Turn is saved

A save is either an append of the new Turn or a rewrite of the whole Session. Today each save also carries up to 1,000 Events inside the Session, so every save grows with the Session's history.

### A model catalog

A catalog next to the providers would list model names, context sizes and prices.

### Compaction

Compaction exists in the code, and it is not part of an Agent's definition until a later phase.

One part is decided: a warm Session is compacted before it leaves memory at its TTL, on by default, and a setting turns it off ([The store is the truth, and memory is a cache](#the-store-is-the-truth-and-memory-is-a-cache)). How a Session is compacted is still open: who writes the summary, what is kept word for word, and how this relates to the ceiling that compacts at the start of a Run. That discussion belongs to a later phase.

### What the stored record is called

Today `gotato.Agent` is the Loop, `service.AgentSpec` is the Agent, and `session.Session` is the stored record. Once the name Session belongs to the live unit, the record needs a name of its own. The Agent and the Loop take their glossary names in Phase 3, and the record takes its own in Phase 6, each in a planned break with a [CHANGELOG.md](CHANGELOG.md) entry ([ROADMAP.md](ROADMAP.md)).

Smaller open decisions about Events and extensions are tracked in [TODO.md](TODO.md) → Decisions.
