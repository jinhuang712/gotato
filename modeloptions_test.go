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
	agent, err := gotato.NewSession(
		gotato.WithModel(model),
		gotato.WithModelOptions(gotato.ModelOptions{Temperature: &temperature, MaxTokens: 256, ReasoningEffort: "low"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	temperature = 0.9 // the Session keeps its own copy

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

func TestWithCacheKeyReachesEveryModelCallAndLeavesThePrefixAlone(t *testing.T) {
	// run plays a two-Turn Run and returns its requests and the prefix hash
	// each Turn's context_built reported.
	run := func(options ...gotato.Option) ([]gotato.ModelRequest, []any) {
		t.Helper()
		model := testkit.NewFakeModel(
			testkit.ToolCalls(gotato.ToolCall{ID: "c1", ToolID: testkit.DemoToolID, Arguments: []byte(`{"value":"v"}`)}),
			testkit.Text("done"),
		)
		recorder := testkit.NewEventRecorder()
		options = append(options, gotato.WithModel(model), gotato.WithTool(testkit.DemoEchoTool()), gotato.WithExtension(recorder))
		agent, err := gotato.NewSession(options...)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = agent.Close(context.Background()) }()
		if _, err := agent.Prompt(context.Background(), gotato.UserMessage("hi")); err != nil {
			t.Fatal(err)
		}
		var prefixes []any
		for _, event := range recorder.Events() {
			if event.Kind == gotato.EventContextBuilt {
				prefixes = append(prefixes, event.Payload["prefix_hash"])
			}
		}
		return model.Requests(), prefixes
	}
	keyed, keyedPrefixes := run(gotato.WithCacheKey("session-key"))
	plain, plainPrefixes := run()
	if len(keyed) != 2 || len(plain) != 2 {
		t.Fatalf("model calls = %d and %d, want 2 each", len(keyed), len(plain))
	}
	for i := range keyed {
		if keyed[i].CacheKey != "session-key" || plain[i].CacheKey != "" {
			t.Fatalf("call %d cache keys = %q and %q", i, keyed[i].CacheKey, plain[i].CacheKey)
		}
	}
	if len(keyedPrefixes) != 2 || keyedPrefixes[0] != plainPrefixes[0] || keyedPrefixes[1] != plainPrefixes[1] {
		t.Fatalf("prefix hashes with a key %v, without %v; the key must not change them", keyedPrefixes, plainPrefixes)
	}
}

func TestWithModelOptionsRejectsNegativeTemperature(t *testing.T) {
	temperature := -1.0
	_, err := gotato.NewSession(
		gotato.WithModel(testkit.NewFakeModel(testkit.Text("done"))),
		gotato.WithModelOptions(gotato.ModelOptions{Temperature: &temperature}),
	)
	if err == nil {
		t.Fatal("NewSession accepted a negative temperature")
	}
}
