# Which packages do I import?

By the end of this page you know which Gotato packages your program needs, and what you give up by leaving one out. Install them as in [Install](install.md).

## The core is complete on its own

`gotato` is the one package every program uses. It depends on the standard library alone, and it runs by itself: a Session with its conversation in memory, tools, and Events. Everything else adds what a service needs.

## Where each package lives

The layout follows three rules: the module root holds no Go code; a directory's name is its package's name, so no import needs an alias; and a test-helper package sits next to what it tests, as `net/http/httptest` does.

```text
github.com/jinhuang712/gotato
├── gotato/            the core: Session, Tool, Model, Event (standard library only)
├── session/
│   └── storetest/     the Store contract suite
├── modelctx/  gateway/  service/  service/httpapi/  testkit/  cmd/gotato/
├── internal/          our own test tools
├── adapter/mcp/       a module of its own
├── examples/larkbot/  a module of its own
└── bench/refservice/  a module of its own
```

## What to import for what

| You want to | Import |
|---|---|
| Run an agent in your program | `gotato`, and a model: `gateway` for a provider's wire API, or your own `gotato.Model` |
| Keep conversations in a store and continue them on any pod | add `service` (the Runner) and `session` (the Store) |
| Keep long conversations small | `modelctx`; the Runner brings it |
| Call MCP servers | `adapter/mcp` (a module of its own) |
| Serve it over HTTP | `service/httpapi` |
| Write tests with a fake model | `testkit` |
| Prove your own Store adapter keeps the store contract | `session/storetest` |

## What leaving one out costs

```text
gotato                               one process, in memory, no compaction
gotato + session                     the conversation is saved and any pod can continue it
gotato + session + modelctx          long conversations compact on their own
```

- **Without `session`** a conversation lives in the memory of one process and is gone when it exits. It is the one package to add before you run on many pods.
- **Without `modelctx`** the context is the full history, which grows until it meets the model's window.

## How they depend on each other

Each package builds only on the ones to its left, and `layering_test.go` keeps it so:

```text
gotato ◄── session ◄── modelctx
     ▲             ▲            ▲
     │             └──── service ┘       service builds on core, session and modelctx
     └── gateway
```

`session` and `gateway` depend on the core alone, and not on each other.

## Next

- [Getting started](getting-started.md) uses the core and a model.
- [Embed Gotato in a service](embed-in-a-service.md) adds `service` and `session`.
