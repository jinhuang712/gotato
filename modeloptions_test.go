package gotato_test

import (
	"context"
	"testing"

	"github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

func TestWithModelOptionsReachesEveryModelCall(t *testing.T) {
	temperature := 0.2
	model := testkit.NewFakeModel(testkit.Text("done"))
	agent, err := gotato.NewAgent(
		gotato.WithModel(model),
		gotato.WithModelOptions(gotato.ModelOptions{Temperature: &temperature, MaxTokens: 256, ReasoningEffort: "low"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	temperature = 0.9 // the Agent keeps its own copy

	if _, err := agent.Prompt(context.Background(), gotato.UserMessage("hi")); err != nil {
		t.Fatal(err)
	}
	request, ok := model.LastRequest()
	if !ok {
		t.Fatal("model received no request")
	}
	options := request.Options
	if options.Temperature == nil || *options.Temperature != 0.2 || options.MaxTokens != 256 || options.ReasoningEffort != "low" {
		t.Fatalf("options = %+v (temperature %v)", options, options.Temperature)
	}
}

func TestWithModelOptionsRejectsNegativeTemperature(t *testing.T) {
	temperature := -1.0
	_, err := gotato.NewAgent(
		gotato.WithModel(testkit.NewFakeModel(testkit.Text("done"))),
		gotato.WithModelOptions(gotato.ModelOptions{Temperature: &temperature}),
	)
	if err == nil {
		t.Fatal("NewAgent accepted a negative temperature")
	}
}
