package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// sent is what one request to a recording server carried.
type sent struct {
	header http.Header
	body   map[string]json.RawMessage
}

// recordingServer keeps every request and answers with a finished chat
// stream. The other wire APIs fail to parse it, which the tests ignore: they
// look only at what was sent.
func recordingServer(t *testing.T) (*httptest.Server, func() []sent) {
	t.Helper()
	var mu sync.Mutex
	var requests []sent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var body map[string]json.RawMessage
		if err := json.Unmarshal(data, &body); err != nil {
			t.Errorf("request body is not a JSON object: %v: %s", err, data)
		}
		mu.Lock()
		requests = append(requests, sent{header: r.Header.Clone(), body: body})
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	t.Cleanup(server.Close)
	return server, func() []sent {
		mu.Lock()
		defer mu.Unlock()
		return append([]sent(nil), requests...)
	}
}

func TestCacheKeyIsSentAsConfigured(t *testing.T) {
	for _, tc := range []struct {
		name      string
		config    Config
		key       string
		wantField string // the body field that carries the key, or ""
		wantHead  string // the header that carries the key, or ""
	}{
		{"responses sends prompt_cache_key by default", Config{API: APIResponses}, "k1", "prompt_cache_key", ""},
		{"responses with the field turned off", Config{API: APIResponses, CacheKeyField: "none"}, "k1", "", ""},
		{"responses with a header too", Config{API: APIResponses, CacheKeyHeader: "session-id"}, "k1", "prompt_cache_key", "session-id"},
		{"chat sends nothing by default", Config{}, "k1", "", ""},
		{"chat with a configured field", Config{CacheKeyField: "prompt_cache_key"}, "k1", "prompt_cache_key", ""},
		{"chat with a header", Config{CacheKeyHeader: "session-id"}, "k1", "", "session-id"},
		{"anthropic with a header", Config{API: APIAnthropicMessages, CacheKeyHeader: "x-session-affinity"}, "k1", "", "x-session-affinity"},
		{"no key sends nothing", Config{API: APIResponses, CacheKeyHeader: "session-id"}, "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, requests := recordingServer(t)
			config := tc.config
			config.Endpoint, config.APIKey, config.Model, config.MaxRetries = server.URL, "secret", "m", -1
			client, err := New(config)
			if err != nil {
				t.Fatal(err)
			}
			stream, err := client.Stream(context.Background(), gotato.ModelRequest{Messages: []gotato.Message{gotato.UserMessage("hi")}, CacheKey: tc.key})
			if err != nil {
				t.Fatal(err)
			}
			drain(t, stream)
			got := requests()
			if len(got) != 1 {
				t.Fatalf("%d requests, want 1", len(got))
			}
			request := got[0]
			if model := string(request.body["model"]); model != `"m"` {
				t.Fatalf("model = %s; the rest of the body must stay as encoded", model)
			}
			for _, field := range []string{"prompt_cache_key"} {
				value, ok := request.body[field]
				switch {
				case field == tc.wantField && string(value) != `"`+tc.key+`"`:
					t.Fatalf("body field %s = %s, want %q", field, value, tc.key)
				case field != tc.wantField && ok:
					t.Fatalf("body carries %s = %s, want no such field", field, value)
				}
			}
			for _, header := range []string{"session-id", "x-session-affinity"} {
				value := request.header.Get(header)
				switch {
				case header == tc.wantHead && value != tc.key:
					t.Fatalf("header %s = %q, want %q", header, value, tc.key)
				case header != tc.wantHead && value != "":
					t.Fatalf("header %s = %q, want none", header, value)
				}
			}
		})
	}
}

func TestCacheKeyFieldIsRefusedWhereTheWireAPIHasNone(t *testing.T) {
	_, err := New(Config{API: APIAnthropicMessages, Model: "m", CacheKeyField: "prompt_cache_key"})
	if err == nil || !strings.Contains(err.Error(), "cache_control") {
		t.Fatalf("err = %v, want a refusal that points to cache_control", err)
	}
	if _, err := New(Config{BaseURL: "https://example.com", Model: "m", CacheKeyHeader: "session id"}); err == nil {
		t.Fatal("New accepted a header name with a space")
	}
}

func TestCacheKeyHeaderSurvivesARestart(t *testing.T) {
	var calls atomic.Int32
	var mu sync.Mutex
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("session-id"))
		mu.Unlock()
		if calls.Add(1) == 1 {
			dropConnection(t, w, roleChunk)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL, Model: "m", CacheKeyHeader: "session-id", MaxRetries: 2, RetryBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Stream(context.Background(), gotato.ModelRequest{CacheKey: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	if text := textOf(drain(t, stream)); text != "ok" {
		t.Fatalf("text = %q, want the restarted stream's", text)
	}
	if len(keys) != 2 || keys[0] != "k1" || keys[1] != "k1" {
		t.Fatalf("session-id per attempt = %q, want k1 on both", keys)
	}
}

func TestParseYAMLCacheKey(t *testing.T) {
	config, err := parseYAML([]byte(`
api: openai-responses
model: m
api_key: secret
cache_key_header: session-id
cache_key_field: none
`))
	if err != nil {
		t.Fatal(err)
	}
	if config.CacheKeyHeader != "session-id" || config.CacheKeyField != "none" {
		t.Fatalf("config = %+v", config)
	}
}

// A CacheKeyField that names a field the wire API already sends would replace
// it in the request, such as the model: New refuses it, and the encoder
// refuses it again for a Client built by hand.
func TestCacheKeyFieldRefusesAFieldTheRequestAlreadySends(t *testing.T) {
	server, _ := recordingServer(t)
	reserved := map[API][]string{
		APIChatCompletions: {"model", "stream", "messages", "tools", "stream_options", "temperature", "max_tokens", "tool_choice", "Model"},
		APIResponses:       {"model", "stream", "input", "store", "instructions", "include", "tool_choice", "reasoning", "prompt_cache_options", "max_output_tokens"},
	}
	for api, names := range reserved {
		for _, name := range names {
			_, err := New(Config{API: api, BaseURL: server.URL, APIKey: "k", Model: "expected-model", CacheKeyField: name})
			if err == nil || !strings.Contains(err.Error(), "CacheKeyField") {
				t.Errorf("%s: CacheKeyField %q: err = %v, want a refusal", api, name, err)
			}
		}
	}
	// The names a cache key may use stay allowed.
	for api, name := range map[API]string{APIChatCompletions: "prompt_cache_key", APIResponses: "prompt_cache_key"} {
		if _, err := New(Config{API: api, BaseURL: server.URL, APIKey: "k", Model: "m", CacheKeyField: name}); err != nil {
			t.Errorf("%s: %q refused: %v", api, name, err)
		}
	}

	client := &Client{api: APIChatCompletions, cacheKeyField: "model"}
	if body, err := client.withCacheKeyField([]byte(`{"model":"m"}`), "session-key"); err == nil {
		t.Fatalf("encode accepted a reserved field: %s", body)
	}
}
