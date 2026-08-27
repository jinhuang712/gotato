# Shout-out and Project Origin

> Gotato is inspired by Pi's small and extensible agent-kernel design.

## Background

[Pi](https://pi.dev), created by Mario Zechner and developed with its contributors, is a minimal and highly extensible coding-agent harness.

The technical idea that led to Gotato is the Agent loop beneath Pi's terminal product:

```text
user input
    ↓
model stream
    ├── final response ──────────────► done
    └── Tool Calls
          ↓
        execute
          ↓
        append Tool Results
          └──────────────────────────► model stream
```

This loop demonstrates that a stateful, tool-using Agent runtime can remain compact and understandable.

## From Pi to Gotato

Gotato began with a concrete question:

> How can Pi-like Agent semantics power an idiomatic Go Agent service while preserving a small, transport-independent runtime boundary?

```text
Pi agent-kernel semantics
           │
           ▼
Go Runtime Kernel
  context · interfaces · bounded concurrency · ToolSets
           │
           ▼
Gotato Agent Service
  Protobuf · gRPC · lifecycle · Event streaming
```

The service use case reveals the necessary lifecycle, cancellation, Event, state, and error contracts. The runtime kernel keeps those semantics independent of transport and hosting.

## Semantic reference

The primary reference is `@earendil-works/pi-agent-core`:

```text
Agent state
Prompt and Continue
Model streaming
Message assembly
Tool execution
Lifecycle Events
Abort and WaitForIdle
Steering and Follow-up
Sequential and parallel Tool batches
Context and Tool interception
```

Gotato expresses these ideas through its own Go runtime and service contracts.

## Go-native expression

```text
Pi / TypeScript              Gotato / Go
──────────────────────       ─────────────────────────────
AbortSignal                  context.Context
AsyncIterable                explicit Model stream
Promise concurrency          bounded goroutines
mutable object composition   constructors and small interfaces
Tool arrays                  Tools plus ToolSets
```

ToolSet is a Gotato addition for grouping related operations and supporting staged capability discovery.

## Project boundary

Pi's terminal interface, coding Tools, session-tree product, resource loader, skills, themes, package manager, provider login, and project trust belong to Pi's coding-agent product.

Gotato focuses on reusable Agent execution semantics, a standard Go service boundary, and the runtime kernel beneath that service. It provides no first-party end-user UI.

## Credit and license

Pi is distributed under the MIT License. Gotato acknowledges Pi's creator and contributors as the source of its primary design reference.

Gotato is an independent Go implementation rather than an official Pi port. Attribution should be retained wherever code or derived material requires it.

- [Pi website](https://pi.dev)
- [Pi repository](https://github.com/earendil-works/pi)
- [`pi-agent-core` on npm](https://www.npmjs.com/package/@earendil-works/pi-agent-core)
