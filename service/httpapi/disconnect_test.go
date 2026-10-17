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

	gotato "github.com/jinhuang712/gotato"
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
		view, err := runner.Session(ctx, s.ID())
		if err != nil {
			t.Fatal(err)
		}
		runs := view.Runs()
		if len(runs) == 1 && runs[0].Status != gotato.RunRunning && runner.ActiveRuns() == 0 {
			if runs[0].Status != gotato.RunCanceled {
				t.Fatalf("run = %+v, want cancelled", runs[0])
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
