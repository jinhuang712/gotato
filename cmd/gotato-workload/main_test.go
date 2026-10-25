package main

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/service/workload"
)

func runCommand(t *testing.T, args ...string) (int, workload.Report, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, &stdout, &stderr, func(string) string { return "" })
	var report workload.Report
	if stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
			t.Fatalf("stdout is not a report: %v\n%s", err, stdout.String())
		}
	}
	return code, report, stderr.String()
}

func TestFakeRunPrintsTheReport(t *testing.T) {
	code, report, stderr := runCommand(t, "-tasks", "lookup,tool_error", "-plays", "3", "-concurrency", "2")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if report.Model != "fake" || report.Plays != 3 || len(report.Tasks) != 2 || report.Tasks[0].Task != "lookup" {
		t.Fatalf("report = %+v", report)
	}
	for _, task := range report.Tasks {
		if task.Passed != 3 || task.ContextTurns != task.Turns {
			t.Fatalf("%s: %+v", task.Task, task)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"-tasks", "nope"},
		{"-price", "3"},
		{"-price", "3,x"},
		{"-plays", "0"},
		{"extra"},
	} {
		if code, _, _ := runCommand(t, args...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
}

func TestLiveRunNeedsAGatewayConfiguration(t *testing.T) {
	code, _, stderr := runCommand(t, "-live", "-gateway-config", filepath.Join(t.TempDir(), "missing.yaml"))
	if code != exitError || !strings.Contains(stderr, "gateway configuration") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

// TestLiveRunAgainstALocalProvider plays the lookup task through the real
// gateway against a local server that speaks the chat completions wire API:
// it asks for orders.lookup, then answers from the tool result, and reports
// usage with cached tokens the way a provider does.
func TestLiveRunAgainstALocalProvider(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		usage := `"usage":{"prompt_tokens":1000,"completion_tokens":20,"total_tokens":1020,"prompt_tokens_details":{"cached_tokens":600}}`
		if request.Messages[len(request.Messages)-1].Role == "tool" {
			_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"Order 42 has shipped."}}]}` + "\n\n"))
			_, _ = w.Write([]byte(`data: {"choices":[{"delta":{},"finish_reason":"stop"}],` + usage + "}\n\n"))
		} else {
			_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"orders_lookup","arguments":"{\"id\":\"42\"}"}}]}}]}` + "\n\n"))
			_, _ = w.Write([]byte(`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],` + usage + "}\n\n"))
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer provider.Close()
	config := filepath.Join(t.TempDir(), "gateway.yaml")
	if err := os.WriteFile(config, []byte("api: openai-chat-completions\nendpoint: "+provider.URL+"\napi_key: test\nmodel: local-model\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, report, stderr := runCommand(t, "-live", "-gateway-config", config, "-tasks", "lookup", "-price", "3,15,0.3")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	lookup := report.Tasks[0]
	if report.Model != "local-model" || lookup.Passed != 1 || lookup.ToolCalls != 1 || lookup.Usage.InputTokens != 2000 || lookup.Usage.CacheReadTokens != 1200 {
		t.Fatalf("report = %+v", report)
	}
	// 800 uncached input tokens at $3, 1,200 cached at $0.30 and 40 output
	// tokens at $15, per million.
	if want := (800*3 + 1200*0.3 + 40*15) / 1e6; math.Abs(lookup.CostUSD-want) > 1e-12 {
		t.Fatalf("cost = %v, want %v", lookup.CostUSD, want)
	}
	if lookup.CacheHitRate == nil || *lookup.CacheHitRate != 0.6 {
		t.Fatalf("cache hit rate = %v", lookup.CacheHitRate)
	}
	if lookup.Time.ModelMS <= 0 || lookup.ContextMismatches != 0 {
		t.Fatalf("lookup = %+v", lookup)
	}
}

func TestRecapCheckPrintsTheShareBeforeAndAfter(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"-recap-check", "-min-share", "0.8"}, &stdout, &stderr, func(string) string { return "" })
	var report workload.RecapReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("stdout is not a report: %v\n%s", err, stdout.String())
	}
	if code != exitOK || report.Check != "payments" || report.Questions != 5 || report.Before.Share != 1 || report.After.Share != 1 || !report.RecapWritten {
		t.Fatalf("exit %d, report = %+v\n%s", code, report, stderr.String())
	}
	if !strings.Contains(stderr.String(), "answered 5 of 5 before the compaction, 5 of 5 after it") {
		t.Errorf("stderr = %s", stderr.String())
	}
	if code, _, _ := runCommand(t, "-recap-check", "-min-share", "2"); code != exitUsage {
		t.Errorf("-min-share 2 exits %d, want usage", code)
	}
}
