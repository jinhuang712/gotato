# Which packages do I import?

> [Wiki](../README.md) › [Reference](../README.md#reference) › Which packages do I import?

By the end of this page you know which Gotato packages your program needs, and what you give up by leaving one out. Install them as in [Install](../how-to/install.md).

## The core is complete on its own

`gotato` is the one package every program uses. It depends on the standard library alone, and it runs by itself: a Session with its conversation in memory, tools, and Events. Everything else adds what a service needs.

## Where each package lives

The layout follows three rules: the module root holds no Go code; a directory's name is its package's name, so no import needs an alias (`adapter/http` is the one exception, see below); and a test-helper package sits next to what it tests, as `net/http/httptest` does.

```text
github.com/jinhuang712/gotato
├── gotato/            the core: Session, Tool, Model, Event (standard library only)
├── ref/               the form of a reference (standard library only)
├── record/            the Record, the Context it yields, compaction and read-back
├── store/             saving: the Store contract, the Writer, the memory and file stores
│   └── storetest/     the Store contract suite
├── profile/           AgentProfile, TaskSpec, the Registry
├── model/             the bridge to a model over HTTP
├── host/              hosting many Sessions on many instances: the Runner
├── testkit/           fakes for tests
├── internal/          text, hostlink: shared by the packages above (standard library only);
│                      the rest are our own test tools, the test CLI (internal/cmd/gotato) among them,
│                      which no shipped package imports
├── adapter/http/      a module of its own: an HTTP API over host
├── adapter/mcp/       a module of its own
├── examples/larkbot/  a module of its own
└── bench/refservice/  a module of its own
```

`adapter/http` is a package named `http`, the name of `net/http`, so a program that serves it imports it with an alias: `httpapi "github.com/jinhuang712/gotato/adapter/http"`.

## What to import for what

| You want to | Import |
|---|---|
| Run an agent in your program | `gotato`, and a model: `model` for a provider's wire API, or your own `gotato.Model` |
| Keep conversations in a store and continue them on any pod | add `host` (the Runner) and `store` (the Store) |
| Keep long conversations small | `record` (compaction); `store` and the Runner bring it |
| Register Agents and jobs that run again, inline or from your own store | `profile` (AgentProfile, TaskSpec, the Registry); a Runner needs one |
| Call MCP servers | `adapter/mcp` (a module of its own) |
| Serve it over HTTP | `adapter/http` (a module of its own) |
| Write tests with a fake model | `testkit` |
| Prove your own Store adapter keeps the store contract | `store/storetest` |

## What leaving one out costs

```text
gotato                               one process, in memory, no compaction
gotato + record                      long conversations compact on their own
gotato + record + store              the conversation is saved and any pod can continue it
```

- **Without `store`** a conversation lives in the memory of one process and is gone when it exits. It is the one package to add before you run on many pods.
- **Without `record`** the context is the full history, which grows until it meets the model's window.

## How they depend on each other

Every shipped package builds on the core. The diagram shows the layers, with each package above the ones it imports; `model` and `adapter/mcp` sit beside the chain and import the core alone:

```text
adapter/http
      │
      ▼
    host
      │
      ▼
  profile
      │
      ▼
   store ◄──────── store/storetest
      │
      ▼
   record ◄─────── testkit        model   adapter/mcp
      │                             │         │
      ▼                             ▼         ▼
   gotato  ◄────────────────────────┴─────────┘
      │
      ▼
    ref
```

An arrow reads "imports". Some imports skip a layer, and the table lists all of them:

| Package | Imports |
|---|---|
| `ref` | none (the standard library) |
| `gotato` | `ref` (and the standard library, and the shared `internal/text` and `internal/hostlink`) |
| `record` | `gotato`, `ref` |
| `store` | `gotato`, `record` |
| `model` | `gotato` (and the shared `internal/text`) |
| `testkit` | `gotato`, `record` |
| `store/storetest` | `gotato`, `record`, `store` |
| `profile` | `gotato`, `record`, `store` |
| `host` | `gotato`, `record`, `store`, `profile` |
| `adapter/http` | `gotato`, `record`, `store`, `host` |
| `adapter/mcp` | `gotato` |

The order is `host`, `profile`, `store`, `record`, `gotato`, `ref`: a package imports its own layer and the ones after it, never one before it. `store` imports `record`, and `record` does not know how it is saved. `model` and `adapter/mcp` depend on the core alone, and not on each other (`model` also uses the shared `internal/text`). `testkit` imports the core and `record`, and `store/storetest` the core, `record` and `store`. `profile` sits between `host` and `store`: it imports no `host`, and the core, `record` and `store` import none of it, so a program that does not host Agents never loads it. Of the shipped packages, only `adapter/http` imports `host`.

`gotato/layering_test.go` holds this table as `allowedImports` and the order as `layerOrder`, and fails on any import outside the table or against the order, on a table entry no code uses, and on a new package that is not in it. To add a dependency on purpose, extend the table in the same commit and say why.

## Next

- [Getting started](../tutorials/getting-started.md) uses the core and a model.
- [Embed Gotato in a service](../how-to/run-sessions-on-many-pods.md) adds `host`, `profile` and `store`.
- [Write a tool](../how-to/write-a-tool.md), [wrap a model](../how-to/wrap-a-model.md), [write a Store](../how-to/write-a-store-adapter.md), and [pick an extension point](extension-points.md).
- [HTTP in five minutes](http-in-five-minutes.md), a [minimal `main.go`](../tutorials/a-minimal-main.md), and [tests with fakes](../how-to/test-with-fakes.md).
