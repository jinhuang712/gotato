# Test a Runner and its endpoint

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Test a Runner and its endpoint

By the end of this page you can test a Runner, and its HTTP handler behind `httptest`, with no provider. You need [Test with fakes](test-with-fakes.md) and [Run Sessions on many pods](run-sessions-on-many-pods.md).

## Test the Runner, in process and over HTTP

A `host.Runner` takes the same fakes as an AgentProfile's `Model`, registered in a `profile.Registry`. Test the Runner in process, or put its handler behind `httptest.NewServer` and call it as a client would. The handler is `adapter/http`, a module of its own, imported as `httpapi` because the test imports `net/http` too (`go get github.com/jinhuang712/gotato/adapter/http`).

```go
func TestRunnerWithoutHTTP(t *testing.T) {
	memory := store.NewMemoryStore()
	profiles := profile.NewRegistry(nil)
	_ = profiles.RegisterAgentProfileInline(profile.AgentProfile{Name: "default", Model: testkit.EchoModel{}})
	runner, _ := host.New(host.Config{
		Store:        store.Store{Records: memory, Liveness: memory},
		Profiles:     profiles,
		DefaultAgent: "default",
	})
	defer runner.Close(context.Background())
	out, err := runner.Run(context.Background(), host.RunRequest{Prompt: "hi"})
	if err != nil || out.FinalText != "echo: hi" {
		t.Fatal(out, err)
	}
}
```

```go
func TestARunOverHTTP(t *testing.T) {
	memory := store.NewMemoryStore()
	profiles := profile.NewRegistry(nil)
	if err := profiles.RegisterAgentProfileInline(profile.AgentProfile{Name: "default", Model: testkit.NewFakeModel(testkit.Text("pong"))}); err != nil {
		t.Fatal(err)
	}
	runner, err := host.New(host.Config{
		Store:        store.Store{Records: memory, Liveness: memory},
		Profiles:     profiles,
		DefaultAgent: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	server := httptest.NewServer(httpapi.New(runner))
	defer server.Close()

	// Create a Session, then run a prompt in it.
	resp, err := http.Post(server.URL+"/v1/sessions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var created struct{ ID string }
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()

	resp, err = http.Post(server.URL+"/v1/sessions/"+created.ID+"/runs", "application/json", strings.NewReader(`{"prompt":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		FinalText string `json:"final_text"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 200 || out.FinalText != "pong" {
		t.Fatalf("%d %q", resp.StatusCode, out.FinalText)
	}
}
```

- `go test -race ./...` is the check to run: a handler is concurrent by nature.
- A test of the HTTP wire itself, such as an error table, asserts on the JSON and the `Gotato-Not-Run` and `Gotato-Retryable` headers, not on prose. See [HTTP in five minutes](http-in-five-minutes.md).
- A test of a graceful shutdown starts a Run on a slow model, calls `runner.Drain`, and checks the Run `completed`: [A minimal main](../tutorials/a-minimal-main.md).

## See also

- [HTTP in five minutes](../tutorials/http-in-five-minutes.md)
- [A minimal main, for a test of shutdown](../tutorials/a-minimal-main.md)
