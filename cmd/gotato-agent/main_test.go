package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestBlackBoxLocalAgent starts the real gotato-agent process and drives the
// one-stage success criteria over HTTP. It uses the deterministic demo Model,
// so it needs no LLM, API key, database, registry, or broker.
func TestBlackBoxLocalAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("black-box test starts a real process")
	}
	base := startAgent(t)
	client := &http.Client{Timeout: 10 * time.Second}

	// 1 and 2: create a Conversation and settle one Run.
	first := post(t, client, base+"/v1/runs", `{"agent_name":"default","conversation_key":"blackbox","prompt":"hello"}`, http.StatusOK)
	if first["status"] != "completed" || first["final_message"] != "demo response: hello" {
		t.Fatalf("first run = %+v", first)
	}
	if first["agent_generation"].(float64) != 0 {
		t.Fatalf("first generation = %+v", first["agent_generation"])
	}
	conversationID := first["conversation_id"].(string)
	firstAgent := first["agent_id"].(string)

	// 3: observe canonical Events, including one Tool round trip.
	kinds := streamKinds(t, base+"/v1/runs/stream", `{"agent_name":"default","conversation_key":"blackbox","prompt":"use-tool"}`)
	assertOrder(t, kinds, []string{
		"agent_start", "turn_start", "message_start", "message_end",
		"tool_execution_start", "tool_execution_end", "tool_result_committed",
		"turn_end", "agent_end", "result",
	})

	// 4: the Run settled without closing the Agent.
	record := get(t, client, base+"/v1/conversations/"+conversationID, http.StatusOK)
	if record["status"] != "active" || record["live_agent_id"] != firstAgent {
		t.Fatalf("conversation after Run = %+v", record)
	}

	// 5: the same key reaches the same live Agent.
	second := post(t, client, base+"/v1/runs", `{"agent_name":"default","conversation_key":"blackbox","prompt":"again"}`, http.StatusOK)
	if second["agent_id"] != firstAgent || second["agent_generation"].(float64) != 0 {
		t.Fatalf("second run = %+v", second)
	}

	// 6: closing the live Agent stops new Prompts on that Conversation.
	closed := post(t, client, base+"/v1/agents/"+firstAgent+"/close", `{}`, http.StatusOK)
	if closed["status"] != "closed" {
		t.Fatalf("close response = %+v", closed)
	}
	post(t, client, base+"/v1/runs", `{"agent_name":"default","conversation_key":"blackbox","prompt":"after close"}`, http.StatusNotFound)

	// A second Agent definition is routable by name, and an unknown one is
	// rejected rather than silently served by the default.
	demo := post(t, client, base+"/v1/runs", `{"agent_name":"demo","conversation_key":"blackbox-demo","prompt":"hello"}`, http.StatusOK)
	if demo["final_message"] != "demo response: hello" {
		t.Fatalf("second definition = %+v", demo)
	}
	if demo["conversation_id"] == conversationID {
		t.Fatalf("second definition shared the first Conversation: %+v", demo)
	}
	post(t, client, base+"/v1/runs", `{"agent_name":"missing","conversation_key":"blackbox-missing","prompt":"hello"}`, http.StatusBadRequest)

	// 10: drain flips readiness and stops new admission.
	drained := post(t, client, base+"/admin/drain", `{}`, http.StatusOK)
	if drained["status"] != "drained" {
		t.Fatalf("drain response = %+v", drained)
	}
	readiness, err := client.Get(base + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	readiness.Body.Close()
	if readiness.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readiness after drain = %d", readiness.StatusCode)
	}
	post(t, client, base+"/v1/runs", `{"agent_name":"default","conversation_key":"post-drain","prompt":"late"}`, http.StatusServiceUnavailable)
}

func startAgent(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "gotato-agent")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build gotato-agent: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()

	process := exec.Command(binary, "--addr", addr, "--model", "demo")
	process.Stdout = os.Stderr
	process.Stderr = os.Stderr
	if err := process.Start(); err != nil {
		t.Fatalf("start gotato-agent: %v", err)
	}
	t.Cleanup(func() {
		_ = process.Process.Signal(syscall.SIGTERM)
		exited := make(chan error, 1)
		go func() { exited <- process.Wait() }()
		select {
		case <-exited:
		case <-time.After(15 * time.Second):
			_ = process.Process.Kill()
			<-exited
		}
	})

	base := "http://" + addr
	deadline := time.Now().Add(20 * time.Second)
	for {
		response, healthErr := http.Get(base + "/healthz")
		if healthErr == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return base
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("gotato-agent never became healthy: %v", healthErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func post(t *testing.T, client *http.Client, url, body string, wantStatus int) map[string]any {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("content-type", "application/json")
	return send(t, client, request, wantStatus)
}

func get(t *testing.T, client *http.Client, url string, wantStatus int) map[string]any {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return send(t, client, request, wantStatus)
}

func send(t *testing.T, client *http.Client, request *http.Request, wantStatus int) map[string]any {
	t.Helper()
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s = %d, want %d: %s", request.Method, request.URL.Path, response.StatusCode, wantStatus, raw)
	}
	decoded := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("%s %s body: %v (%s)", request.Method, request.URL.Path, err, raw)
		}
	}
	return decoded
}

func streamKinds(t *testing.T, url, body string) []string {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("content-type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var kinds []string
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		if kind, ok := strings.CutPrefix(scanner.Text(), "event: "); ok {
			kinds = append(kinds, kind)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return kinds
}

// assertOrder checks that want appears inside got in the same relative order.
func assertOrder(t *testing.T, got, want []string) {
	t.Helper()
	index := 0
	for _, kind := range got {
		if index < len(want) && kind == want[index] {
			index++
		}
	}
	if index != len(want) {
		t.Fatalf("event order missing %q\nobserved: %v", want[index], got)
	}
}
