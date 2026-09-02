package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/orchestration"
)

// gatedModel holds a Run open until release is closed. It records whether the
// Run Context was cancelled so a test can tell a disconnect that cancelled the
// Run from one that only stopped delivery.
type gatedModel struct {
	release   chan struct{}
	cancelled chan struct{}
	once      sync.Once
}

func newGatedModel() *gatedModel {
	return &gatedModel{release: make(chan struct{}), cancelled: make(chan struct{})}
}

func (m *gatedModel) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	return &gatedStream{model: m}, nil
}

type gatedStream struct {
	model *gatedModel
	done  bool
}

func (s *gatedStream) Recv(ctx context.Context) (gotato.ModelEvent, error) {
	if s.done {
		return gotato.ModelEvent{}, context.Canceled
	}
	select {
	case <-s.model.release:
		s.done = true
		return gotato.ModelEvent{Kind: gotato.ModelDone, StopReason: gotato.StopEndTurn}, nil
	case <-ctx.Done():
		s.model.once.Do(func() { close(s.model.cancelled) })
		return gotato.ModelEvent{}, ctx.Err()
	}
}

func (s *gatedStream) Close() error { return nil }

// openStreamUntilFirstEvent starts an SSE Run and returns once the first event
// arrives, together with the cancel function that plays the client disconnect.
func openStreamUntilFirstEvent(t *testing.T, url, body string) (*http.Response, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	request.Header.Set("content-type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	reader := bufio.NewReader(response.Body)
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			cancel()
			response.Body.Close()
			t.Fatal(readErr)
		}
		if strings.HasPrefix(line, "data: ") {
			return response, cancel
		}
	}
}

func TestHTTPStreamDisconnectStopsDeliveryOnly(t *testing.T) {
	model := newGatedModel()
	hostServer, server := newTestHTTPServerWithModel(t, model)
	defer server.Close()

	response, disconnect := openStreamUntilFirstEvent(t, server.URL+"/v1/runs/stream",
		`{"agent_name":"default","conversation_key":"disconnect-test","prompt":"stream"}`)
	record := getConversationByKey(t, hostServer, "disconnect-test")
	liveAgent := record.LiveAgentID

	disconnect()
	response.Body.Close()

	// The default policy detaches delivery without cancelling the Run, so the
	// Model Context must stay alive until the gate opens.
	select {
	case <-model.cancelled:
		t.Fatal("disconnect cancelled the Run under the default policy")
	case <-time.After(50 * time.Millisecond):
	}
	close(model.release)

	waitForAgentStatus(t, hostServer, record.ID, gotato.AgentIdle)
	after, ok := hostServer.Orchestration.Get(record.ID)
	if !ok {
		t.Fatal("conversation disappeared after disconnect")
	}
	if after.Status != orchestration.ConversationActive || after.LiveAgentID != liveAgent {
		t.Fatalf("disconnect changed the route: %+v", after)
	}
	reused := postJSON(t, server.URL+"/v1/runs", `{"agent_name":"default","conversation_key":"disconnect-test","prompt":"again"}`)
	if reused["agent_id"] != string(liveAgent) {
		t.Fatalf("Agent was not reusable after disconnect: %+v", reused)
	}
}

func TestHTTPStreamDisconnectCancelsRunWhenPolicySaysSo(t *testing.T) {
	model := newGatedModel()
	hostServer, server := newTestHTTPServerWithModel(t, model)
	hostServer.CancelRunOnDisconnect = true
	defer server.Close()

	response, disconnect := openStreamUntilFirstEvent(t, server.URL+"/v1/runs/stream",
		`{"agent_name":"default","conversation_key":"disconnect-cancel-test","prompt":"stream"}`)
	record := getConversationByKey(t, hostServer, "disconnect-cancel-test")

	disconnect()
	response.Body.Close()

	select {
	case <-model.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("disconnect did not cancel the Run under the explicit policy")
	}
	waitForAgentStatus(t, hostServer, record.ID, gotato.AgentIdle)
	after, ok := hostServer.Orchestration.Get(record.ID)
	if !ok || after.Status != orchestration.ConversationActive || after.LiveAgentID != record.LiveAgentID {
		t.Fatalf("cancelling the Run also closed the Agent: %+v", after)
	}
}

func TestHTTPDrainReportsIncompleteInsteadOfClaimingClosed(t *testing.T) {
	model := newGatedModel()
	hostServer, server := newTestHTTPServerWithModel(t, model)
	defer server.Close()

	submitted := postJSON(t, server.URL+"/v1/runs/async",
		`{"agent_name":"default","conversation_key":"drain-test","prompt":"busy"}`)
	if submitted["status"] != string(gotato.RunRunning) {
		t.Fatalf("async submit = %+v", submitted)
	}
	conversationID := gotato.ConversationID(submitted["conversation_id"].(string))
	waitForAgentStatus(t, hostServer, conversationID, gotato.AgentBusy)

	drainCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := hostServer.Drain(drainCtx)
	var incomplete *orchestration.DrainIncomplete
	if !errors.As(err, &incomplete) {
		t.Fatalf("drain over a Busy Agent returned %v", err)
	}
	if len(incomplete.Pending) != 1 || incomplete.Pending[0].ConversationID != conversationID {
		t.Fatalf("incomplete drain report = %+v", incomplete.Pending)
	}
	if incomplete.Pending[0].Agent == gotato.AgentClosed {
		t.Fatal("drain reported a Busy Agent as closed")
	}

	readiness, err := http.Get(server.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	readiness.Body.Close()
	if readiness.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readiness after drain = %d", readiness.StatusCode)
	}

	rejected, err := http.Post(server.URL+"/v1/runs", "application/json",
		strings.NewReader(`{"agent_name":"default","conversation_key":"drain-test-2","prompt":"late"}`))
	if err != nil {
		t.Fatal(err)
	}
	rejected.Body.Close()
	if rejected.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("run admitted after drain = %d", rejected.StatusCode)
	}

	close(model.release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		settleCtx, settleCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		drainErr := hostServer.Drain(settleCtx)
		settleCancel()
		if drainErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("drain never completed after the Run settled: %v", drainErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	final, ok := hostServer.Orchestration.Get(conversationID)
	if !ok || final.Status != orchestration.ConversationClosed || final.LiveAgentID != "" {
		t.Fatalf("conversation after completed drain = %+v", final)
	}
}

func getConversationByKey(t *testing.T, server *Server, key string) orchestration.Record {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		agent, record, err := server.Orchestration.Resolve(context.Background(), orchestration.Request{
			AgentName: "default", ConversationKey: gotato.ConversationKey(key),
		})
		if err == nil && agent != nil && record.LiveAgentID != "" {
			return record
		}
		if time.Now().After(deadline) {
			t.Fatalf("conversation %q never became routable: %v", key, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitForAgentStatus(t *testing.T, server *Server, id gotato.ConversationID, want gotato.AgentStatus) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		agent, record := server.Orchestration.LiveAgent(id)
		if agent != nil {
			if lifecycle, ok := agent.(gotato.AgentLifecycle); ok && lifecycle.Status() == want {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("conversation %s never reached %s: %+v", id, want, record)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPollTableIsBounded(t *testing.T) {
	hostServer, server := newTestHTTPServer(t)
	hostServer.RunRetention = 3
	defer server.Close()
	defer hostServer.Drain(context.Background())

	var runIDs []string
	for i := 0; i < 6; i++ {
		submitted := postJSON(t, server.URL+"/v1/runs/async",
			`{"agent_name":"default","conversation_key":"bounded","prompt":"hello"}`)
		runIDs = append(runIDs, submitted["run_id"].(string))
		waitForRunStatus(t, server.URL, submitted["run_id"].(string), string(gotato.RunCompleted))
	}
	if got := hostServer.RetainedRuns(); got != 3 {
		t.Fatalf("retained runs = %d, want 3", got)
	}
	// The newest Runs are still pollable.
	for _, id := range runIDs[3:] {
		response, err := http.Get(server.URL + "/v1/runs/" + id)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("recent run %s poll = %d", id, response.StatusCode)
		}
	}
	// The oldest were reclaimed.
	for _, id := range runIDs[:3] {
		response, err := http.Get(server.URL + "/v1/runs/" + id)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode == http.StatusOK {
			t.Fatalf("old run %s was never reclaimed", id)
		}
	}
}

func TestConversationEndpointCarriesNoTranscript(t *testing.T) {
	hostServer, server := newTestHTTPServer(t)
	defer server.Close()
	defer hostServer.Drain(context.Background())

	first := postJSON(t, server.URL+"/v1/runs",
		`{"agent_name":"default","conversation_key":"opaque","prompt":"secret words"}`)
	conversationID := first["conversation_id"].(string)

	response, err := http.Get(server.URL + "/v1/conversations/" + conversationID)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "secret words") {
		t.Fatalf("conversation endpoint leaked the transcript: %s", body)
	}
}

func waitForRunStatus(t *testing.T, base, runID, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		response, err := http.Get(base + "/v1/runs/" + runID)
		if err != nil {
			t.Fatal(err)
		}
		var polled map[string]any
		decodeErr := json.NewDecoder(response.Body).Decode(&polled)
		response.Body.Close()
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if polled["status"] == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s never reached %s: %+v", runID, want, polled)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
