# Getting started

By the end of this page you have run an agent in your own program, with a tool it can call. You need Go 1.24 and the root module (`go get github.com/jinhuang712/gotato@v0.2.0`; see [Install](install.md)). No provider key is needed: the examples run on a fake model, so they behave the same every time.

## 1. A Session and a prompt

A Session is one conversation: a model, an instruction and a loop. `Prompt` runs the loop and returns when the Run settles.

```go
import (
    "github.com/jinhuang712/gotato/gotato"
    "github.com/jinhuang712/gotato/testkit"
)

s, err := gotato.NewSession(
    gotato.WithModel(testkit.EchoModel{}), // any gotato.Model
    gotato.WithInstruction("You are a helpful assistant."),
)
if err != nil {
    return err
}
defer s.Close(ctx)

result, err := s.Prompt(ctx, gotato.UserMessage("hello"))
fmt.Println(gotato.TextOf(*result.FinalMessage)) // echo: hello
```

- `ctx` carries cancellation and the deadline through every model call and tool call.
- A Session keeps its conversation in memory unless you give it a place to write ([Embed Gotato in a service](embed-in-a-service.md)).
- To talk to a real model, use a provider from the `gateway` package (OpenAI Chat Completions, OpenAI Responses, Anthropic Messages). Gotato takes a base URL and a key in a YAML file, so a team can point it at its own gateway.

This example is run as a test: `Example` in [example_test.go](../gotato/example_test.go).

## 2. A tool

A tool is a Go function. Its input type becomes the JSON schema the model sees, and the `description` tags become the field descriptions.

```go
weather, err := gotato.NewFuncTool("weather", "Returns the weather of a city.", func(_ context.Context, in struct {
    City string `json:"city" description:"the city to look up"`
}) (string, error) {
    return "sunny in " + in.City, nil
})

s, err := gotato.NewSession(
    gotato.WithModel(model),
    gotato.WithTools(weather),
)
```

The model asks for the tool, the Session runs it, gives the result back, and the loop goes on until the model answers. Gotato checks the model's arguments against the schema first; arguments that do not fit go back to the model as a failed Tool Result, and it corrects them.

This example is run as a test: `ExampleNewFuncTool` in [example_test.go](../gotato/example_test.go).

## 3. Watch what happens

Every step is an Event, a Go type you switch on. Subscribe before `Prompt` to read the Run as it happens:

```go
sub, _ := s.SubscribeRun(ctx)
go func() {
    for {
        event, err := sub.Next(ctx) // io.EOF once the Run's stream has ended
        if err != nil {
            return
        }
        switch e := event.(type) {
        case gotato.TextDelta:
            fmt.Print(e.Text)
        case gotato.RunEnded:
            fmt.Println("\nstatus:", e.Status)
        }
    }
}()
```

A Session never waits for a subscriber: a slow reader does not slow the Run.

This example is run as a test: `ExampleSession_SubscribeRun` in [example_test.go](../gotato/example_test.go).

## Next

- Keep conversations in a store, and run them on many pods: [Embed Gotato in a service](embed-in-a-service.md).
- What each term means: [GLOSSARY.md](../GLOSSARY.md).
- What is built, and what is not planned: [FEATURES.md](../FEATURES.md).
