package gotato_test

import (
	"encoding/json"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
)

func TestWindowIsTextThatRoundTrips(t *testing.T) {
	for _, tc := range []struct {
		window gotato.Window
		text   string
		tokens int
		stated bool
	}{
		{gotato.WindowTokens(200000), "200000", 200000, true},
		{gotato.WindowUnknown(), "unknown", 0, true},
		{gotato.Window{}, "", 0, false},
	} {
		text, err := tc.window.MarshalText()
		if err != nil || string(text) != tc.text {
			t.Errorf("MarshalText = %q, %v, want %q", text, err, tc.text)
		}
		var back gotato.Window
		if err := back.UnmarshalText(text); err != nil || back != tc.window {
			t.Errorf("UnmarshalText(%q) = %+v, %v, want %+v", text, back, err, tc.window)
		}
		if back.Tokens() != tc.tokens || back.Stated() != tc.stated {
			t.Errorf("%q: tokens %d stated %v, want %d %v", tc.text, back.Tokens(), back.Stated(), tc.tokens, tc.stated)
		}
	}
	var w gotato.Window
	if err := w.UnmarshalText([]byte(" Unknown ")); err != nil || !w.Stated() || w.Tokens() != 0 {
		t.Errorf("Unknown in any case: %+v, %v", w, err)
	}
	for _, bad := range []string{"0", "-5", "12k", "none", "1.5"} {
		if err := new(gotato.Window).UnmarshalText([]byte(bad)); err == nil {
			t.Errorf("UnmarshalText(%q) accepted", bad)
		}
	}
}

func TestWindowChecksItsOwnNumber(t *testing.T) {
	if err := gotato.WindowTokens(0).Valid(); err == nil {
		t.Error("a window of 0 tokens is valid")
	}
	if err := gotato.WindowTokens(-3).Valid(); err == nil {
		t.Error("a window of -3 tokens is valid")
	}
	for _, w := range []gotato.Window{gotato.WindowTokens(1), gotato.WindowUnknown(), {}} {
		if err := w.Valid(); err != nil {
			t.Errorf("%+v: %v", w, err)
		}
	}
	if gotato.WindowTokens(-3).Tokens() != 0 {
		t.Error("a negative window reads as tokens")
	}
}

func TestModelConfigWritesAsJSON(t *testing.T) {
	temperature := 0.5
	config := gotato.ModelConfig{
		Name:     "m",
		Facts:    gotato.ModelFacts{Window: gotato.WindowTokens(8192), RoundReasoning: true},
		Sampling: gotato.Sampling{Temperature: &temperature},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"name":"m","facts":{"window":"8192","round_reasoning":true},"sampling":{"temperature":0.5}}`
	if string(data) != want {
		t.Fatalf("JSON = %s, want %s", data, want)
	}
	var back gotato.ModelConfig
	if err := json.Unmarshal(data, &back); err != nil || back.Facts.Window.Tokens() != 8192 || !back.Facts.RoundReasoning || *back.Sampling.Temperature != 0.5 {
		t.Fatalf("back = %+v, %v", back, err)
	}
	if empty, _ := json.Marshal(gotato.ModelConfig{}); string(empty) != `{}` {
		t.Fatalf("the zero ModelConfig is %s", empty)
	}
}
