package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// anyAddress allows every forward target, for tests whose store is trusted.
func anyAddress(string) bool { return true }

func newServer(t *testing.T) (*httptest.Server, *service.Runner) {
	t.Helper()
	runner, err := service.New(service.Config{
		Store: session.NewMemoryStore(),
		Agents: []service.Agent{
			{Name: "echo", Model: testkit.EchoModel{}, ModelName: "echo"},
			{Name: "demo", Model: testkit.DemoModel{}, ModelName: "demo", Tools: []gotato.Tool{testkit.DemoEchoTool()}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(runner))
	t.Cleanup(server.Close)
	return server, runner
}

func call(t *testing.T, server *httptest.Server, method, path string, body any) (int, []byte) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, server.URL+path, reader)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out bytes.Buffer
	_, _ = out.ReadFrom(resp.Body)
	return resp.StatusCode, out.Bytes()
}

// newSession creates a Session over HTTP and returns its ID.
func newSession(t *testing.T, server *httptest.Server, agent string) string {
	t.Helper()
	status, body := call(t, server, http.MethodPost, "/v1/sessions", map[string]any{"agent": agent})
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &created); err != nil || status != http.StatusCreated || created.ID == "" {
		t.Fatalf("create session = %d %s (%v)", status, body, err)
	}
	return created.ID
}

// gate answers its first call once released, and tells when it starts.
type gate struct {
	started chan struct{}
	release chan struct{}
}

func (g *gate) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	if len(request.Messages) == 1 {
		g.started <- struct{}{}
		select {
		case <-g.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return testkit.NewFakeModel(testkit.Text("reply to "+strings.TrimSpace(testkit.UserText(request)))).Stream(ctx, request)
}

// instance is one server over a Runner, on a store it shares.
type instance struct {
	server   *httptest.Server
	runner   *service.Runner
	requests atomic.Int64
}

func newInstance(t *testing.T, store session.Store, name string, model gotato.Model, advertise bool, options ...httpapi.Option) *instance {
	t.Helper()
	in := &instance{}
	in.server = httptest.NewUnstartedServer(nil)
	address := ""
	if advertise {
		address = "http://" + in.server.Listener.Addr().String()
	}
	runner, err := service.New(service.Config{
		Store: store, Instance: name, Address: address,
		Liveness: service.Liveness{TTL: 5 * time.Second, Wait: 3 * time.Second},
		Agents:   []service.Agent{{Name: "echo", Model: model}},
	})
	if err != nil {
		t.Fatal(err)
	}
	in.runner = runner
	handler := httpapi.New(runner, options...)
	in.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		in.requests.Add(1)
		handler.ServeHTTP(w, r)
	})
	in.server.Start()
	t.Cleanup(func() {
		in.server.Close()
		_ = runner.Close(context.Background())
	})
	return in
}

func (in *instance) post(t *testing.T, path string, body any, headers ...string) (*http.Response, []byte) {
	t.Helper()
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, in.server.URL+path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out bytes.Buffer
	_, _ = out.ReadFrom(resp.Body)
	return resp, out.Bytes()
}

// running starts a Run on the instance that the model holds, and returns the
// Session and a function that waits for the Run.
func running(t *testing.T, in *instance, g *gate) (string, func() service.RunResponse) {
	t.Helper()
	s, err := in.runner.CreateSession(context.Background(), "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan service.RunResponse, 1)
	go func() {
		result, _ := in.runner.Run(context.Background(), service.RunRequest{SessionID: s.ID(), Prompt: "hello"})
		done <- result
	}()
	select {
	case <-g.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the Run never reached the model")
	}
	return s.ID(), func() service.RunResponse {
		select {
		case result := <-done:
			return result
		case <-time.After(5 * time.Second):
			t.Fatal("the Run did not settle")
			return service.RunResponse{}
		}
	}
}

func newGate() *gate { return &gate{started: make(chan struct{}, 4), release: make(chan struct{})} }
