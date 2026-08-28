# Shout-out and Project Origin

> Gotato is inspired by Pi's small and extensible Agent kernel.

## Background

[Pi](https://pi.dev), created by Mario Zechner and developed with its contributors, is a minimal and highly extensible coding-agent harness. Its core loop demonstrates that stateful, tool-using Agent execution can remain compact and understandable:

```text
user input
    ↓
Model stream
    ├── final response → done
    └── Tool Calls → execute → Tool Results → Model stream
```

## From Pi to Gotato

Gotato asks how those semantics can become an idiomatic Go Agent Core that is useful both inside an existing service and behind a hosted Agent API:

```text
Pi-like loop
     ↓
Go Agent Core
  Context · interfaces · bounded work
     ├── embedded Go application
     └── optional Orchestrator + gRPC Host
```

The Agent goroutine, channel boundary, ToolSet model, service boundary, and delivery contracts are Gotato's own design. The service host does not replace or duplicate the Agent Loop.

## Semantic reference

The primary reference is `@earendil-works/pi-agent-core`, including Agent state, Prompt/Continue, Model streaming, Message assembly, Tool execution, lifecycle Events, Abort/WaitForIdle, Steering/Follow-up, sequential/parallel Tool batches, and interception.

Gotato expresses these ideas through Go goroutines, channel communication, Context cancellation, bounded capability work, explicit Extensions, and separate Hosted orchestration.

## Project boundary

Pi's terminal product, coding Tools, session tree, resource loader, skills, themes, package manager, provider login, and project trust are outside Gotato's scope. Gotato provides no first-party end-user UI.

## Credit and license

Pi is distributed under the MIT License. Gotato is an independent Go design rather than an official Pi port. Attribution should be retained wherever derived code or material requires it.

- [Pi website](https://pi.dev)
- [Pi repository](https://github.com/earendil-works/pi)
- [`pi-agent-core` on npm](https://www.npmjs.com/package/@earendil-works/pi-agent-core)
