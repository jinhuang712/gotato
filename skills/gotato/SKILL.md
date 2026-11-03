---
name: gotato
description: Add an agent to a Go service with Gotato (github.com/jinhuang712/gotato): a Session with a model and tools, a Runner over a Store for many pods, the HTTP API, and tests with fakes. Use when a Go project needs an embedded LLM agent, a tool-calling loop, or a multi-instance agent service.
---

# Using Gotato from a Go project

Gotato is a minimal, synchronous Go library. An **Agent** is a definition (model, instruction, tools, limits). A **Session** is one conversation running it. Its **Record** is saved in a **Store**, so any pod can load it. Read the guide for the task; do not guess an API from memory.

## Pick the task

| You want | Read |
|---|---|
| add the module | `wiki/how-to/install.md` (pin `@v0.2.0`, never `@main`) |
| one in-process agent, a tool, Events | `wiki/tutorials/getting-started.md` |
| many pods, a Store, shutdown | `wiki/how-to/run-sessions-on-many-pods.md`, `wiki/tutorials/a-minimal-main.md` |
| an HTTP API you can curl | `wiki/tutorials/http-in-five-minutes.md` (`adapter/http`, a module of its own) |
| your own Tool, ToolSet or Model | `wiki/how-to/write-a-tool.md`, `wiki/how-to/group-and-supply-tools.md`, `wiki/how-to/wrap-a-model.md` |
| your own database as the Store | `wiki/how-to/write-a-store-adapter.md` |
| tests with no network | `wiki/how-to/test-with-fakes.md` |
| change what the model sees, or hook a Turn | `wiki/reference/extension-points.md` |
| MCP server tools | `adapter/mcp` (a module of its own, Go 1.25) |
| a term you do not know | `GLOSSARY.md` |

## Rules that save time

- Import the core as `github.com/jinhuang712/gotato/gotato` (package `gotato`); `ref`, `record`, `store`, `profile`, `host`, `model` and `testkit` are siblings, and `adapter/http` and `adapter/mcp` are modules of their own. Layers import down only: `host` -> `profile` -> `store` -> `record` -> `gotato` -> `ref`.
- Start with `testkit` fakes (`EchoModel`, `DemoModel`, `NewFakeModel(testkit.ToolCalls(...), testkit.Text(...))`). A real model comes from `model.New(wire, connection, config)` (the CLI and the workload tool read a YAML file for it); no key is needed to start.
- A Tool is `gotato.NewFuncTool(name, description, func(ctx, In) (string, error))`. Declare the effect read-only with `gotato.ToolWithEffect(tool, gotato.ToolEffectRead)` when it is; an undeclared Tool counts as destructive.
- A `testkit.FakeModel` shares one script across all Runs; in a server test use `testkit.NewFuncModel` (it has no `Delay`: wait inside the function).
- Pass `ctx` everywhere; it cancels model and tool calls.
- A tool's error text reaches the model only if the error implements `gotato.PublicError` (a `Public() string` method); otherwise the model sees `tool failed`.
- A `ToolSet` goes in `Agent.ToolSets` (with a `Mode`), not in `Agent.ToolSources`. An Extension goes in `Agent.Extensions`. There is no `Agent.Options`: the Runner builds every `SessionOption`.
- A Session has no instruction or ceiling of its own. Another instruction is another Agent, named by `RunRequest.Agent`. A metadata key that starts with `gotato.` is the Runner's, and `CreateSession`, `RunRequest.Metadata` and `RunRequest.Update` refuse it with `host.ErrReservedKey`.
- `Runner.CreateSession(ctx, host.CreateSessionRequest{...})`, `Fork` and `Record` return values (`record.Info`, `record.Info`, `record.Document`); there is no `Runner.Store()`.
- A wrapper Model must implement `Unwrap() gotato.Model`, or the context window and cache hints are lost.
- On shutdown: `Drain`, then `Shutdown` the HTTP server, then `Runner.Close`.
- Errors are `*gotato.RuntimeError` with a code. `gotato.NotRun(err)` means nothing happened, so resend elsewhere; `gotato.Retryable(err)` means it ran and a later retry may work. HTTP carries both as `Gotato-Not-Run` and `Gotato-Retryable`.
- Forwarding between pods is off; `httpapi.WithForwarding(client, allow)` (`httpapi` is `github.com/jinhuang712/gotato/adapter/http`) needs an allow function for writer addresses.
- Run `storetest.RunRecords` and `storetest.RunLiveness` (package `store/storetest`) on any `store.Records` and `store.Liveness` you write (and `storetest.RunLister` on a `store.Lister`). The Entry, Header, Info and Loaded such a store returns are `record` types.

## Smallest working program

```go
s, err := gotato.NewSession(ctx,
    gotato.WithModel(testkit.EchoModel{}),
    gotato.WithInstruction("You are a helpful assistant."),
)
if err != nil { return err }
defer s.Close(ctx)

result, err := s.Prompt(ctx, gotato.UserMessage("hello"))
fmt.Println(gotato.TextOf(*result.FinalMessage)) // echo: hello
```

## Do not

- invent sub-agents, roles or hierarchy types: there are only Agents; roles live in `Session.Metadata`.
- put conversation state outside the Store, or call a model without the caller's `ctx`.
