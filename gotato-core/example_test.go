package gotato_test

import (
	"context"
	"fmt"

	gotato "github.com/jinhuang712/gotato/gotato-core"
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
