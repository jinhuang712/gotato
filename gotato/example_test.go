package gotato_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/fakegateway"
	"github.com/jinhuang712/gotato/model"
	"github.com/jinhuang712/gotato/testkit"
)

// A Session needs only a Model. Prompt returns when the Run settles.
func Example() {
	s, err := gotato.NewSession(
		context.Background(),
		gotato.WithModel(testkit.EchoModel{}), // any gotato.Model; providers live in model/
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
		context.Background(),
		gotato.WithModel(testkit.DemoModel{}), // a scripted model that calls testkit.DemoToolID
		gotato.WithTools(weather, testkit.DemoEchoTool()),
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
	s, err := gotato.NewSession(context.Background(), gotato.WithModel(testkit.EchoModel{}))
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

// A real model through the model bridge, and a tool. The README shows this code with
// an Anthropic key; here the endpoint is a fake endpoint that speaks the same
// wire API, so the test needs no key and no network.
func Example_modelAndTool() {
	fake := httptest.NewServer(fakegateway.New())
	defer fake.Close()

	// 1. A model: a wire API, where to send it, and what the model is.
	m, err := model.New(
		model.AnthropicMessages{}, // or ChatCompletions{}, or Responses{}
		model.Connection{
			Endpoint: fake.URL + "/v1/messages", // the complete URL
			APIKey:   "test-key",                // the README reads it from the environment
		},
		gotato.ModelConfig{
			Name:  "claude-sonnet-5-5",
			Facts: gotato.ModelFacts{Window: gotato.WindowTokens(200_000)},
		},
	)
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
		context.Background(),
		gotato.WithModel(m),
		gotato.WithInstruction("You are a helpful assistant."),
		gotato.WithTools(weather),
	)
	if err != nil {
		panic(err)
	}
	defer func() { _ = s.Close(context.Background()) }()

	// 4. Ask. Prompt runs the loop (model, tools, model, ...) and returns the final reply.
	// "use-tool" makes the fake endpoint call the first tool; a real model decides for itself.
	result, err := s.Prompt(context.Background(), gotato.UserMessage("use-tool"))
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Status, result.Metrics.ToolCalls, strings.HasPrefix(gotato.TextOf(*result.FinalMessage), "tool said: sunny in"))
	// Output: completed 1 true
}
