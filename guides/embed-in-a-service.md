# Embed Gotato in a service

By the end of this page your service runs agent conversations that survive a restart and can be continued by any pod. You need the [first page](getting-started.md), and a place to keep Sessions: a directory (`store.NewFileStore`), or your own `store.Records` and `store.Liveness`, which a `store.Store` holds side by side.

## The Runner

A `host.Runner` runs Sessions for you. It opens a Session's Record from the store, runs the message, writes every step as it happens, and lets the Session go.

```go
// One FileStore is both halves of a Store: Records and Liveness. Your own may be two.
st, err := store.NewFileStore("/var/lib/myservice/sessions")
if err != nil {
    return err
}
profiles := profile.NewRegistry(nil)
if err := profiles.RegisterAgentProfileInline(profile.AgentProfile{Name: "default", Model: m, Tools: tools}); err != nil {
    return err
}
runner, err := host.New(host.Config{
    Store:        store.Store{Records: st, Liveness: st},
    Profiles:     profiles,
    DefaultAgent: "default",
})
if err != nil {
    return err
}
defer runner.Close(ctx)

first, err := runner.Run(ctx, host.RunRequest{Prompt: "summarize the open tickets"})
// The same Session goes on, here or on another pod.
second, err := runner.Run(ctx, host.RunRequest{SessionID: first.SessionID, Prompt: "and the urgent ones?"})
fmt.Println(second.FinalText)
```

An Agent is a definition, in code a `profile.AgentProfile`: a model, an instruction, tools, limits. A Session is a conversation that runs it. The Runner keeps the two apart, so one Agent serves many Sessions. The Runner looks the Agent up by name in the `profile.Registry` you give it, each time it builds a Session, and `DefaultAgent` names the one a request that names none runs. How the pod keeps idle Sessions (`Config.Warm`) and when a pause counts as time away (`Config.TimeAwayAfter`) are the pod's settings, not the Agent's.

This example is run as a test: `ExampleRunner_Run` in [host/run_test.go](../host/run_test.go).

## Many pods

Pods that share a store run a Session one at a time. A step is written before it is sent, so a crash loses at most the step in flight.

```text
pod A runs session S ──► pod A is removed
next message for S ──► pod B: opens S from the store, takes it over, goes on
```

- **One writer.** While a Run is going on pod A, a message for the same Session that reaches pod B is not run there: pod B says `not_writer` and names pod A. You send it to pod A, or turn on forwarding in the HTTP endpoint.
- **Waiting for the writer** is a plain answer to `not_writer` for one replica: retry the `NotRun` after a back-off until the writer finishes, then pod B takes the Session over. The limit: a long Run holds the second message that long. It was not tried with several instances writing one Session. Gotato does not forward by itself, since it builds no infrastructure ([Decision 024](../plan/decisions/024-gotato-builds-no-infrastructure.md)).
- **Taking over.** Pod B takes the Session over when pod A's Run has ended, or pod A is gone.
- **Mid-Run messages.** A message sent to a Session while it runs can steer the Run or wait for the next turn. `runner.Send` is the call; [FEATURES.md → F1.5: Steer a Run in flight](../FEATURES.md#f15-steer-a-run-in-flight) has the rules.

## Errors say what to do

Every error answers two questions, and HTTP only translates the answers.

```go
out, err := runner.Run(ctx, req)
switch {
case gotato.NotRun(err):
    // refused before it started (draining, full, busy, no such Session): nothing
    // happened, so send it to another pod or ask the user to try again
case errors.Is(err, host.ErrNotPersisted):
    // the Run failed to save (store_failure): the Record does not hold it,
    // though a reply was made (out.FinalText)
case gotato.Retryable(err):
    // it ran; sending the same request again later may succeed
}
```

`Run` returns a response and an error together. A refusal before the Run started is the zero response and an error marked not run; a response with a `SessionID` is a Run that started and settled, with its status, its final text and its usage, whatever `err` is, so read the response before you return on `err != nil`. Pass `RequestID` on the request to find it in the Record (`run_started`) and in the response; Gotato does not look for a repeat of it. A chat platform delivers an event again when it is not acked in time, so pass its message id as `MessageRequest.RequestID` and a repeat can be found in `run_started`.

## Another instruction is another Agent

A Session has no instruction of its own: the instruction, the model, the tools, the Effect cap, the Sampling and the compaction ceiling all come from its Agent. To answer one message in French, or to give one kind of job a tighter cap, register another Agent and name it on the request.

```go
profiles := profile.NewRegistry(nil)
err := profiles.RegisterAgentProfileInline(profile.AgentProfile{Name: "default", Model: m, Instruction: "You answer questions about open tickets.", Tools: tools})
err = profiles.RegisterAgentProfileInline(profile.AgentProfile{Name: "french", Model: m, Instruction: "You answer questions about open tickets, in French.", Tools: tools})
runner, err := host.New(host.Config{
    Store:        store.Store{Records: st, Liveness: st},
    Profiles:     profiles,
    DefaultAgent: "default",
})

// Answer this message in French. The Session now runs "french", and stays on it
// until a request names another Agent.
out, err := runner.Run(ctx, host.RunRequest{SessionID: id, Agent: "french", Prompt: "what is open today?"})
```

`RunRequest.Agent` is the one way to change a Session's Agent, and it is saved with the Run (`SkipSave` keeps the Session as it was). An AgentProfile holds typed fields and no function that runs on the Session: `Instruction`, `Tools`, `ToolSets` (each a `profile.ToolSet{Set, Mode}`: listed, folded or active), `ToolSources`, `ToolSearcher`, `MaxEffect`, `Sampling`, `Extensions`, `ContextBuilder`, `Limits`, `ReadBack`, `RunTimeout` and `Compact`. There is no `AgentProfile.Options`: the Runner builds every `gotato.SessionOption` itself, so an Agent cannot replace the Writer or raise its own cap. `ReadBack` is the one hole, and only for the read-back tool.

### A job that runs again is a TaskSpec

A report every morning has its own background and wants narrower bounds than the Agent it runs. Register it as a `profile.TaskSpec`: it names its AgentProfile, a Background, and bounds that can only narrow it (a subset of the Tools, an Effect cap no higher, a timeout no longer, a `MaxTokens` no higher).

```go
err = profiles.RegisterTaskSpecInline(profile.TaskSpec{
    Name:       "morning-report",
    Profile:    "default",
    Background: "Report the tickets that opened since yesterday.",
    MaxEffect:  gotato.ToolEffectRead,
})
out, err := runner.Run(ctx, host.RunRequest{Task: "morning-report", Prompt: "Go."})
```

A Run of a TaskSpec opens a new Session, so it takes no `SessionID` and no `Agent`. The Background is added once after the instruction, and the Session keeps the TaskSpec as it was, so another pod builds it the same after you change the registered spec. A spec that widens its profile is refused (`profile.ErrWidens`). `RunRequest.TaskSpec` carries a spec as a value instead of by name. `CreateSession` takes the same `Task` and `TaskSpec` and opens the same Session without running it, so a scheduler creates it with an ID it derives (the job and the day, say), runs it by that ID, and a retry of either call is safe: a second create of the ID is `already_exists`. Over HTTP, `POST /v1/sessions` takes `{"task": "morning-report", "id": "..."}`, a registered name and never a value, and `GET /v1/tasks` lists the names.

### Definitions from your own store

A definition can live in your database instead of your code. Fill a `profile.Catalog` with the models, tools and ToolSets your code offers, and register a name whose document is read from your `store.Definitions`:

```go
catalog, err := profile.NewCatalog(map[string]gotato.Model{"main": m}, tools, nil)
profiles := profile.NewRegistry(catalog)
err = profiles.RegisterAgentProfileWithStore("support", defs)  // defs is a store.Definitions
err = profiles.RegisterTaskSpecWithStore("nightly", defs)
```

The stored document is data only (a model name, Tool names, numbers and text); it selects from the Catalog and never adds to it. Each name is registered once, and a row whose name is not registered is not a definition. [Write a store](write-a-store.md) says how to build `defs`.

### Keys under `gotato.` are the Runner's

Session metadata is yours, except for the keys that start with `host.ReservedPrefix` (`"gotato."`). `CreateSession`, `RunRequest.Metadata` and `RunRequest.Update` refuse such a key with `host.ErrReservedKey` (`invalid_argument`, not run, naming the key) before anything is written, and the HTTP API answers 400. A tool is switched only by `Runner.SetToolActive`, and the cache key only by `Runner.Fork`.

```go
_, err := runner.Run(ctx, host.RunRequest{
    SessionID: id,
    Prompt:    "what is open today?",
    Update:    map[string]string{"gotato.instruction": "Answer in French."}, // refused
})
errors.Is(err, host.ErrReservedKey) // true; gotato.NotRun(err) is true; nothing was written
```

The tests `TestAnInstructionInMetadataIsRefusedAndAnotherAgentIsTheWay` in [host/run_instruction_test.go](../host/run_instruction_test.go) and `TestAReservedKeyIsRefusedByRunMetadataAndUpdate` in [host/reserved_test.go](../host/reserved_test.go) pin this.

### Values, not handles

`CreateSession` takes one request and `Fork` and `Record` answer values you can keep and read, but not write through:

```go
info, err := runner.CreateSession(ctx, host.CreateSessionRequest{
    Agent:    "default",
    ID:       "ticket-4711",                  // optional; a taken ID is ErrSessionExists
    Metadata: map[string]string{"owner": "ann"},
})                                           // info is a record.Info
child, err := runner.Fork(ctx, info.ID, "")  // a record.Info
doc, err := runner.Record(ctx, info.ID)      // a record.Document: a copy
```

There is no `Runner.Store()`; keep the `store.Store` you gave to `host.New` if you read Records yourself.

## Shutting down

On SIGTERM, call `runner.Drain(ctx)`: the pod stops taking new Runs, lets each Run finish, save and release its Session, and answers 503 on `/readyz` so the load balancer sends traffic elsewhere. Then `runner.Close(ctx)` removes the pod's row, and other pods take its Sessions over at once. In the maintainer's rolling-deploy runs on a local cluster, no message was lost this way.

## Over HTTP

```go
mux.Handle("/agent/", http.StripPrefix("/agent", httpapi.New(runner)))
```

`httpapi` is `github.com/jinhuang712/gotato/adapter/http`, a module of its own (`go get github.com/jinhuang712/gotato/adapter/http`; see [Install](install.md)), imported with an alias because `net/http` is imported too.

The handler serves the same Runner over HTTP, with a stream for each Run and each Session. A service that wants another transport maps the same Runner itself.

## A chat bot on three pods

[FEATURES.md → F9: Many instances](../FEATURES.md#f9-many-instances) lists every behavior above. The chat-bot example, which runs three pods on a local cluster through a rolling deploy, a killed writer and a scale-down, is in [`examples/larkbot`](../examples/larkbot/README.md) and described in [Example: a chat bot](example-chat-bot.md).

Known gap: there is no de-duplication across pods. `examples/larkbot` keeps an in-memory set of message IDs on each instance and drops a repeat it has seen; a repeat that lands on another instance is handled as new. Gotato itself does not de-duplicate `RequestID` in 0.2.0, and a message that a Run in flight takes is not in `run_started`: the Record names it by its message ID alone. If you want it, keep the message id in your own shared store.
