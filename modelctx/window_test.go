package modelctx_test

import (
	"context"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gateway"
	gotato "github.com/jinhuang712/gotato/gotato-core"
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
func (m sizedModel) ContextWindow() int            { return m.window }
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
	client, err := gateway.New(gateway.Config{API: gateway.APIAnthropicMessages, Provider: gateway.ProviderAnthropic, BaseURL: "https://gw.example.com", Model: "m", ContextWindow: 200000})
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
