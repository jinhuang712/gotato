package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
)

// hangingModel streams nothing until its call ends, as a slow provider does.
type hangingModel struct{}

func (hangingModel) Stream(context.Context, gotato.ModelRequest) (gotato.ModelStream, error) {
	return hangingStream{}, nil
}

type hangingStream struct{}

func (hangingStream) Recv(ctx context.Context) (gotato.ModelEvent, error) {
	<-ctx.Done()
	return gotato.ModelEvent{}, ctx.Err()
}

func (hangingStream) Close() error { return nil }

// A client that disconnects mid-stream still gets its Run settled and saved
// (GOALS.md G-G02).
func TestStreamDisconnectSettlesAndSavesTheRun(t *testing.T) {
	runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{{Name: "slow", Model: hangingModel{}}}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(runner))
	t.Cleanup(server.Close)
	ctx := context.Background()
	s, err := runner.CreateSession(ctx, "slow", nil)
	if err != nil {
		t.Fatal(err)
	}

	clientCtx, disconnect := context.WithCancel(ctx)
	defer disconnect()
	request, err := http.NewRequestWithContext(clientCtx, http.MethodPost, server.URL+"/v1/sessions/"+s.ID()+"/runs/stream", bytes.NewReader([]byte(`{"prompt":"hello?"}`)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(response.Body)
	for lines.Scan() {
		if strings.TrimSpace(lines.Text()) == "event: context_built" {
			break
		}
	}
	disconnect()
	_ = response.Body.Close()

	deadline := time.Now().Add(5 * time.Second)
	for {
		view, err := runner.Record(ctx, s.ID())
		if err != nil {
			t.Fatal(err)
		}
		runs := view.Runs()
		if len(runs) == 1 && runs[0].Status != gotato.RunRunning && runner.ActiveRuns() == 0 {
			if runs[0].Status != gotato.RunCanceled {
				t.Fatalf("run = %+v, want canceled", runs[0])
			}
			if messages := view.Messages(); len(messages) != 1 || gotato.TextOf(messages[0]) != "hello?" {
				t.Fatalf("messages = %+v, want the prompt kept", messages)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the Run did not settle and save after the client left: runs %+v, active %d", runs, runner.ActiveRuns())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// gatedModel holds its reply until its gate opens.
type gatedModel struct {
	gate, started chan struct{}
}

func (m gatedModel) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	close(m.started)
	select {
	case <-m.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return hangingModel{}.Stream(ctx, request)
}

// A Run on a Session that runs on another instance waits, then answers 409
// with Retry-After.
func TestBusyOnAnotherInstanceAnswers409WithRetryAfter(t *testing.T) {
	store := session.NewMemoryStore()
	held := gatedModel{gate: make(chan struct{}), started: make(chan struct{})}
	holder, err := service.New(service.Config{Store: store, Instance: "instance-a", Agents: []service.Agent{{Name: "slow", Model: held}}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.New(service.Config{Store: store, Instance: "instance-b", Liveness: service.Liveness{Wait: 100 * time.Millisecond}, Agents: []service.Agent{{Name: "slow", Model: hangingModel{}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		close(held.gate)
		_ = holder.Close(context.Background())
		_ = other.Close(context.Background())
	})
	ctx := context.Background()
	s, err := holder.CreateSession(ctx, "slow", nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_, _ = holder.Run(runCtx, service.RunRequest{SessionID: s.ID(), Prompt: "slow"})
	}()
	<-held.started

	server := httptest.NewServer(httpapi.New(other))
	t.Cleanup(server.Close)
	response, err := http.Post(server.URL+"/v1/sessions/"+s.ID()+"/runs", "application/json", strings.NewReader(`{"prompt":"meanwhile"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusConflict || response.Header.Get("Retry-After") == "" {
		t.Fatalf("status = %d, Retry-After = %q; want 409 with Retry-After", response.StatusCode, response.Header.Get("Retry-After"))
	}
}
