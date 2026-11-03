# Architecture

Gotato is a Go library that runs agent Sessions inside a service, on many copies of it at once. This page is the map: the parts, what each owns, how they connect and which folder each lives in. How a part works inside is in its own spec, named in each section. The principles the shape serves are in [DESIGNS.md](../DESIGNS.md).

A request may reach any Instance, which is one running copy of your service (a pod on Kubernetes). The Instances never talk to each other. They meet only in your Store, which keeps every Session as a log, so whichever Instance gets a request can load the Session and go on.

```text
 callers ──▶ your load balancer ──▶ Instance A │ Instance B │ Instance C     no arrows between Instances
                                          │
 inside one Instance (a layer imports only the layers below it)

   adapter/http   routes over the Runner                      ┐ beside the layers, import down:
   host           the Runner: admit, claim, warm, drain       │   model        calls a model over HTTP ──▶ model providers
   profile        AgentProfiles, TaskSpecs, the Registry      │   adapter/mcp  tool servers ──▶ MCP servers
   store          the Store contract, the Writer, two stores  │   testkit      fakes for tests
   record         the Record, compaction, read-back           ┘
   gotato         the Session, the Loop, Model, Tool, Event
   ref            the form of a reference
                                          │ Records, Liveness rows, Definitions
                                          ▼
                 your Store: a database, files, or memory for tests
```

The order is `host`, `profile`, `store`, `record`, `gotato`, `ref`, from the top. A package imports its own layer and the ones below it, never one above, and `gotato/layering_test.go` fails when an import breaks the order. `store` imports `record` and never the reverse, so the Record does not know how it is saved. The core runs alone: a program with `gotato` and a model needs no store, no host and no profile.

| Part | Folder | Spec |
|---|---|---|
| the core | `gotato/` | [session.md](session.md), [events.md](events.md) |
| the reference form | `ref/` | [compaction.md](compaction.md) |
| the Record, compaction and read-back | `record/` | [record.md](record.md), [compaction.md](compaction.md) |
| the Store | `store/`, `store/storetest/` | [store.md](store.md) |
| definitions | `profile/` | [profile.md](profile.md) |
| the host | `host/` | [multi-instance.md](multi-instance.md) |
| the model bridge | `model/` | [model.md](model.md) |
| the Context, tools, errors and Events across parts | see the threads below | [context.md](context.md), [tools.md](tools.md), [errors.md](errors.md), [events.md](events.md) |

## The core

The core is the live Session: a Loop that builds the Context, calls the Model, runs the Tools it asks for and commits each Step. It declares the contracts the rest plugs into (Model, Tool, ToolSet, Transcript, Journal, Event and the six Extension stages). It imports the standard library, `ref` and two shared packages of `internal/`, and it runs on options alone, with its conversation in memory.

- Folder: `gotato/`.
- Spec: [session.md](session.md) for the Session, the Run and its ending, [events.md](events.md) for the Events.

## The reference form

A reference is the short text a Segment or a spilled Tool Result carries to name one original, `result:<call_id>` or `msg:<message_id>`. The form is a contract, since a Segment keeps it for as long as the Record lives, so it has a package of its own that imports nothing.

- Folder: `ref/`.
- Spec: [compaction.md](compaction.md) covers the refs a Segment carries.

## The Record, compaction and read-back

The Record is a Session's append-only log of Entries, the one copy of what happened. Compaction closes old Messages into Segments and keeps the log whole, and read-back makes what left the Context readable again. The Context the model sees is built from the Record's Messages by the core, on every Turn, and is never stored.

- Folder: `record/`.
- Specs: [record.md](record.md), [compaction.md](compaction.md).

## The Store

The Store is the contract a database must keep: `Records` (one conditional write per batch, a load in one read), `Liveness` (one row per Instance, timed by the store's clock), the optional `Lister`, and `Definitions` for stored profiles. The Writer binds one Record to a Store as the Instance that holds its Claim. A memory store and a file store ship, and `storetest` is the suite an adapter of your own must pass.

- Folder: `store/`, and `store/storetest/`.
- Spec: [store.md](store.md).

## Definitions

An AgentProfile is a kind of Agent: model, instruction, tools, Effect cap, limits. A TaskSpec is a job that runs again and only narrows its profile. The Registry holds both by name, inline as Go values or from a store of documents, and the host asks it each time it builds a Session. The core does not need it.

- Folder: `profile/`.
- Spec: [profile.md](profile.md).

## The host

The host is the Runner: it turns the runtime into a service that serves any Session on any Instance. It admits a Run, claims the Session, builds a live Session from its AgentProfile, saves it and keeps it warm. It keeps the Instance's Liveness row, drains on shutdown and answers `not_writer` when a message misses the Writer. Its parts live under `host/internal/`.

- Folder: `host/`.
- Spec: [multi-instance.md](multi-instance.md).

## The model bridge

The bridge is the one place that speaks to a provider. Four layers, each replaceable, turn a `gotato.ModelConfig` into a `gotato.Model`: the wire API, the connection, the retry and the meaning of an error. The core sees one interface and one kind of error answer. It sits beside the layers and imports the core only.

- Folder: `model/`.
- Spec: [model.md](model.md).

## The adapters

Each adapter is a Go module of its own, so a service that does not use it carries none of it.

- `adapter/http/` is an HTTP API over the Runner: each route maps to one Runner method, errors carry the runtime's two answers in headers, and a client resumes a stream by number. It adds no Agent semantics.
- `adapter/mcp/` supplies an MCP server as a ToolSet, with one shared connection for all Sessions.
- Specs: [events.md](events.md) for the stream, [errors.md](errors.md) for the translation of errors, [tools.md](tools.md) for MCP.

## Threads through several parts

Four specs follow one thread across the layers and have no folder of their own.

| Thread | Where it runs | Spec |
|---|---|---|
| The Context: built every Turn, laid out for the cache, with a big result kept out | `gotato/` (builder, request, tags, Notices, spill), `record/` (Segments, read-back) | [context.md](context.md) |
| Tools: their Effect, the catalog, a load, a call, a big result | `gotato/` (registry, search, calls), `record/` (read-back), `adapter/mcp/` | [tools.md](tools.md) |
| Errors: what ran, and whether to retry | `gotato/` (the codes), `host/` (five more), `adapter/http/` (the table of statuses) | [errors.md](errors.md) |
| Events: written, then sent, numbered by position | `gotato/` (Feed), `store/` (the Writer), `host/` (Instance Events), `adapter/http/` (SSE) | [events.md](events.md) |

## Outside the shipped parts

- `testkit/` holds fakes for tests: scripted models, a recording tool, an Event recorder.
- `internal/` holds two shared packages (`text`, `hostlink`) that the shipped packages may import, and our own test tools that none imports: a fake model endpoint, the reference tasks, the workload runner and the test CLI `internal/cmd/gotato`.
- `examples/larkbot/` is a chat bot on several Instances, and `bench/refservice/` is the smallest service one would write around Gotato, used for the footprint. Both are modules of their own that nothing imports.

## A Run

A Run is one request to a Session. The diagram shows who does what, from the call to the answer.

```text
 caller ─ Run(request) ─▶ host
   1 host     check the request; resolve its TaskSpec                         profile
   2 host     admit: draining? memory bound? the service's Admitter          host
   3 host     take the Session's lock in this process
   4 host     open the Session: a warm copy if its head is current,
              else load from the last Checkpoint and write a Claim            store (Writer, Records)
   5 host     look up the AgentProfile by name, narrowed by the TaskSpec      profile
   6 host     build a live Session over the Writer, from the profile          gotato, record
   7 Session  Turn: build the Context ─▶ call the Model ─▶ run the Tools      gotato, model
              every Step is written before the next begins                    store
   8 Session  Events are sent to subscribers after they are written           gotato (Feed)
   9 host     close the live Session; release the Claim and keep a warm copy
 ◀ answer: the settled result, whatever its status
```

The Session is a goroutine over the Record and is built again for each Run. Dropping it loses nothing but a queued message and the Step in flight, because step 7 wrote everything else.

## A compaction

A compaction keeps a long Session inside the model's window without losing its history. It runs at a Turn boundary, as a Turn preparer the host installs from the AgentProfile.

```text
 Loop, before a model call ─▶ record.AutoCompact ─▶ Trigger: due, and why?
                                                       │ yes
                              Compactor (the frame): Cutter picks the kept tail
                                                     RecapWriter, if any: the Agent's own model
                                                     Builder builds the Segment from the closed Messages
                                                       │ one batch: Segment + Checkpoint
                              the Record queues it ─▶ store.Writer ─▶ Records.Append
 Loop ─▶ builds the Context again from the Record: Segments, then the kept tail
```

Parts return values, and only the frame writes, so a part that fails drops the compaction and never the Run. The host also compacts an idle warm Session shortly before its prompt cache expires, with the same frame.

## A Claim and a handoff

One Instance writes a Session at a time. A Claim is an Entry in the Record, written at the position after the head its Instance read, so a writer that lost the Claim always writes to a taken position and the Store refuses it.

```text
 Instance A runs S ──── A dies ────────────────────────────────▶ A's Liveness row expires
 a message for S reaches Instance B
   B loads S: the last Claim is A's, and S is busy
   B asks the Store: is A's row alive?
     no  ─▶ B writes a Claim at the next position, with the next Epoch
            the Run that never ended is ended as failed, cause `interrupted`
     yes ─▶ B waits, then answers `busy`; a Steer or Abort answers `not_writer`, naming A
 A wakes up late and appends ─▶ the position is taken ─▶ conflict, A stops
```

A Session that was released is claimed at once, and a warm copy holds no Claim, so nothing is pinned to an Instance. The Store's conditional writes are the only coordination.

## How the shape serves the design

| Principle | Where it shows | 
|---|---|
| [The store is the only truth](../DESIGNS.md#1-the-store-is-the-only-truth) | `record` keeps the log, `store` keeps it whole or refuses the write, and the live Session is rebuilt |
| [A request only grows at its end](../DESIGNS.md#2-a-request-only-grows-at-its-end) | the request layout in `gotato`, the Segments in `record`, and the tool layout the core hands to `model` |
| [The Context is a bounded view](../DESIGNS.md#3-the-context-is-a-bounded-view) | the builder in `gotato`, and Segments and read-back in `record` |
| [A failure stays local](../DESIGNS.md#4-a-failure-stays-local) | the error answers made in `gotato`, `host` and `model`, and translated by the adapters |
| [Keep subtracting](../DESIGNS.md#5-keep-subtracting) | one core with options; everything else is optional or beside it |
| [Stay inside the boundary](../DESIGNS.md#6-stay-inside-the-boundary) | no routing, no credentials, no database: the Store, the Admitter and the connection are seams |
| [Serve purpose-built agents](../DESIGNS.md#7-serve-purpose-built-agents) | the Effect cap in `gotato` and `profile`, with the bounds in the definition |
| [No instance is special](../DESIGNS.md#8-no-instance-is-special) | `store` conditional writes and Liveness, `host` Claims and warm copies |
| [A Session can be stopped and seen](../DESIGNS.md#9-a-session-can-be-stopped-and-seen) | the Context inspection in every Run, the Events in `gotato`, `adapter/http` streams |
| [Each layer does one job, and each default can be replaced](../DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced) | the layer order itself, the four layers of `model`, the parts of the Compactor, the Runner and the Session |
