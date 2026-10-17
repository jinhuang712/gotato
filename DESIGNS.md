<!-- what-we-build: drafted 2026-10-17 -->
# Gotato — Designs

Every capitalized term on this page is defined in [GLOSSARY.md](GLOSSARY.md), which also gives each term's name in today's code.

Gotato is designed around seven principles:

1. **Keep subtracting.** Gotato does the least it takes to run Sessions in the cloud, and anything new has to show that Gotato cannot do without it.
2. **Stay inside the boundary.** Routing, authentication, databases, telemetry and user interfaces are needed, but the service around Gotato already has them, so Gotato leaves seams for them instead of building them.
3. **Serve purpose-built agents.** Gotato is built for agents whose tools the team writes and whose jobs it can predict, not for general assistants or for coding in the cloud.
4. **Many instances from day one.** Gotato is designed for many instances, one instance is only the simplest case, and no instance is special.
5. **A Session can be dropped and rebuilt at any time.** A live Session is a goroutine running its Record, so dropping it loses nothing and rebuilding it is cheap.
6. **The model's input is built, and laid out for the cache.** What the model sees is built on every Turn, and its prefix stays the same from one Turn to the next.
7. **A Session can be stopped and seen.** A Session runs with nobody watching, so it can be stopped while it runs, and what it did can be seen afterward.

A principle is a question to ask of every new requirement. Each section below names that question, then lists the decisions that follow from the principle: what was chosen, what was not, and why. The decisions that weighed several options keep their table and their date. After the principles comes the order that decides when two of them pull apart.

This page describes the design we intend, not the state of the code. What is built, and how each feature works, is in [FEATURES.md](FEATURES.md). Open design questions are in [ROADMAP.md](ROADMAP.md), and beliefs are in [PHILOSOPHY.md](PHILOSOPHY.md).

## 1. Keep subtracting

Gotato does the least it takes to run Sessions in the cloud. Every addition is weight that each service embedding Gotato carries for as long as it does, so the default answer to a new feature is no.

**Ask:** can a Go service that embeds an agent work without it? If it can, it stays out.

### One Loop, and the model decides the steps

- **Instead of:** graphs, workflows and nodes.
- **Why:** a better model makes the Loop better with no code change, while every node is code that someone keeps ([PHILOSOPHY.md → 1](PHILOSOPHY.md#1-what-actually-changed)). A fixed sequence is written in Go around the Loop.

### Agents are flat

- **Instead of:** sub-agents, supervisors, workers or any other role as a type.
- **Why:** roles and lineage are how an application composes agents, and they live in Session metadata.

### No orchestration between agents

- **Instead of:** multi-agent orchestration, an agent-to-agent protocol such as A2A, or worktrees.
- **Why:** composing agents is ordinary Go code in the application, which already knows its own tasks.

### A Session stays generic

- **Instead of:** typed fields for tasks, projects or workflows.
- **Why:** each application keeps its own structure in metadata, so one Session type serves them all.

### No long-term memory

- **Instead of:** a built-in memory architecture.
- **Why:** what to remember across Sessions differs by application. Gotato provides Sessions, the Context and Compaction, and memory is built on them.

### One implementation means a concrete type

- **Instead of:** an interface drawn before a second implementation exists.
- **Why:** an interface with one implementation is a guess about the future. A caller that wants a fake defines its own small interface, as Go code usually does.

### Extensions wrap the Loop at fixed stages

- **Instead of:** a plugin framework, or a fork of the Loop.
- **Why:** a fixed set of stages keeps the Loop one path that a reader can follow. A need that fits no stage waits for a new stage, added as a reviewed change.

### Library first

- **Instead of:** a required server, daemon or database.
- **Why:** a small `main` with a fake model runs an agent in-process, so the service and the store are choices, not the price of entry.

### Dependencies point inward

- **Instead of:** behavior that only the CLI or only the service has, or a core that knows about the service.
- **Why:** every behavior lives in the packages first, so the library, the CLI and the service behave the same. Claims, liveness and drain live in the service layer and the store contract, not in the core. `layering_test.go` keeps each layer building only on the layers inside it:

```text
cmd/gotato, gRPC adapter  ──►  service, service/httpapi  ──►  session, modelctx, toolregistry, testkit  ──►  core
```

### The core uses only the standard library

- **Instead of:** vendor SDKs or other libraries that save some code, or one module that carries every dependency.
- **Why:** every dependency lands in every service that embeds Gotato. The root package imports only the standard library, the root module requires only `gopkg.in/yaml.v3`, and the gRPC adapter is a module of its own.

### Providers are configured by wire API — 2026-09-17

A provider is configuration: an endpoint, a key, a wire API and its models. Gotato implements each wire API once (`openai-chat-completions`, `openai-responses` and `anthropic-messages`), and any vendor that speaks one of them works without new code.

| Option | For | Against |
|---|---|---|
| **One implementation per wire API** (chosen) | A new vendor is a config entry. No vendor SDK in the service. | A vendor with a new wire API waits for its implementation. |
| One adapter per vendor | Each vendor's full feature set. | Code for every vendor, and the list never ends. |
| Vendor SDKs | Maintained by the vendors. | Their dependencies land in every service. |

## 2. Stay inside the boundary

Gotato is a library inside your service. Routing, authentication, databases, telemetry and user interfaces are all needed, and your service already has them. Gotato does not replace them: it does its own job well, and leaves seams for theirs. Where *Keep subtracting* asks whether something is needed at all, this principle asks whose job it is. Routing is needed; it is just not Gotato's.

**Ask:** is this a job only an agent runtime has, or one the service around it already does?

### Gotato builds no infrastructure, and stays within its boundary — 2026-10-16

Running a Session on many instances raises two kinds of problem: getting a request to the right instance, and keeping the Session correct once it is there. The first is infrastructure that every team already runs. Gotato takes only the second: it keeps a Session correct on whichever instance a request reaches, taking over an idle Session and waiting for a busy one, and messages for a running Session reach its writer through the same routing as any request.

| Option | For | Against |
|---|---|---|
| **Stay within the boundary: correctness is Gotato's, routing is the infrastructure's** (chosen) | Nothing new to deploy or operate. Works with a plain consistent hash. No Session is ever unreachable. | How seamless it feels depends on the team's routing; while a hash changes, a message may wait for a running Run. |
| Forward requests to the writer between instances | Every message reaches the writer at once. | Gotato becomes a router, with an internal API and its authentication. |
| Redirect the caller to the writer | No forwarding. | Pushes routing onto every caller, and a load balancer cannot follow a redirect to one instance. |
| Queue every message in the store for the writer | Survives the writer's death. | Gotato becomes a message broker: a write per message, and a writer that polls the store. |
| A placement or membership service of Gotato's own | Central control of where Sessions run. | A separate system to run, which [PROPOSAL.md](PROPOSAL.md) rules out. |

### No authentication

- **Instead of:** built-in authentication, OAuth or a tenant model.
- **Why:** every service already authenticates its callers in its own way. Gotato passes the caller's identity down to the extensions and the store, and lets them refuse. Who may touch which Session stays the application's rule.

### No global scheduling or quotas

- **Instead of:** scheduling resources or provider quotas across services.
- **Why:** that is system policy, and the application owns it. Gotato offers Runs that are cheap and cancellable, bounds its own concurrent Runs, and keeps one Run per Session.

### Gotato owns the shapes, and adapters own storage and telemetry — 2026-10-15

Databases model schemas differently, and teams run different telemetry stacks, so neither belongs in the core.

| Option | For | Against |
|---|---|---|
| **Stable shapes for a Session and its Events, mapped by adapters** (chosen) | Works with any database and any telemetry stack. Adds no dependency to the service. | Each team writes or picks an adapter. |
| Ship a SQL store in the core | Works on day one for SQL users. | Drivers, migrations and one schema for everyone. No help for Mongo. |
| Ship OpenTelemetry in the core | Tracing on day one. | A heavy dependency for every service, including those on another stack. |

### Core types belong to no vendor

- **Instead of:** one vendor SDK's types as the core's types.
- **Why:** the model contract uses Gotato's own request and Event types, and provider data the core does not understand, such as reasoning signatures, passes through untouched.

### No clients and no user interfaces

- **Instead of:** bundled bots, schedulers, front ends or terminal UIs.
- **Why:** callers write their own. Gotato provides the HTTP handler, the gRPC service and the Go API they call, and its own human-facing surface is CLI output.

### Embedding never forces an upgrade

- **Instead of:** following the newest Go release.
- **Why:** a service should not have to upgrade Go to add a library. The root `go.mod` says `go 1.22`.

### A named, versioned contract

- **Instead of:** changes that slip in unannounced.
- **Why:** a service builds against Gotato, so it needs to know what can change under it. The contract is the exported identifiers of the root and standard runtime packages; the CLI's commands, flags, JSON fields and exit codes; the Event kinds and their payload keys; the HTTP and gRPC contracts, which carry their own version; the store contract; and the places where many instances behave differently from one. Additions are free, and consumers tolerate unknown Event kinds. A break is rare, raises the version, and has a before-and-after entry in [CHANGELOG.md](CHANGELOG.md).

## 3. Serve purpose-built agents

Gotato is built for agents whose tools the team chooses and writes, and whose jobs it can predict ([PHILOSOPHY.md → 3](PHILOSOPHY.md#3-how-is-a-cloud-session-different-from-a-local-one)). A general assistant or a coding agent in the cloud needs a machine and a person standing by, and Gotato provides neither.

**Ask:** does a purpose-built agent need this, or only a general assistant?

### An Agent is a definition, and a Session is the live unit — 2026-10-15

The words now match how the labs' agent APIs use them, so a reader who knows those APIs reads Gotato correctly without a note.

| Option | For | Against |
|---|---|---|
| **Agent = definition, Session = live unit** (chosen) | Matches Claude Managed Agents and the OpenAI Agents API. One Agent running many Sessions reads naturally. | A rename of the code, done in Phase 3. |
| Agent = live unit, with no term "session" | One noun for callers. | The reverse of the labs' usage, so every reader needs a note. |
| Keep the old code names: Agent = the loop for one Run, Session = the stored record | No rename. | The thing a caller talks to has no name of its own. |

### A Session has no machine

- **Instead of:** a shell, a sandbox or a filesystem for each Session.
- **Why:** the work Gotato is for needs tools and data, not a computer ([PHILOSOPHY.md → 2](PHILOSOPHY.md#2-what-belongs-in-a-cloud-session)). Work that needs a machine runs on a laptop, in CI or in a coding product.

### No tool runs commands or code

- **Instead of:** built-in tools that run commands, code or a browser.
- **Why:** Gotato is not a cloud coding agent, and a tool that runs anything is a bound nobody can read.

### Tools are Go functions or MCP tools

- **Instead of:** a tool catalog or marketplace that ships with Gotato.
- **Why:** the team chooses and writes its tools. General-purpose tools come from MCP servers and from the application.

### The bounds are in the definition

- **Instead of:** someone watching each Run, or bounds that change from one conversation to the next.
- **Why:** an Agent's bounds are its named tools and its Effect cap, set in its definition, where a reviewer reads them. A conversation cannot raise them: a Session can switch off one of its Agent's tools, but never add one or lift the cap.

### Every tool declares its Effect, and silence means destructive

- **Instead of:** treating a tool that declares nothing as harmless.
- **Why:** a forgotten declaration should fail closed. The levels, from the least impact to the most, are read, write local, write external and destructive.

### Tools check their own arguments

- **Instead of:** a central policy engine.
- **Why:** a write that is safe for some arguments and not others is best judged by the code that knows them. The tools are the bounds, so they are written with care.

### No approval system — 2026-10-15

Gotato is for purpose-built agents, whose tools the team chooses and writes (reason: [PHILOSOPHY.md → 3](PHILOSOPHY.md#3-how-is-a-cloud-session-different-from-a-local-one)).

| Option | For | Against |
|---|---|---|
| **The Effect cap, plus tools that check their own arguments** (chosen) | No waiting state in the Loop. The bounds are visible in the Agent and the tools. | Tools must be written with care. |
| Built-in approval that pauses a Run for a person | Familiar from general assistants. | Treats a broken stack as normal. A paused Run must survive restarts, which adds a waiting state to every Session. |
| Leave it to extensions only | No new concept. | No bounds by default. |

A team that still wants an extra check can add one with the `PreToolUse` extension. A step that needs a person by design is a tool that hands it over, for example by opening a ticket.

### Different bounds make a different Agent

- **Instead of:** growing one Agent until it is general.
- **Why:** when reach and narrowness pull apart, the narrower answer wins ([PHILOSOPHY.md → When the answers collide](PHILOSOPHY.md#when-the-answers-collide)). Reach comes from more Agents, not bigger ones.

## 4. Many instances from day one

Gotato is designed for many instances behind your load balancer. One instance is the simplest case of that design, not the case it starts from, and a caller cannot tell which instance served it. This principle adds the most machinery, so *Keep subtracting* applies inside it: Gotato picks the least machinery that is still correct on many instances.

**Ask:** is this still right when a request reaches any instance?

### The store is the truth, and memory is a cache — 2026-10-15

A Session behaves like a live unit while it is in use, and like a record while it waits.

| Option | For | Against |
|---|---|---|
| **A live unit kept warm, with the store as the truth** (chosen) | Holds a Session's queue, stream and status while it works. Survives the loss of any instance. The TTL only frees memory. | A write per Turn. Machinery to load and to let go. |
| Nothing in memory: load and save around every Run (today's code) | The fewest moving parts. | The Session has no queue of its own, so a message that arrives mid-Run can only wait or be rejected. |
| A long-lived unit in memory that owns its state | Fast, and natural for queues and streams. | Lost with its instance. Pins each conversation to one instance. |

An earlier version of Gotato kept agents in memory for their whole life, and a later one replaced them with a stored record. This decision brings a long-lived unit back. It is safe this time, because a Session's identity is its record, not the memory that holds it.

### One writer per Session, claimed in the store — 2026-10-16

A second instance must stop before it calls the model, and a writer that lost its claim must not append. We studied how seven Go systems do this. The closest are CockroachDB's per-node liveness record, whose epoch ends all of a node's leases at once, and Temporal's range ID, which every write checks.

| Option | For | Against |
|---|---|---|
| **A claim with an epoch, valid while its instance's liveness row is** (chosen) | Needs only conditional writes in your own store. Fences every append. One write ends a dead instance's claims. | A heartbeat write per instance. A takeover after a crash waits for one row to expire. |
| A lease per Run | No liveness row. | Ownership flips between Runs, so a warm Session and a stream have no stable home. |
| A membership protocol, such as gossip | Fast failure detection. | A second system inside Gotato, and no fence on the store without an epoch anyway. |
| A placement service | Central control of placement. | A separate platform to run, which [PROPOSAL.md](PROPOSAL.md) rules out. |

### Every append checks the epoch in the same write

- **Instead of:** avoiding conflicts with timeouts and timing.
- **Why:** clocks drift and processes pause, so only a check inside the write itself stops a writer whose claim was taken.

### Coordination uses only the store's conditional writes

- **Instead of:** a coordination service such as etcd, ZooKeeper or Redis.
- **Why:** a team already runs a database. A second system to coordinate instances is infrastructure that Gotato does not add.

### Liveness is one row per instance, timed by the store's clock

- **Instead of:** a heartbeat per Session, or a comparison of the instances' own clocks.
- **Why:** one write per instance per heartbeat, however many Sessions it holds, and one write ends every claim of a dead instance. The store's clock is the only one every instance shares.

### A conflict is found before the model is called

- **Instead of:** finding out after a Turn that it cannot be committed.
- **Why:** a Turn costs money and seconds, so a second instance learns that the Session has a writer before it spends either.

### No Session is bound to an instance

- **Instead of:** keeping a Session on the instance that activated it.
- **Why:** instances come and go. An idle Session is taken over wherever its request lands, and a busy one is waited for, up to a bound.

### Many instances behave like one, and the differences are written down

- **Instead of:** promising more than one instance can, such as a message that always arrives.
- **Why:** a single instance's behavior is the spec. The few places where many instances differ, such as losing the Turn in flight when its writer dies, are part of the contract and listed in [FEATURES.md → G-F27](FEATURES.md#g-f27--service-many-instances-).

### A refusal is a safety valve, not a balancer

- **Instead of:** Gotato spreading load itself.
- **Why:** placing Sessions is the job of the load balancer and the autoscaler. An instance refuses a new Session only past a hard limit, such as its memory, and marks the refusal "not run" so that a retry is safe.

### A drain finishes before it disconnects

- **Instead of:** closing connections or cancelling Runs first.
- **Why:** a deploy should not lose work. An instance stops taking new Sessions, lets every Run finish, save and release its claim, and only then closes its streams.

## 5. A Session can be dropped and rebuilt at any time

A live Session is a goroutine running its Record, not a process or a sandbox kept until someone destroys it. Dropping it loses nothing, because its Record is in the store, and any instance can rebuild it cheaply. This is what lets a Session move between instances, and cost almost nothing while it is idle.

**Ask:** once this is in a Session, can the Session still be dropped at any moment and rebuilt quickly on another instance?

### A live Session is a goroutine

- **Instead of:** a process, a subprocess or a sandbox for each Session.
- **Why:** a goroutine costs kilobytes and starts in microseconds, so one instance holds many Sessions, most of them idle.

### A Session's identity is its Record

- **Instead of:** a live object with an identity of its own.
- **Why:** the Session ID is its Record's ID, so whichever instance builds the Session, it is the same Session.

### The Record is an append-only log — 2026-10-16

A save had to cost the size of a Turn, and a stale copy had to fail instead of overwriting a newer one.

| Option | For | Against |
|---|---|---|
| **Append only, with checkpoints for compaction** (chosen) | A save costs one Turn. Two writers cannot append the same sequence number. Every adapter maps it to an insert. Nothing is lost to compaction. | The Record grows; storage is bounded by the team. |
| Rewrite the whole Record, with a version check | The simplest contract. | A save costs the whole Session, megabytes for long ones. |
| Append, plus a rewrite for compaction | Compaction stays a replacement. | Two write paths, and the rewrite brings the cost back. |

### A load starts from the last Checkpoint

- **Instead of:** replaying the whole history.
- **Why:** rebuilding a Session costs the part since its last Checkpoint, so a Session that lives long stays cheap to rebuild.

### A Session holds only what is cheap to rebuild

- **Instead of:** a Session that opens its own subprocesses or holds its own long-lived connections.
- **Why:** anything expensive in a Session makes dropping it expensive. Model clients and connections belong to the instance, and its Sessions share them.

### Sessions are built when needed

- **Instead of:** a pool of prepared Sessions.
- **Why:** building one is cheap enough that a pool would only add state to keep right.

### An idle Session leaves memory at its TTL

- **Instead of:** staying in memory until someone destroys it.
- **Why:** memory is a cache, so an idle Session gives it back, after 60 minutes unless its Agent sets another TTL. It is compacted first, unless a setting turns this off (see [principle 6](#compaction-appends-a-checkpoint-when-the-cache-has-gone-cold)).

### A Fork writes the store and activates nothing

- **Instead of:** copying a live Session in memory.
- **Why:** the new Session is a new Record, so whichever instance its first request reaches activates it.

### A stream resumes from a position

- **Instead of:** a stream that must reconnect to the same instance, or start over.
- **Why:** an instance can go away at any time. A client reconnects to any instance with the last position it saw, and the stream goes on without gaps or duplicates.

## 6. The model's input is built, and laid out for the cache

What the model sees is a value built on every Turn, not the history sent as it is. It is laid out so the provider can serve most of each request from its prompt cache, because at scale what the model reads costs more than what the service holds ([PHILOSOPHY.md → 4](PHILOSOPHY.md#4-where-is-agent-as-a-service-going)).

**Ask:** does this change the prefix between two Turns?

### The Transcript and the Context are separate values

- **Instead of:** sending the history to the model as it is.
- **Why:** a `ContextBuilder` builds the Context from the Transcript on every Turn, so what the model sees can be chosen, laid out and inspected.

### The Context depends only on the Record and the Agent

- **Instead of:** state that lives on one instance.
- **Why:** every instance builds the same Context for a Session, so the provider's cache serves it whichever instance sends it.

### Stable content first, growing content last

- **Instead of:** an order by time or by kind.
- **Why:** the instruction and the Static blocks come first, then the tools sorted by ID, then the Transcript. Because the Record only grows at its end, one Turn's prefix is the next Turn's prefix.

### Nothing that changes every Turn goes into the request

- **Instead of:** a dynamic system prompt, or the current time in every request.
- **Why:** such content is never cached, so it costs its full price on every Turn. A fact the model needs now comes from a tool it calls.

### A change is appended, not rewritten into the prefix

- **Instead of:** rewriting the instruction or the tool list at the top of the request.
- **Why:** a change at the top makes the provider read the whole history again at full price.

### Full history by default, and your own `ContextBuilder` for the rest

- **Instead of:** built-in retrieval or memory search.
- **Why:** retrieval and windowing depend on the application's data. Gotato ships full history, Static blocks and Compaction.

### Cache markers are neutral in the core

- **Instead of:** one provider's cache syntax in the core.
- **Why:** the core marks where a provider may cache, and each wire API maps the marks to its own form, such as Anthropic's `cache_control`.

### One cache key per Session, inherited by a Fork

- **Instead of:** a key per request or per instance.
- **Why:** a provider that keys its cache serves one conversation from one cache, on whichever instance it runs. A Fork shares its parent's prefix, so it shares the key.

### Compaction appends a Checkpoint, when the cache has gone cold

- **Instead of:** replacing the history with a summary, or compacting while the cache is still warm.
- **Why:** nothing a Session said is lost to compaction. Compacting before a Session leaves memory at its TTL breaks no cache that is still alive, since the provider's cache has expired by then. How a Session is compacted is still open ([ROADMAP.md → Waiting for design](ROADMAP.md#waiting-for-design)).

## 7. A Session can be stopped and seen

A cloud Session runs with nobody watching each step. So a Session always ends, can be stopped at any point, and leaves enough behind to see what it did and what it cost.

**Ask:** if this step goes wrong, do the Events show it, and can it be stopped?

### Every Run ends

- **Instead of:** no limits, left to the caller.
- **Why:** Turns, tool calls, bytes and time each have a default cap, so a Run settles even when the model never answers or a call hangs.

### `context.Context` is the only cancellation

- **Instead of:** a cancellation mechanism of Gotato's own.
- **Why:** Go code already cancels through `ctx`, so every model call, tool call, store call and stream takes one, and stopping a Run reaches all of them.

### A mistake the model can fix becomes a tool result

- **Instead of:** failing the Run.
- **Why:** an unknown tool or bad arguments go back to the model as a failed tool result, and the Run goes on. The limits bound a model that keeps getting it wrong.

### Every Run settles with a status and a cause

- **Instead of:** one generic error.
- **Why:** a failure names the part that caused it: the model, the provider, a tool, the store, a limit, an extension, a cancel or Gotato.

### Every step is an Event, and every Event names its Session

- **Instead of:** log lines only.
- **Why:** Events are facts a program can read. Each carries its Session, and an Event of a Run also carries its Run, its Turn, a sequence number and a time.

### Every number about a Run comes from Events and `Usage`

- **Instead of:** counters kept inside Gotato, or a metrics endpoint of its own.
- **Why:** a team computes cache hits, tokens, failures and timings from its own telemetry, and gets the same numbers Gotato reports.

### The Context can be inspected, with the Turn's own code

- **Instead of:** a separate preview.
- **Why:** anyone can see what the model would receive now without calling it, and because inspection runs the code a Turn runs, the two cannot drift apart.

### Tests never call a network model

- **Instead of:** tests that depend on a live model.
- **Why:** fake and replay models keep every test deterministic, and CI runs with no provider credentials.

## When principles collide

When two principles pull apart, this order decides:

1. **Never lose a committed Turn.** A lost conversation breaks trust in the service.
2. **Stay minimal.** Weight is paid by every service that embeds Gotato, for as long as it does.
3. **Stay cheap.** Cost can be tuned later.

The Effect cap is not part of the trade: an Agent never runs a tool above its cap to save a Turn or a write.
