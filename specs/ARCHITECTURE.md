# Architecture

Gotato is a Go library that runs agent Sessions inside your service, on as many copies of it as you run. This page is the map: the parts, what each does, how they connect and where each lives. How a part works inside is in its own spec, and what the user gets from it is in [FEATURES.md](../FEATURES.md).

```text
            callers: your service's code · a bot · a job · another service
                 │ Go API                         │ HTTP
                 │                        ┌───────▼────────┐
                 │                        │ adapter/http   │  the HTTP API
                 │                        └───────┬────────┘
                 ▼                                ▼
        ┌──────────────────────────────────────────────────────┐
        │ host       hosting on many instances                 │
        └──────┬────────────────────┬──────────────────────────┘
               ▼                    │
        ┌──────────────┐            │
        │ profile      │ definitions│
        └──────┬───────┘            │
               ▼                    ▼
        ┌──────────────────────────────────────────────────────┐
        │ store      saving: Records, Liveness, Definitions    │ ◀── your database adapter
        └──────┬───────────────────────────────────────────────┘
               ▼
        ┌──────────────────────────────────────────────────────┐
        │ record     the Record, compaction, read-back         │
        └──────┬───────────────────────────────────────────────┘
               ▼
        ┌──────────────────────────────────────────────────────┐
        │ gotato     the Loop, the Session, the request, tools │ ◀── your tools and extensions
        └──────┬─────────────────────────────┬─────────────────┘
               ▼                             │ calls through the Model interface
        ┌──────────────┐             ┌───────▼────────┐         ┌──────────────┐
        │ ref          │             │ model          │──HTTP──▶│ a provider   │
        │ a reference  │             │ the bridge     │         └──────────────┘
        └──────────────┘             └────────────────┘

        adapter/mcp ──▶ an MCP server's tools as a tool set of gotato
        testkit     ──▶ fake and replay models for tests
```

The instances of your service never talk to each other. They meet only in your store, which keeps every Session as a log, so whichever instance gets a request can load the Session and go on. Your load balancer routes, and Gotato builds no network of its own.

## The layers

Each package is one layer, and imports only the layers below it: `host`, `profile`, `store`, `record`, `gotato`, `ref`, from the top. `store` imports `record` and never the reverse, so the Record does not know how it is saved. `model`, `testkit` and the adapters sit beside the layers and import down, and no layer imports them. `gotato/layering_test.go` fails when an import breaks the order.

The core runs alone. A program with `gotato` and a model needs no store, no host and no definitions.

## The Loop and the live Session

The live Session runs the Loop: it builds a request, calls the model, runs the tools the model asks for and commits each step, until the model answers. It holds the Run in flight, the messages that wait for it and the extensions at fixed stages. It declares the contracts the other parts plug into: the model, a tool, a tool set, the Transcript and an Event.

- Folder: `gotato/`.
- Spec: [session.md](session.md).

## Building the request

On every Turn the core builds the request from the Session's Transcript, in an order the provider's cache can reuse. Notices join at its end, and a large tool result stays out except for its preview.

- Folder: `gotato/`.
- Spec: [context.md](context.md).

## Tools

The core resolves the tools a Session may offer, keeps the catalog the model loads them from, checks each call and commits its result. A large result is spilled to the Record and read in pages.

- Folder: `gotato/`.
- Spec: [tools.md](tools.md).

## The reference form

A reference is the short text that names one original, such as a tool result or a message. A Segment keeps it for as long as the Record lives, so its form is a package of its own that imports nothing.

- Folder: `ref/`.
- Spec: [compaction.md](compaction.md).

## The Record

The Record is a Session's append-only log, the one copy of what happened. A rebuild reads its facts from the last Checkpoint, and a fork reads its parent's log.

- Folder: `record/`.
- Spec: [record.md](record.md).

## Compaction and read-back

Compaction closes old messages into Segments and merges the oldest Segments when they grow past their budget. It never rewrites the log, and read-back makes what left the request readable again. A fixed frame writes and keeps the guarantees, around parts a team can replace.

- Folder: `record/`.
- Spec: [compaction.md](compaction.md).

## Saving

The store is the contract a database keeps: Records, Liveness and the optional Lister and Definitions. The Writer binds one Record to the store for the instance that holds its claim. A memory store and a file store ship, beside the suite an adapter of your own must pass.

- Folders: `store/`, `store/storetest/`.
- Spec: [store.md](store.md).

## Definitions

A profile defines a kind of Agent, and a task a job that runs again and only narrows its profile. The registry holds both by name, from code or from a store, and the host asks it each time it builds a Session.

- Folder: `profile/`.
- Spec: [profile.md](profile.md).

## The model bridge

The bridge is the one place that speaks to a provider. Four layers turn a model's configuration into the core's Model: the wire API, the connection, the retry and the meaning of an error. No layer imports it: your service puts it in a profile, or hands it to a Session directly.

- Folder: `model/`.
- Spec: [model.md](model.md).

## Hosting on many instances

The host turns the runtime into a service that serves any Session on any instance. It admits a Run, claims the Session, builds a live Session from its profile, saves it and keeps it warm. It keeps the instance's liveness row and drains on shutdown. Its parts, each with one job, live under `host/internal/`.

- Folders: `host/`, `host/internal/`.
- Spec: [host.md](host.md).

## The HTTP API

The HTTP API maps each route to one host call, translates errors into statuses and streams Events as server-sent events. It adds no Agent semantics. It is a Go module of its own.

- Folder: `adapter/http/`.
- Spec: [http.md](http.md).

## MCP tools

The MCP adapter supplies one MCP server as a tool set, with one connection shared by the instance's Sessions. It is a Go module of its own.

- Folder: `adapter/mcp/`.
- Spec: [mcp.md](mcp.md).

## Testing

The test kit has fake and replay models, a fake tool and an Event recorder. Our own test tools, which no shipped package imports, add a fake provider over HTTP, the reference tasks, the workload runner and the test CLI.

- Folders: `testkit/`, `internal/fakemodel/`, `internal/reftasks/`, `internal/workload/`, `internal/cmd/`.
- Spec: [testing.md](testing.md).

## Across the parts

Two mechanisms run through several parts, and each keeps one set of rules for all of them.

| Mechanism | Where it runs | Spec |
|---|---|---|
| Errors: whether a request ran, and whether a retry may succeed | made in `gotato/`, `model/` and `host/`; translated in `adapter/http/` | [errors.md](errors.md) |
| Events: numbered by position, written before they are sent | made in `gotato/` and `host/`; stored by `store/`; streamed by `adapter/http/` | [events.md](events.md) |

## A Run across the parts

A Run is one request to a Session. The diagram shows which part does each step, from the call to the answer.

```text
 caller ─ Run ─▶ host
   1  host     check the request; resolve its task                        profile
   2  host     admit: draining? the memory limit? your admission rule     host
   3  host     open the Session: a warm copy if its head is current,
               else load from the last Checkpoint and write a claim       store
   4  host     look up the profile by name, narrowed by the task          profile
   5  host     build a live Session over the Writer                       gotato, record
   6  Session  each Turn: build the request ─▶ call the model ─▶ run tools gotato, model
               every step is written before the next begins               store
   7  Session  Events go to subscribers after they are written            gotato
   8  host     close the live Session; release the claim; keep it warm    host, store
 ◀ the settled result, whatever its status
```

The live Session is built again for each Run. Dropping it loses only a queued message and the step in flight, because step 6 wrote everything else.

## Outside the library

- `internal/text/` and `internal/hostlink/` are shared packages that the shipped packages may import.
- `examples/larkbot/` is a chat bot on several instances, a module of its own that nothing imports.
- `bench/refservice/` is the smallest service one would write around Gotato, used to measure the footprint.
