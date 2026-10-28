package gotato_test

import (
	"context"
	"math"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

func TestWithModelConfigReachesEveryModelCall(t *testing.T) {
	temperature := 0.2
	model := testkit.NewFakeModel(testkit.Text("done"))
	agent, err := gotato.NewSession(
		context.Background(),
		gotato.WithModel(model),
		gotato.WithModelConfig(gotato.ModelConfig{Temperature: &temperature, MaxTokens: 256, ReasoningEffort: "low"}),
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
	options := request.Config
	if options.Temperature == nil || *options.Temperature != 0.2 || options.MaxTokens != 256 || options.ReasoningEffort != "low" {
		t.Fatalf("options = %+v (temperature %v)", options, options.Temperature)
	}
}

func TestWithCacheKeyReachesEveryModelCallAndLeavesThePrefixAlone(t *testing.T) {
	// run plays a two-Turn Run and returns its requests and the prefix hash
	// each Turn's context_built reported.
	run := func(options ...gotato.SessionOption) ([]gotato.ModelRequest, []string) {
		t.Helper()
		model := testkit.NewFakeModel(
			testkit.ToolCalls(gotato.ToolCall{ID: "c1", ToolID: testkit.DemoToolID, Arguments: []byte(`{"value":"v"}`)}),
			testkit.Text("done"),
		)
		options = append(options, gotato.WithModel(model), gotato.WithTools(testkit.DemoEchoTool()))
		agent, err := gotato.NewSession(context.Background(), options...)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = agent.Close(context.Background()) }()
		recorder := testkit.RecordEvents(agent)
		if _, err := agent.Prompt(context.Background(), gotato.UserMessage("hi")); err != nil {
			t.Fatal(err)
		}
		var prefixes []string
		for _, event := range recorder.OfKind(gotato.EventKindContextBuilt) {
			prefixes = append(prefixes, event.(gotato.ContextBuilt).PrefixHash)
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

func TestWithModelConfigRejectsNegativeTemperature(t *testing.T) {
	temperature := -1.0
	_, err := gotato.NewSession(
		context.Background(),
		gotato.WithModel(testkit.NewFakeModel(testkit.Text("done"))),
		gotato.WithModelConfig(gotato.ModelConfig{Temperature: &temperature}),
	)
	if err == nil {
		t.Fatal("NewSession accepted a negative temperature")
	}
}

func TestWithModelConfigRefusesANonFiniteTemperature(t *testing.T) {
	for name, value := range map[string]float64{"nan": math.NaN(), "inf": math.Inf(1)} {
		t.Run(name, func(t *testing.T) {
			temperature := value
			agent, err := gotato.NewSession(context.Background(), gotato.WithModel(testkit.EchoModel{}),
				gotato.WithModelConfig(gotato.ModelConfig{Temperature: &temperature}))
			if err == nil {
				_ = agent.Close(context.Background())
				t.Fatalf("WithModelConfig accepted temperature %v", value)
			}
		})
	}
}
