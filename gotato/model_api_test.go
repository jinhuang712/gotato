package gotato_test

import (
	"context"
	"math"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

func TestWithSamplingReachesEveryModelCall(t *testing.T) {
	temperature := 0.2
	model := testkit.NewFakeModel(testkit.Text("done"))
	agent, err := gotato.NewSession(
		context.Background(),
		gotato.WithModel(model),
		gotato.WithSampling(gotato.Sampling{Temperature: &temperature, MaxTokens: 256, ReasoningEffort: "low"}),
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
	options := request.Sampling
	if options.Temperature == nil || *options.Temperature != 0.2 || options.MaxTokens != 256 || options.ReasoningEffort != "low" {
		t.Fatalf("options = %+v (temperature %v)", options, options.Temperature)
	}
}

// sampledModel is a Model whose own Config carries default Sampling, and
// wrapper embeds it: a wrapper that embeds a Model forwards Config for free.
type sampledModel struct{ *testkit.FakeModel }

func (sampledModel) Config() gotato.ModelConfig {
	temperature := 0.7
	return gotato.ModelConfig{Name: "m", Sampling: gotato.Sampling{Temperature: &temperature, MaxTokens: 1000, ReasoningEffort: "medium"}}
}

type wrapper struct{ gotato.Model }

// The Model's Config.Sampling holds the defaults, and WithSampling overrides
// them field by field: a field it leaves unset keeps the default, even when
// the Model sits behind a wrapper.
func TestWithSamplingOverridesTheModelsDefaultsFieldByField(t *testing.T) {
	for name, wrap := range map[string]func(gotato.Model) gotato.Model{
		"bare":    func(m gotato.Model) gotato.Model { return m },
		"wrapped": func(m gotato.Model) gotato.Model { return wrapper{m} },
	} {
		t.Run(name, func(t *testing.T) {
			fake := testkit.NewFakeModel(testkit.Text("done"))
			override := 0.1
			session, err := gotato.NewSession(context.Background(),
				gotato.WithModel(wrap(sampledModel{fake})),
				gotato.WithSampling(gotato.Sampling{Temperature: &override, MaxTokens: 64}))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close(context.Background()) }()
			if _, err := session.Prompt(context.Background(), gotato.UserMessage("hi")); err != nil {
				t.Fatal(err)
			}
			request, _ := fake.LastRequest()
			got := request.Sampling
			if got.Temperature == nil || *got.Temperature != 0.1 || got.MaxTokens != 64 || got.ReasoningEffort != "medium" {
				t.Fatalf("Sampling = %+v (temperature %v), want temperature 0.1, 64 tokens, effort medium from the default", got, got.Temperature)
			}
		})
	}
}

func TestAModelWithNoConfigSendsNoDefaults(t *testing.T) {
	fake := testkit.NewFakeModel(testkit.Text("done"))
	if config := fake.Config(); config.Facts.Window.Stated() || config.Facts.Window.Tokens() != 0 || config.Sampling.MaxTokens != 0 {
		t.Fatalf("a hand-written Model states nothing, got %+v", config)
	}
	session, err := gotato.NewSession(context.Background(), gotato.WithModel(fake))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	if _, err := session.Prompt(context.Background(), gotato.UserMessage("hi")); err != nil {
		t.Fatal(err)
	}
	if request, _ := fake.LastRequest(); request.Sampling != (gotato.Sampling{}) {
		t.Fatalf("Sampling = %+v, want none", request.Sampling)
	}
}

type panickingConfig struct{ *testkit.FakeModel }

func (panickingConfig) Config() gotato.ModelConfig { panic("config exploded") }

func TestAModelWhoseConfigPanicsFailsNewSessionInsteadOfCrashing(t *testing.T) {
	_, err := gotato.NewSession(context.Background(), gotato.WithModel(panickingConfig{testkit.NewFakeModel()}))
	if !gotato.IsCode(err, gotato.CodeInvalidArgument) {
		t.Fatalf("err = %v, want invalid_argument", err)
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

func TestWithSamplingRejectsNegativeTemperature(t *testing.T) {
	temperature := -1.0
	_, err := gotato.NewSession(
		context.Background(),
		gotato.WithModel(testkit.NewFakeModel(testkit.Text("done"))),
		gotato.WithSampling(gotato.Sampling{Temperature: &temperature}),
	)
	if err == nil {
		t.Fatal("NewSession accepted a negative temperature")
	}
}

func TestWithSamplingRefusesANonFiniteTemperature(t *testing.T) {
	for name, value := range map[string]float64{"nan": math.NaN(), "inf": math.Inf(1)} {
		t.Run(name, func(t *testing.T) {
			temperature := value
			agent, err := gotato.NewSession(context.Background(), gotato.WithModel(testkit.EchoModel{}),
				gotato.WithSampling(gotato.Sampling{Temperature: &temperature}))
			if err == nil {
				_ = agent.Close(context.Background())
				t.Fatalf("WithSampling accepted temperature %v", value)
			}
		})
	}
}
