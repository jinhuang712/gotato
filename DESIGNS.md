<!-- what-we-build: drafted 2026-10-16 -->
# Gotato — Designs

Every capitalized term on this page is defined in [GLOSSARY.md](GLOSSARY.md), which also gives each term's name in today's code.

Gotato is built around seven points. Every principle on this page follows from one of them:

1. **The model decides the steps.** One thin Loop calls the model and the tools it asks for, and draws no flow for it.
2. **A conversation is an append-only log in your store.** The model sees a view of it, the provider caches its prefix, an instance caches it, a client tails it, and a fork branches it.
3. **Any instance answers as if it were the only one.** One writer at a time, wherever your load balancer routes, and no Session bound to any instance.
4. **An Agent does only what its purpose-built tools allow.** Up to its Effect cap, with no approvals, no shell and no sandbox.
5. **Every request is laid out for the cache, and an idle Session holds no memory.**
6. **Gotato lives inside your service.** It uses your store, your load balancer and your telemetry, and it keeps a named, versioned contract.
7. **Every step can be seen.** Each one is an Event that names its Session, and anyone can inspect what the model sees.

Each point has its section below, in this order. Under each are its principles, what each one costs, and how a reviewer can check that it still holds. After them come the failures in one table, the rule for when principles collide, the decisions and the open questions.

The page describes the design we intend, not the state of the code: what is built is tracked in [FEATURES.md](FEATURES.md), open defects in [TODO.md](TODO.md), and the checks in [GOALS.md](GOALS.md). Beliefs live in [PHILOSOPHY.md](PHILOSOPHY.md).

This drawing shows the whole shape, from a caller's request to the store:

```text
callers ──► your load balancer ──► any instance of your service
            (routes by session-id)        │
                                          │  Agent (a definition) ──► live Session
                                          │                           the Loop, plus a cache of its Record
                                          │
                                          │  appends each Turn, fenced by an epoch
                                          ▼
                                   your store: the Record, an append-only log
```

## 1. The model decides the steps

Gotato runs one thin Loop that calls the model and the tools the model asks for, and it does not draw the flow for it ([PHILOSOPHY.md → 1](PHILOSOPHY.md#1-what-actually-changed)).

### One loop, no graph

The Loop calls the model, runs the tools it asked for, and repeats until the model answers or a limit stops it:

```text
call the model ──► run the tools it asked for ──► repeat until it answers or a limit stops it
```

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

### Messages reach a running Session in its own process

A message for a Session that is running reaches its writer, because the load balancer routes every request for one Session to the same instance. Each kind takes effect at its own point:

| Kind | When it takes effect |
|---|---|
| Steer | at the next Turn boundary |
| FollowUp | where the Run would otherwise settle |
| Interrupt | at once: a model call in flight is cancelled, a tool in flight finishes, and the message starts the next Turn of the same Run |
| Stop | at once: everything in flight is cancelled through its `ctx`, and the Run settles as cancelled |

- A message that arrives after the Run has ended starts a new Run instead of being dropped.
- An Interrupt lets a running tool finish, because a write cut in half can leave half a side effect. Stop cancels tools too, and each tool decides how to stop safely.
- **Cost:** a message the writer accepted but has not committed is lost if the writer dies. The caller sees its stream end and sends it again, as it would with a single instance.
- **Check:** a test steers, interrupts and stops a Run on a fake model and checks where each message lands in the Transcript.

## 2. A conversation is an append-only log in your store

A Session's Record, kept in your store, is an append-only log. The model sees a view of it, the provider caches its prefix, an instance caches it, a client tails it, and a fork branches it. This drawing shows each of those roles on one Record:

```text
                      provider's cached prefix
                   ┌────────────────────────────┐
                      model's view this Turn
                   ┌─────────────────────────────────┐
 [1][2][3][4] [CP] [6][7][8][9][10][11][12]  [13 being written] ◄── the writer (epoch 8)
  kept, not loaded            │                          ▲
                              └── fork: [9'][10'] …      └── a client tails from 12
```

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

### The Record is an append-only log

Every Turn a Session commits is appended to its Record, and nothing already written is rewritten. Each entry has a sequence number, and the store assigns it in the same write.

- A metadata change, such as a new instruction or a switched-off tool, is an appended entry too.
- Two writers cannot both append the same sequence number, so an old copy can never overwrite a newer one.
- A save costs the size of the new Turn, not the size of the Session.
- **Cost:** the Record grows with every Turn. A team that wants to bound its storage deletes entries before a checkpoint itself.
- **Check:** the store conformance suite appends from two copies of one Record and expects exactly one to succeed, and a crash at every step of the reference workload loses at most the Turn in flight ([GOALS.md](GOALS.md) G-G01).

### Compaction appends a checkpoint

Compaction does not replace part of the history. It appends a checkpoint that holds the summary and the point from which the original entries are kept word for word, and a load starts from the last checkpoint.

- Loading costs the size of the Session since its last checkpoint, not its whole history.
- The entries before a checkpoint stay, so nothing a Session said is lost to compaction.
- **Cost:** a checkpoint must say exactly which entries it covers, so a load never mixes a summary with the entries it summarized.
- **Check:** a test compacts, appends, reloads and compares the Context with the one before the reload.

### A fork is a branch

A fork writes a new Record in the store, starting from its parent's entries, and activates nothing. The new Session is activated by whichever instance its first request reaches.

- The fork keeps its parent's prompt cache key, so its first Turn reads the parent's cached prefix on any instance.
- **Cost:** today a fork copies its parent's entries. Referring to them instead is a later optimization, and it would pin the parent's entries against deletion.
- **Check:** a fork is reachable through any instance, and its first request activates it there.

### The Transcript and the Context are separate values

The Loop commits to the Transcript, and the model receives what a `ContextBuilder` builds from it:

```text
Transcript (everything committed) ──► ContextBuilder ──► Context (what the model sees this Turn)
```

- **Cost:** the Context is built again on every Turn.
- **Check:** no code path sends the Transcript to a model without going through a `ContextBuilder`.

### Gotato owns the shape, and adapters own the schema

A Record has a stable, versioned shape (`schema_version`). An adapter in the application, or in an extension package, maps that shape onto MySQL, Postgres, Mongo or any other database, and owns its tables, collections and migrations. Every operation the store contract needs, the claim, the fenced append and the liveness row, is an ordinary conditional write.

- **Cost:** teams write an adapter, because Gotato ships only a memory store and a file store.
- **Check:** the root module has no database driver, and loading a Record with a newer `schema_version` fails with `ErrNotSupported`.

## 3. Any instance answers as if it were the only one

A Session runs on any instance, with one writer at a time. Your load balancer decides which instance receives a request, and Gotato makes that instance the writer when it can. To a caller, every instance answers as if it were the only one.

This drawing shows who does what:

```text
your load balancer: hash on session-id · retry a "not run" 503 once · drop an instance that is not ready
Gotato:             claim the writer · fence every append · take over an idle Session · drain in order
your store:         the Records · one liveness row per instance · the claims
```

### The store is the truth, and memory is a cache

A live Session may stay warm in memory while it is in use, but correctness never depends on it. An instance may drop a live Session at any time, at its TTL, on drain or in a crash, and any instance can rebuild it from its Record.

- **Cost:** a write for every committed Turn, and a load from the last checkpoint before a cold Session answers.
- **Check:** killing an instance loses no committed Turn ([GOALS.md](GOALS.md) G-G01).

### One writer at a time

Only one instance, the writer, may append to a Record. It holds a claim: the Record names its instance and an epoch, a number that grows with every new claim. Every append checks the epoch inside the same transaction, so a writer whose claim was taken can no longer append.

- A claim is a conditional write that raises the epoch by one, and it succeeds only when no live instance holds the Record.
- When an append fails on the epoch, the writer drops its copy at once.
- When an append's outcome is unknown, for example after a timeout, the writer claims again before it reads, which fences its own write if it landed.
- A second instance learns that the Session has a writer before it calls the model, so it never pays for a Turn it cannot commit.
- **Cost:** every append reads the claim, and the store must offer a conditional write.
- **Check:** two instances that both believe they own a Session cannot both append, and the multi-instance harness finds no Turn appended twice or lost ([GOALS.md](GOALS.md) G-G08).

### Liveness is one row per instance

Each instance writes one liveness row and renews it on a heartbeat. A claim points at that row, and it is valid while the row is alive.

- The row's expiry is set and compared with the store's own clock, never an instance's.
- An instance gets a new identity each time it starts, and a row that has expired never comes back to life.
- When one instance dies, raising the epoch of its row ends every claim it held, in one write.
- An instance that cannot renew its row stops appending and drains itself.
- The row also carries the instance's count of live Sessions, as a load signal.
- **Cost:** a write per instance per heartbeat, and a Session waits for one row to expire before another instance takes it over after a crash.
- **Check:** a test stops an instance's heartbeat and expects its claims to end, and its late appends to fail.

### No Session is bound to an instance

When a request reaches an instance that is not the writer, the instance looks at the claim:

| The Session is | The instance |
|---|---|
| idle, or its writer is gone or draining | claims it, loads it from the last checkpoint and serves the request |
| running a Run on another live instance | waits for that Run to end, up to a bound, then claims it; past the bound it answers 409 with `Retry-After` |

- This covers every case where a Session's writer is not where the load balancer points: a fork, a retry after a refusal, a Session activated before the load balancer's hash changed.
- **Cost:** a takeover drops the old writer's warm copy, and the new writer loads from the store.
- **Check:** the harness sends each request to a random instance and expects the same Transcripts and Event order as a single instance gives.

### Any instance answers like the only one

A caller cannot tell which instance served it, apart from a few places that are written down here:

| Place | What differs from a single instance |
|---|---|
| The writer dies mid-Run | the Turn in flight is lost; the stream ends and resumes on another instance |
| The load balancer's hash changes | for a few seconds, a message may wait for a running Run, and a Steer then starts a new Run |
| Several instances | `MaxActiveRuns` counts per instance, not across the service |
| A rolling deploy | an instance on an older contract version refuses what it does not know, until it is replaced |

- **Cost:** these places are part of the contract, and a change to them is a contract change.
- **Check:** the harness above.

### The load balancer routes; Gotato does not

Gotato never forwards a request between instances, because discovery and routing already exist where your services run ([PROPOSAL.md → What we don't want](PROPOSAL.md#what-we-dont-want)). It needs three things from your load balancer:

| Your load balancer | Why |
|---|---|
| a consistent hash on the `session-id` header | every request for one Session reaches one instance |
| one retry on another instance after a 503 marked "not run" | a refusal never ran, so a retry cannot run it twice |
| readiness from `/readyz` | an instance that is draining receives no new requests |

- A request that creates a Session, such as `POST /v1/runs`, needs no header; its response carries the new ID.
- **Cost:** a load balancer without header hashing still works, but more requests take over Sessions from other instances.
- **Check:** the deployment guide in [FEATURES.md](FEATURES.md) shows these three settings for common load balancers, and the harness runs behind a hashing proxy.

### Drain finishes, saves and releases before it disconnects

On an endpoint or on SIGTERM, an instance drains in this order:

```text
mark draining in its liveness row ──► /readyz answers 503 ──► refuse new activations
  ──► close(drain): every live Session finishes its Run, saves, and releases its claim
  ──► close each stream with a reconnect code ──► leave
```

- The signal reaches every live Session through one closed channel, and each Session handles it in its own turn, after the messages before it.
- A claim is released only if it still names this instance.
- What a drain cannot finish by its deadline is fenced by the epoch anyway.
- **Cost:** a deploy waits for running Runs, up to the drain deadline.
- **Check:** a test drains an instance with a Run in flight and expects the Run committed, the claim released and the stream ended with a reconnect code.

### A stream resumes from a position

Every Event a client streams carries its sequence number, and the Record carries an epoch of its own that changes if its log is ever reset. A client that loses its stream reconnects through the load balancer, to any instance, and passes the last position it saw.

- The instance reads the entries after that position, merges them with the live stream without gaps or duplicates, and goes on.
- If the epoch differs, the client is told the position cannot be resumed, and reloads the Session.
- A stream that ends says whether to reconnect, and how long to wait first, so a lost instance does not cause every client to reconnect at the same instant.
- **Cost:** every streamed Event carries a position, and resuming needs a short buffer while history is read.
- **Check:** a test cuts a stream at every Event of the reference workload and expects the resumed stream to equal an uncut one.

## 4. An Agent does only what its purpose-built tools allow

An Agent can do what its purpose-built tools allow, up to its Effect cap, and nothing else ([PHILOSOPHY.md → 3](PHILOSOPHY.md#3-how-is-a-cloud-session-different-from-a-local-one)).

### Every tool declares its Effect

The levels, from the least impact to the most:

```text
read  <  write local  <  write external  <  destructive
```

- A tool that declares nothing ranks as destructive.
- **Cost:** tool authors classify each tool.
- **Check:** `ToolEffect` in `tool.go` ranks an unspecified Effect with destructive.

### An Agent caps the Effect it allows

An Agent names its tools and can name the highest Effect any of them may have. A tool above the cap is never offered to the model, so a read-only Agent cannot change anything.

- **The cap hides tools.** The model sees only the tools at or below the cap, including the tools of a ToolSet it activates later.
- **A hidden tool is an unknown tool.** A model that calls one by a guessed name gets the same failed result as for any name that does not exist.
- **The cap belongs to the Agent.** A Session cannot raise or lower it, and work that needs another cap is another Agent.
- **No cap by default.** An Agent that names none offers all its tools.
- **Cost:** a write tool needs an Agent that is allowed to write, and two jobs with different bounds are two Agents.
- **Check:** a test gives tools of every Effect to Agents with every cap, and asserts that no tool above the cap reaches a model request, the tool listing or its own code ([GOALS.md](GOALS.md) G-G05).

### Tools check their own arguments

A write that is safe for some arguments and not others is refused by the tool itself (see [No approval system](#no-approval-system--2026-10-15)).

- **Cost:** tools are written with care, because the tools are the bounds.

### No shell and no sandbox in Gotato

Tools are Go functions in the service, or tools reached through MCP servers.

- **Why:** [PHILOSOPHY.md → 2](PHILOSOPHY.md#2-what-belongs-in-a-cloud-session).
- **Cost:** work that needs a machine is done elsewhere.
- **Check:** the module ships no tool that runs commands or code.

## 5. Every request is laid out for the cache, and an idle Session holds no memory

Every request is laid out so the provider can serve most of it from its prompt cache, and a Session that is not in use holds no memory ([PHILOSOPHY.md → 4](PHILOSOPHY.md#4-where-is-agent-as-a-service-going)).

### Stable content first, changing content last

Each Context is laid out in this order, from the most stable part to the part that grows:

```text
instruction · Static blocks  →  tools, sorted by ID  →  Transcript
```

- Because the Record only grows at its end, the prefix of one Turn's request is the prefix of the next.
- Nothing that changes every Turn is added. A fact the model needs now, such as the current time, comes from a tool it calls.
- **Cost:** the model learns a changing fact only by calling a tool, and any change to the order breaks the provider's cache.
- **Check:** `TestPrefixHashStableAcrossAppendOnlyTurns` keeps the prefix hash unchanged across appending Turns.

### Cache markers are neutral in the core

The core marks where a provider may cache, and each wire API maps the marks to its own form, such as Anthropic's `cache_control`. A cache key, one per Session, tells a provider which requests belong to one cache, on whichever instance they run.

- **Cost:** a provider feature with no neutral form waits for one.
- **Check:** the root package names no provider.

### An idle Session holds no memory

A live Session leaves memory when it has been idle for its TTL, 60 minutes unless its Agent sets another. Its Record stays in the store.

- Before it leaves, it is compacted, unless a setting turns this off. By then the provider's prompt cache has expired, so the next Run starts cold either way, and a shorter history costs less to send again.
- **Cost:** compacting before the TTL loses detail, and it costs a summary for every Session that leaves, including one that never comes back.
- **Check:** G-B08 in [GOALS.md](GOALS.md) measures the heap a warm Session holds.

### A refusal is a safety valve, not a balancer

Where new Sessions go is the load balancer's choice. An instance refuses to activate a new Session only past a hard limit, such as its memory budget, with a 503 marked "not run".

- Spreading load is the job of the load balancer, for example a hash with bounded load, and of the autoscaler.
- While an instance keeps refusing, the Sessions routed to it run elsewhere, and each of their requests takes one extra retry.
- **Cost:** an instance near its limit serves its Sessions with an extra hop until load moves.
- **Check:** a test fills an instance to its limit and expects a "not run" refusal, an Event, and no Session activated there.

## 6. Gotato lives inside your service

Gotato asks nothing new of your service. It is a Go library inside it. It uses your store, your load balancer and your telemetry through small adapters, and it promises a named, versioned contract in return.

### Library first

A small `main` builds a Session with `gotato.NewSession` and runs it with a fake model in-process, with no server, daemon or database.

- **Cost:** the service layer cannot add anything the library needs to work.
- **Check:** the root package's tests build and run Sessions in-process with `testkit` fakes, with no network and no store beyond memory.

### The service sits on top

`service`, `service/httpapi` and the gRPC adapter compose the core packages the way an application would. Claims, liveness and drain live in the service layer and the store contract, not in the core.

- **Cost:** the service layer repeats some wiring that a framework would hide.
- **Check:** the layering test below.

### The CLI is a thin client

Every CLI behavior is reachable through the packages. `--json` and `--jsonl` output and the exit codes are part of the contract.

- **Cost:** a CLI feature first needs a package API.
- **Check:** `cmd/gotato` holds presentation only, and `cmd/gotato/cli_test.go` asserts on JSON.

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

### Core types belong to no vendor

The model contract uses Gotato's own request and event types. Provider data that the core does not understand, such as reasoning signatures, travels through it untouched.

- **Cost:** every wire API maps to and from these types.
- **Check:** the root package imports only the standard library.

### Providers are configured by wire API

A provider is configuration, and Gotato implements each wire API once. See [the decision](#providers-are-configured-by-wire-api--2026-09-17).

### The contract is named

The contract is:

- the exported identifiers of the root package and the standard runtime packages;
- the CLI's commands, flags, JSON fields, JSONL event shapes and exit codes;
- the Event kinds and their documented payload keys;
- the HTTP and gRPC contracts, which carry their own version;
- the store contract that adapters implement, and the places where many instances differ from one.

Every change to these is reviewed as a contract change. Additions are free, and consumers tolerate unknown Event kinds.

- **Check:** today a reviewer reads each change. When mass testing begins, CI also compares the exported identifiers with the last release (ROADMAP Phase 9).

### Contracts carry a version

The HTTP and gRPC contracts each report a contract version, currently `"4"`. It counts breaking changes to what the service returns, JSON payloads included.

- **Cost:** a breaking change to a contract needs a version bump and a [CHANGELOG.md](CHANGELOG.md) entry.
- **Check:** `GET /healthz` and the gRPC contract call return the version.

### Breaks are rare, versioned and written down

A confusing abstraction can still be fixed by a planned break.

- **Cost:** some fixes wait for that break.
- **Check:** every break has a before-and-after entry in [CHANGELOG.md](CHANGELOG.md) (GOALS.md G-G06).

## 7. Every step can be seen

Every step is an Event, every Event names its Session, and anyone can see what the model sees without calling it.

### Every Event carries its Session

Each Event carries its Session's ID, and an Event of a Run also carries its Run, its Turn, a sequence number and a time.

- **Cost:** every Event carries a field a single-Session program does not need.
- **Check:** a test records every Event of the reference workload and finds none without a Session.

### Events have a stable shape, and bind to no telemetry framework

An observer in the application, or in an extension package, maps Events onto OpenTelemetry or whatever stack the team already runs.

- **Cost:** there is no tracing out of the box.
- **Check:** the root module imports no telemetry package.

### Events and `Usage` carry enough to compute every number about a Run

Cache hits, token counts, tool results with their cause, stop reasons and timings all travel with Events or `Usage`, down to the store's time to load and save a Session. Memory, startup time and footprint describe the process, not a Run, and come from benchmarks.

- **Cost:** Events carry fields that some teams never read.
- **Check:** the reference workload computes its numbers about a Run from recorded Events alone.

### The Context is inspectable

Anyone can ask what the model would see for a Session now, without calling the model, and get the prefix hash with it. The view is built with the code a Turn runs, so the two cannot drift apart.

- **Cost:** a `ContextBuilder`, the context extensions and every tool source must build without side effects, because inspection runs them too.
- **Check:** the reference workload compares every Turn's prefix hash with an inspection of that Turn's Transcript ([GOALS.md](GOALS.md) G-G03).

### Tests never call a network model

Fake and replay models, a fake tool and an event recorder cover the Loop.

- **Cost:** provider behavior is checked against recorded replies, not live ones.
- **Check:** CI runs with no provider credentials.

## Failure

Each point above says what happens when its part fails. This table collects them in one place.

| Case | What happens | Point |
|---|---|---|
| A tool fails, or the model gets a tool call wrong | a failed tool result tells the model why, and the Run goes on; the Turn and tool-call limits bound a model that keeps getting it wrong | 1 |
| The model fails | the Run fails, and the Record keeps every Turn committed before it | 1 |
| A limit is reached | the Run settles with a status that names the limit | 1 |
| A Session is busy on its writer, or the instance is at `MaxActiveRuns` | the new Run waits or is rejected, as the admission policy says | 1 |
| An append fails | the Run's error says the result was not persisted, and the caller retries or reports it | 2 |
| An instance stops gently | it drains: every Run finishes, saves and releases before the instance disconnects | 3 |
| An instance dies mid-Run | at most the Turn in flight is lost; another instance takes the Session over once the dead instance's row expires | 3 |
| The writer loses its liveness | it stops appending and drains itself | 3 |
| An append's outcome is unknown | the writer claims again, then reloads | 3 |
| A request reaches an instance that is not the writer | an idle Session is taken over; a busy one is waited for, then refused with 409 | 3 |
| An instance is full | it refuses to activate a new Session, with a 503 marked "not run" | 5 |

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

### The live Session is `gotato.Session`, and its record is `session.Record` — 2026-10-16

The object a caller builds and talks to already has what the glossary gives a Session: an ID, a Status and its state machine, a queue for Steer and FollowUp, a Transcript, a `ContextBuilder` and its tools. It lacks only saving itself and a TTL, which Phase 6 adds. So it takes the name Session, and the stored record takes the name Record.

| Option | For | Against |
|---|---|---|
| **`gotato.Session` for the live unit, `session.Record` for what the store keeps** (chosen) | The type is named after the thing a caller holds. Phase 6 grows the same type, so the name does not change again. | The record's name is settled before Phase 6, so this rename is larger. |
| `gotato.Loop` | Matches the old plan for this rename. | The Loop is a concept, the cycle inside a Session, not a thing a caller holds. |
| A role name, such as `gotato.Executor` | The smallest change. The record keeps its name. | In Phase 6, the Session either wraps the Executor or the type is renamed again. |

Three choices come with it:

- **One concrete type, no interfaces.** `gotato.NewSession` returns a `*Session`. There is one implementation, and no caller implements the interfaces the old type was split into, so they go. A caller that wants a fake defines its own small interface, as Go code usually does.
- **One ID.** A live Session's ID is its Session ID: it comes from its record, or is generated when there is none. Every Event carries it as `session_id`.
- **Today a live Session lives for one Run.** The Runner builds one from the record, runs it and closes it, so it behaves like a Session whose TTL is zero, and the store stays the truth. Phase 6 keeps it warm.

### The Record is an append-only log — 2026-10-16

A save had to cost the size of a Turn, and a stale copy had to fail instead of overwriting a newer one.

| Option | For | Against |
|---|---|---|
| **Append only, with checkpoints for compaction** (chosen) | A save costs one Turn. Two writers cannot append the same sequence number. Every adapter maps it to an insert. Nothing is lost to compaction. | The Record grows; storage is bounded by the team. |
| Rewrite the whole Record, with a version check | The simplest contract. | A save costs the whole Session, megabytes for long ones. |
| Append, plus a rewrite for compaction | Compaction stays a replacement. | Two write paths, and the rewrite brings the cost back. |

### One writer per Session, claimed in the store — 2026-10-16

A second instance must stop before it calls the model, and a writer that lost its claim must not append. We studied how seven Go systems do this. The closest are CockroachDB's per-node liveness record, whose epoch ends all of a node's leases at once, and Temporal's range ID, which every write checks.

| Option | For | Against |
|---|---|---|
| **A claim with an epoch, valid while its instance's liveness row is** (chosen) | Needs only conditional writes in your own store. Fences every append. One write ends a dead instance's claims. | A heartbeat write per instance. A takeover after a crash waits for one row to expire. |
| A lease per Run | No liveness row. | Ownership flips between Runs, so a warm Session and a stream have no stable home. |
| A membership protocol, such as gossip | Fast failure detection. | A second system inside Gotato, and no fence on the store without an epoch anyway. |
| A placement service | Central control of placement. | A separate platform to run, which [PROPOSAL.md](PROPOSAL.md) rules out. |

### The load balancer routes, and Gotato follows — 2026-10-16

Routing already exists where a team runs its services, so Gotato makes the instance a request reaches the writer whenever it can.

| Option | For | Against |
|---|---|---|
| **Follow the load balancer: take over an idle Session, wait for a busy one** (chosen) | No routing in Gotato. Works with a plain consistent hash. No Session is ever unreachable. | While the hash changes, a message may wait for a running Run. |
| Forward requests to the writer between instances | Every message reaches the writer at once. | Routing, an internal API, and its authentication inside Gotato. |
| Redirect the caller to the writer | No forwarding. | A load balancer cannot follow a redirect to one instance. |

### Messages that arrive during a Run — 2026-10-16

A Session's messages reach its writer through the load balancer's hash, so each kind can take effect in the writer's own process.

| Option | For | Against |
|---|---|---|
| **Steer at a Turn boundary; Interrupt and Stop at once; a late message starts a new Run** (chosen) | The same behavior as one instance. Nothing waits in the store. | A message accepted but not yet committed is lost if the writer dies; the caller sends it again. |
| Queue every message in the store first | Survives the writer's death. | A write per message, and a writer that polls the store. |
| Reject a message while a Run is going | The simplest. | A caller cannot steer or stop a Run. |

### No approval system — 2026-10-15

Gotato is for purpose-built agents, whose tools the team chooses and writes (reason: [PHILOSOPHY.md → 3](PHILOSOPHY.md#3-how-is-a-cloud-session-different-from-a-local-one)).

| Option | For | Against |
|---|---|---|
| **The Effect cap, plus tools that check their own arguments** (chosen) | No waiting state in the Loop. The bounds are visible in the Agent and the tools. | Tools must be written with care. |
| Built-in approval that pauses a Run for a person | Familiar from general assistants. | Treats a broken stack as normal. A paused Run must survive restarts, which adds a waiting state to every Session. |
| Leave it to extensions only | No new concept. | No bounds by default. |

A team that still wants an extra check can add one with the `PreToolUse` extension. A step that needs a person by design is a tool that hands it over, for example by opening a ticket.

### The Effect cap hides tools, and only an Agent sets it — 2026-10-16

The cap decides which tools a model may use, so two questions came first: what happens to a tool above the cap, and who may set the cap.

| Option | For | Against |
|---|---|---|
| **Hide the tools above the cap** (chosen) | The model cannot plan around a tool it never sees, and spends no Turn on a call that fails. Fewer tool definitions in the Context. | A model that guesses a hidden name gets "unknown tool", not the reason. |
| Offer every tool, and refuse a call above the cap | The model learns why a call failed. | The model keeps choosing tools it may not use, and each refusal costs a Turn. |

| Option | For | Against |
|---|---|---|
| **Only the Agent sets the cap** (chosen) | The bounds are in one place, the Agent's definition, and a reviewer reads them there. | Two jobs with different bounds need two Agents. |
| A Session can lower the cap too | One Agent for several jobs. | We found no case where the same purpose-built Agent needs different bounds per conversation. A different bound means a different job, so it is a different Agent. |

There is no cap by default: an Agent that names none behaves as before.

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

### The Events on a message's way into a Session

The steps are now known: a message reaches the writer, waits for a Turn boundary, is committed, or starts a new Run. A request may also wait for another instance's Run, or be refused as "not run". Their Event names and fields are open.

### Runtime notices

How Gotato tells the model something that is not the user's words, such as a limit that is near ([FEATURES.md](FEATURES.md) G-F29).

### A model catalog

A catalog next to the providers would list model names, context sizes and prices.

### Compaction

One part is decided: a warm Session is compacted before it leaves memory at its TTL, on by default, and a setting turns it off. Another part follows from the log: compaction appends a checkpoint. How a Session is compacted is still open: who writes the summary, what is kept word for word, and how this relates to the ceiling that compacts at the start of a Run.

Smaller open decisions about Events and extensions are tracked in [TODO.md](TODO.md) → Decisions.
