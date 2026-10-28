package modelctx_test

import (
	"context"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gateway"
	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/testkit"
)

type sizedModel struct {
	window    int
	retention time.Duration
}

func (sizedModel) Stream(context.Context, gotato.ModelRequest) (gotato.ModelStream, error) {
	return nil, nil
}
func (m sizedModel) ContextWindowTokens() int      { return m.window }
func (m sizedModel) CacheRetention() time.Duration { return m.retention }

func TestWindowOfAndRetentionOf(t *testing.T) {
	model := sizedModel{window: 200000, retention: time.Hour}
	if got := modelctx.WindowOf(model); got != 200000 {
		t.Fatalf("WindowOf = %d", got)
	}
	if got := modelctx.RetentionOf(model); got != time.Hour {
		t.Fatalf("RetentionOf = %s", got)
	}
	if got := modelctx.WindowOf(sizedModel{window: -5}); got != 0 {
		t.Fatalf("a negative window = %d, want 0", got)
	}
	// A Model that does not say has none: the answer is unknown, not an error.
	plain := testkit.EchoModel{}
	if modelctx.WindowOf(plain) != 0 || modelctx.RetentionOf(plain) != 0 {
		t.Fatal("a Model without the methods reported a window or a retention")
	}
	if modelctx.WindowOf(nil) != 0 || modelctx.RetentionOf(nil) != 0 {
		t.Fatal("nil reported a window or a retention")
	}
}

func TestGatewayClientIsAContextWindowerAndCacheRetainer(t *testing.T) {
	client, err := gateway.New(gateway.Config{API: gateway.APIAnthropicMessages, Provider: gateway.ProviderAnthropic, BaseURL: "https://gw.example.com", Model: "m", ContextWindowTokens: 200000})
	if err != nil {
		t.Fatal(err)
	}
	if got := modelctx.WindowOf(client); got != 200000 {
		t.Fatalf("WindowOf = %d", got)
	}
	if got := modelctx.RetentionOf(client); got != time.Hour {
		t.Fatalf("RetentionOf = %s", got)
	}
}

// wrapped hides everything of the Model it wraps but Stream.
type wrapped struct{ gotato.Model }

// forwarding does the same and says what it wraps.
type forwarding struct{ wrapped }

func (f forwarding) Unwrap() gotato.Model { return f.Model }

func TestCapabilitiesFollowUnwrap(t *testing.T) {
	inner := sizedModel{window: 100000, retention: time.Minute}
	if got := modelctx.WindowOf(wrapped{inner}); got != 0 {
		t.Fatalf("a wrapper without Unwrap hides the window: WindowOf = %d, want 0", got)
	}
	twice := forwarding{wrapped{forwarding{wrapped{inner}}}}
	if got := modelctx.WindowOf(twice); got != 100000 {
		t.Fatalf("WindowOf through two wrappers = %d", got)
	}
	if got := modelctx.RetentionOf(twice); got != time.Minute {
		t.Fatalf("RetentionOf through two wrappers = %s", got)
	}
	if modelctx.DropsReasoning(twice) {
		t.Fatal("DropsReasoning true for a Model that is not a RoundReasoner")
	}
}
