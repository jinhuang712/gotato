# Run Sessions on many pods

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Run Sessions on many pods

By the end of this page your service runs agent conversations that survive a restart and can be continued by any pod. You need the [first page](../tutorials/getting-started.md), and a place to keep Sessions: a directory (`store.NewFileStore`), or your own `store.Records` and `store.Liveness`, which a `store.Store` holds side by side.

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

This example is run as a test: `ExampleRunner_Run` in [host/run_test.go](../../host/run_test.go).

## Many pods

Pods that share a store run a Session one at a time. A step is written before it is sent, so a crash loses at most the step in flight.

```text
pod A runs session S ──► pod A is removed
next message for S ──► pod B: opens S from the store, takes it over, goes on
```

- **One writer.** While a Run is going on pod A, a message for the same Session that reaches pod B is not run there: pod B says `not_writer` and names pod A. You send it to pod A, or turn on forwarding in the HTTP endpoint.
- **Waiting for the writer** is a plain answer to `not_writer` for one replica: retry the `NotRun` after a back-off until the writer finishes, then pod B takes the Session over. The limit: a long Run holds the second message that long. It was not tried with several instances writing one Session. Gotato does not forward by itself, since it builds no infrastructure ([Decision 024](../../plan/decisions/024-gotato-builds-no-infrastructure.md)).
- **Taking over.** Pod B takes the Session over when pod A's Run has ended, or pod A is gone.
- **Mid-Run messages.** A message sent to a Session while it runs can steer the Run or wait for the next turn. `runner.Send` is the call; [FEATURES.md → F1.5: Steer a Run in flight](../../FEATURES.md#f15-steer-a-run-in-flight) has the rules.

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

## Keys under `gotato.` are the Runner's

Session metadata is yours, except for the keys that start with `host.ReservedPrefix` (`"gotato."`). `CreateSession`, `RunRequest.Metadata` and `RunRequest.Update` refuse such a key with `host.ErrReservedKey` (`invalid_argument`, not run, naming the key) before anything is written, and the HTTP API answers 400. A tool is switched only by `Runner.SetToolActive`, and the cache key only by `Runner.Fork`.

```go
_, err := runner.Run(ctx, host.RunRequest{
    SessionID: id,
    Prompt:    "what is open today?",
    Update:    map[string]string{"gotato.instruction": "Answer in French."}, // refused
})
errors.Is(err, host.ErrReservedKey) // true; gotato.NotRun(err) is true; nothing was written
```

The tests `TestAnInstructionInMetadataIsRefusedAndAnotherAgentIsTheWay` in [host/run_instruction_test.go](../../host/run_instruction_test.go) and `TestAReservedKeyIsRefusedByRunMetadataAndUpdate` in [host/reserved_test.go](../../host/reserved_test.go) pin this.

## Values, not handles

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

## A chat bot on three pods

[FEATURES.md → F9: Many instances](../../FEATURES.md#f9-many-instances) lists every behavior above. The chat-bot example, which runs three pods on a local cluster through a rolling deploy, a killed writer and a scale-down, is in [`examples/larkbot`](../../examples/larkbot/README.md) and described in [Example: a chat bot](../tutorials/a-chat-bot.md).

Known gap: there is no de-duplication across pods. `examples/larkbot` keeps an in-memory set of message IDs on each instance and drops a repeat it has seen; a repeat that lands on another instance is handled as new. Gotato itself does not de-duplicate `RequestID` in 0.2.0, and a message that a Run in flight takes is not in `run_started`: the Record names it by its message ID alone. If you want it, keep the message id in your own shared store.

## See also

- [How the host works inside](../../specs/host.md)
- [Many instances, as the user sees them](../../FEATURES.md#f9-many-instances)
- [Shut down without losing a Run](shut-down-without-losing-a-run.md)
- [Register Agents and jobs](register-agents-and-jobs.md)
