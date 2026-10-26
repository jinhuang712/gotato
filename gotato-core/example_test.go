package gotato_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"

	"github.com/jinhuang712/gotato/gateway"
	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/test/fakegateway"
	"github.com/jinhuang712/gotato/testkit"
	"github.com/jinhuang712/gotato/toolregistry"
)

// A Session needs only a Model. Prompt returns when the Run settles.
func Example() {
	s, err := gotato.NewSession(
		gotato.WithModel(testkit.EchoModel{}), // any gotato.Model; providers live in gateway/
		gotato.WithInstruction("You are a helpful assistant."),
	)
	if err != nil {
		panic(err)
	}
	defer func() { _ = s.Close(context.Background()) }()

	result, err := s.Prompt(context.Background(), gotato.UserMessage("hello"))
	if err != nil {
		panic(err)
	}
	fmt.Println(gotato.TextOf(*result.FinalMessage))
	// Output: echo: hello
}

// A tool is a Go function. The model asks for it, the Session runs it and
// goes on until the model answers.
func ExampleNewFuncTool() {
	weather, err := gotato.NewFuncTool("weather", "Returns the weather of a city.", func(_ context.Context, in struct {
		City string `json:"city" description:"the city to look up"`
	}) (string, error) {
		return "sunny in " + in.City, nil
	})
	if err != nil {
		panic(err)
	}
	s, err := gotato.NewSession(
		gotato.WithModel(testkit.DemoModel{}), // a scripted model that calls testkit.DemoToolID
		gotato.WithToolSource(toolregistry.MustNew(weather, testkit.DemoEchoTool())),
	)
	if err != nil {
		panic(err)
	}
	defer func() { _ = s.Close(context.Background()) }()

	result, err := s.Prompt(context.Background(), gotato.UserMessage("use-tool"))
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Status, result.Metrics.ToolCalls)
	// Output: completed 1
}

// Every step of a Run is an Event, a Go type. Subscribe before Prompt.
func ExampleSession_SubscribeRun() {
	s, err := gotato.NewSession(gotato.WithModel(testkit.EchoModel{}))
	if err != nil {
		panic(err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	ctx := context.Background()

	sub, _ := s.SubscribeRun(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
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
	if _, err := s.Prompt(ctx, gotato.UserMessage("hello")); err != nil {
		panic(err)
	}
	<-done
	// Output:
	// echo: hello
	// status: completed
}

// A real model through the gateway, and a tool. The README shows this code with
// an Anthropic key; here the endpoint is a fake gateway that speaks the same
// wire API, so the test needs no key and no network.
func Example_gatewayAndTool() {
	fake := httptest.NewServer(fakegateway.New())
	defer fake.Close()

	// 1. A model. A provider is configuration: a wire API, a model and a key.
	model, err := gateway.New(gateway.Config{
		API:     gateway.APIAnthropicMessages, // or OpenAI Chat Completions, or Responses
		Model:   "claude-sonnet-5-5",
		APIKey:  "test-key", // the README reads it from the environment
		BaseURL: fake.URL,   // the README leaves this out
	})
	if err != nil {
		panic(err)
	}
	// 2. A tool is a Go function. Its input type becomes the schema the model sees.
	weather, err := gotato.NewFuncTool("weather", "Returns the weather of a city.", func(_ context.Context, in struct {
		City string `json:"city" description:"the city to look up"`
	}) (string, error) {
		return "sunny in " + in.City, nil
	})
	if err != nil {
		panic(err)
	}
	// 3. A Session is one conversation: the model, an instruction and the tools.
	s, err := gotato.NewSession(
		gotato.WithModel(model),
		gotato.WithInstruction("You are a helpful assistant."),
		gotato.WithToolSource(toolregistry.MustNew(weather)),
	)
	if err != nil {
		panic(err)
	}
	defer func() { _ = s.Close(context.Background()) }()

	// 4. Ask. Prompt runs the loop (model, tools, model, ...) and returns the final reply.
	// "use-tool" makes the fake gateway call the first tool; a real model decides for itself.
	result, err := s.Prompt(context.Background(), gotato.UserMessage("use-tool"))
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Status, result.Metrics.ToolCalls, strings.HasPrefix(gotato.TextOf(*result.FinalMessage), "tool said: sunny in"))
	// Output: completed 1 true
}
