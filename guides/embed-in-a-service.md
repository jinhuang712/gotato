# Embed Gotato in a service

By the end of this page your service runs agent conversations that survive a restart and can be continued by any pod. You need the [first page](getting-started.md), and a place to keep Sessions: a directory (`session.NewFileStore`), or your own `session.Store`.

## The Runner

A `service.Runner` runs Sessions for you. It opens a Session's Record from the store, runs the message, writes every step as it happens, and lets the Session go.

```go
runner, err := service.New(service.Config{
    Store:  store, // session.NewFileStore("/var/lib/myservice/sessions"), or your own
    Agents: []service.Agent{{Name: "default", Model: model, Tools: tools}},
})
if err != nil {
    return err
}
defer runner.Close(ctx)

first, err := runner.Run(ctx, service.RunRequest{Prompt: "summarize the open tickets"})
// The same Session goes on, here or on another pod.
second, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "and the urgent ones?"})
fmt.Println(second.FinalText)
```

An Agent is a definition: a model, an instruction, tools, limits. A Session is a conversation that runs it. The Runner keeps the two apart, so one Agent serves many Sessions.

This example is run as a test: `ExampleRunner_Run` in [service/run_test.go](../service/run_test.go).

## Many pods

Pods that share a store run a Session one at a time. A step is written before it is sent, so a crash loses at most the step in flight.

```text
pod A runs session S ──► pod A is removed
next message for S ──► pod B: opens S from the store, takes it over, goes on
```

- **One writer.** While a Run is going on pod A, a message for the same Session that reaches pod B is not run there: pod B says `not_writer` and names pod A. You send it to pod A, or turn on forwarding in the HTTP endpoint.
- **Taking over.** Pod B takes the Session over when pod A's Run has ended, or pod A is gone.
- **Mid-Run messages.** A message sent to a Session while it runs can steer the Run or wait for the next turn. `runner.Send` is the call; [FEATURES.md → Steering a Run](../FEATURES.md#g-f26--steering-a-run-) has the rules.

## Errors say what to do

Every error answers two questions, and HTTP only translates the answers.

```go
out, err := runner.Run(ctx, req)
switch {
case gotato.NotRun(err):
    // refused before it started (draining, full, busy, no such Session): nothing
    // happened, so send it to another pod or ask the user to try again
case errors.Is(err, service.ErrNotPersisted):
    // the Run failed to save (store_failure): the Record does not hold it,
    // though a reply was made (out.FinalText)
case gotato.Retryable(err):
    // it ran; sending the same request again later may succeed
}
```

`Run` returns a response and an error together. A refusal before the Run started is the zero response and an error marked not run; a response with a `SessionID` is a Run that started and settled, with its status, its final text and its usage, whatever `err` is, so read the response before you return on `err != nil`. Pass `RequestID` on the request to find it in the Record (`run_started`) and in the response; Gotato does not look for a repeat of it.

## Shutting down

On SIGTERM, call `runner.Drain(ctx)`: the pod stops taking new Runs, lets each Run finish, save and release its Session, and answers 503 on `/readyz` so the load balancer sends traffic elsewhere. Then `runner.Close(ctx)` removes the pod's row, and other pods take its Sessions over at once. In the maintainer's rolling-deploy runs on a local cluster, no message was lost this way.

## Over HTTP

```go
mux.Handle("/agent/", http.StripPrefix("/agent", httpapi.New(runner)))
```

The handler serves the same Runner over HTTP, with a stream for each Run and each Session. A service that wants another transport maps the same Runner itself.

## A chat bot on three pods

[FEATURES.md → Service: many instances](../FEATURES.md#g-f27--service-many-instances-) lists every behavior above. The chat-bot example, which runs three pods on a local cluster through a rolling deploy, a killed writer and a scale-down, is in [`examples/larkbot`](../examples/larkbot/README.md) and described in [Example: a chat bot](example-chat-bot.md).
