package gateway

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

func TestProfileStatesTheWindowEitherWay(t *testing.T) {
	conn := Connection{BaseURL: "http://127.0.0.1:1", APIKey: "k"}
	for name, c := range map[string]struct {
		window Window
		want   int
	}{
		"tokens":  {WindowTokens(200000), 200000},
		"unknown": {WindowUnknown(), 0},
	} {
		client, err := NewWithProfile(conn, ModelProfile{Name: "m", Window: c.window})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := client.ContextWindowTokens(); got != c.want {
			t.Errorf("%s: window = %d, want %d", name, got, c.want)
		}
	}
}

func TestProfileWithTheZeroWindowIsRefusedWithTheTwoWays(t *testing.T) {
	conn := Connection{BaseURL: "http://127.0.0.1:1"}
	_, err := NewWithProfile(conn, ModelProfile{Name: "m"})
	if err == nil {
		t.Fatal("zero Window accepted")
	}
	for _, want := range []string{"ModelProfile.Window", "WindowTokens", "WindowUnknown"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	for _, n := range []int{0, -5} {
		if _, err := NewWithProfile(conn, ModelProfile{Name: "m", Window: WindowTokens(n)}); err == nil {
			t.Errorf("WindowTokens(%d) accepted", n)
		}
	}
}

func TestProfileSetRefusesAMissingName(t *testing.T) {
	set := ProfileSet{"big": {Name: "m", Window: WindowUnknown()}}
	if _, err := set.Get("small"); err == nil || !strings.Contains(err.Error(), `"small"`) || !strings.Contains(err.Error(), "big") {
		t.Fatalf("Get missing: %v", err)
	}
	if _, err := set.New(Connection{BaseURL: "http://127.0.0.1:1", Profile: "small"}); err == nil {
		t.Fatal("New with a missing profile succeeded")
	}
	if _, err := set.New(Connection{BaseURL: "http://127.0.0.1:1", Profile: "big"}); err != nil {
		t.Fatal(err)
	}
}

func TestProfileOnAConnectionBehavesAsTheFlatConfig(t *testing.T) {
	var calls atomic.Int32
	server := refusingServer(400, `{"error":{"message":"The request is bigger than we take"}}`, &calls)
	defer server.Close()
	flat, err := New(Config{
		API: APIAnthropicMessages, BaseURL: server.URL, APIKey: "k", Model: "m", MaxRetries: -1,
		Provider: ProviderAnthropic, ContextWindowTokens: 1000, CacheRetention: 5 * time.Minute,
		OverflowPhrases: []string{"bigger than we take"}, NoDropBlock: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	set := ProfileSet{"p": {
		Name: "m", Window: WindowTokens(1000), Provider: ProviderAnthropic, CacheRetention: 5 * time.Minute,
		OverflowPhrases: []string{"bigger than we take"}, NoDropBlock: true,
	}}
	viaProfile, err := set.New(Connection{API: APIAnthropicMessages, BaseURL: server.URL, APIKey: "k", MaxRetries: -1, Profile: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if viaProfile.ContextWindowTokens() != 1000 || viaProfile.provider != ProviderAnthropic || viaProfile.model != "m" {
		t.Errorf("window/provider/model = %d %q %q", viaProfile.ContextWindowTokens(), viaProfile.provider, viaProfile.model)
	}
	if viaProfile.CacheRetention() != flat.CacheRetention() || !reflect.DeepEqual(viaProfile.rules, flat.rules) {
		t.Errorf("rules differ: %+v vs %+v", viaProfile.rules, flat.rules)
	}
	if viaProfile.endpoint != flat.endpoint {
		t.Errorf("endpoint %q vs %q", viaProfile.endpoint, flat.endpoint)
	}
	_, err = viaProfile.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}})
	var gatewayErr *Error
	if !errors.As(err, &gatewayErr) || !gatewayErr.Overflow {
		t.Fatalf("overflow phrase of the profile not applied: %v", err)
	}
}

func TestNewWithAConfigKeepsAnUnstatedWindow(t *testing.T) {
	client, err := New(Config{BaseURL: "http://127.0.0.1:1", Model: "m"})
	if err != nil || client.ContextWindowTokens() != 0 {
		t.Fatalf("New without a window: %v", err)
	}
	if client, err = New(Config{BaseURL: "http://127.0.0.1:1", Model: "m", ContextWindowTokens: 4096}); err != nil || client.ContextWindowTokens() != 4096 {
		t.Fatalf("New with a window: %v", err)
	}
	if _, err = New(Config{BaseURL: "http://127.0.0.1:1", Model: "m", ContextWindowTokens: -1}); err == nil {
		t.Fatal("negative window accepted")
	}
}
