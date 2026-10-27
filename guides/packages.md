# Which packages do I import?

By the end of this page you know which Gotato packages your program needs, and what you give up by leaving one out. Install them as in [Install](install.md).

## The core is complete on its own

`gotato-core` (package name `gotato`) is the one package every program uses. It depends on the standard library alone, and it runs by itself: a Session with its conversation in memory, tools, and Events. Everything else adds what a service needs.

## What to import for what

| You want to | Import |
|---|---|
| Run an agent in your program | `gotato-core`, and a model: `gateway` for a provider's wire API, or your own `gotato.Model` |
| Keep conversations in a store and continue them on any pod | add `service` (the Runner) and `session` (the Store) |
| Keep long conversations small | `modelctx`; the Runner brings it |
| Call MCP servers | `adapter/mcp` (a module of its own) |
| Serve it over HTTP | `service/httpapi` |
| Register tools in code, without a Runner | `toolregistry` |
| Write tests with a fake model | `testkit` |

## What leaving one out costs

```text
gotato-core                          one process, in memory, no compaction
gotato-core + session                the conversation is saved and any pod can continue it
gotato-core + session + modelctx     long conversations compact on their own
```

- **Without `session`** a conversation lives in the memory of one process and is gone when it exits. It is the one package to add before you run on many pods.
- **Without `modelctx`** the context is the full history, which grows until it meets the model's window.

## How they depend on each other

Each package builds only on the ones to its left, and `layering_test.go` keeps it so:

```text
gotato-core ◄── session ◄── modelctx
     ▲             ▲            ▲
     │             └──── service ┘       service builds on core, session and modelctx
     ├── gateway
     └── toolregistry
```

`session` and `gateway` depend on the core alone, and not on each other.

## Next

- [Getting started](getting-started.md) uses the core and a model.
- [Embed Gotato in a service](embed-in-a-service.md) adds `service` and `session`.
