package workload_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/reftasks"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
)

// taskRunner runs a task with a Liveness TTL that a loaded machine does not
// outrun: a Runner refuses a write once its last renewal is older than 4/5 of
// the TTL. A test that kills an instance does it through faultStore, which
// removes the dead instance's row, so no short TTL is needed to wait it out.
func taskRunner(t *testing.T, store session.Store, task reftasks.Task, instance string) *service.Runner {
	t.Helper()
	runner, err := service.New(service.Config{Store: store, Instance: instance, Liveness: service.Liveness{TTL: 5 * time.Second, Wait: 2 * time.Second}, Agents: []service.Agent{{
		Name: task.Name, Model: task.Model, Instruction: task.Instruction, Tools: task.Tools, Options: task.Options, Limits: task.Limits,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	return runner
}

// play runs a task's prompts on a new Session until one fails, and returns
// the Session's ID.
func play(ctx context.Context, runner *service.Runner, task reftasks.Task) (string, error) {
	s, err := runner.CreateSession(ctx, task.Name, nil, session.WithMessages(task.History...))
	if err != nil {
		return "", err
	}
	for _, prompt := range task.Prompts {
		if _, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: prompt}); err != nil {
			return s.ID(), err
		}
	}
	return s.ID(), nil
}

// stored counts the Messages a store holds, leaving out the answers repair
// adds when a Session is opened.
func stored(messages []gotato.Message) int {
	n := 0
	for _, m := range messages {
		if !strings.HasPrefix(string(m.ID), "repair-") {
			n++
		}
	}
	return n
}

// shape is a Message without what differs between two plays of one task:
// its ID, its usage and its model.
func shape(messages []gotato.Message) string {
	var b strings.Builder
	for _, m := range messages {
		fmt.Fprintf(&b, "%s|%s|%s|%s", m.Role, m.StopReason, gotato.TextOf(m), gotato.ReasoningOf(m))
		for _, call := range m.ToolCalls {
			fmt.Fprintf(&b, "|call %s %s %s", call.ID, call.ToolID, call.Arguments)
		}
		if r := m.ToolResult; r != nil {
			fmt.Fprintf(&b, "|result %s %s %t %s", r.CallID, r.Status, r.Executed, r.SafeError)
		}
		b.WriteString("\n")
	}
	return b.String()
}

var errCrashed = errors.New("the instance died")

// decodeRequests reads the logged requests back into values.
func decodeRequests(t *testing.T, sent [][]byte) []gotato.ModelRequest {
	t.Helper()
	requests := make([]gotato.ModelRequest, len(sent))
	for i, b := range sent {
		if err := json.Unmarshal(b, &requests[i]); err != nil {
			t.Fatal(err)
		}
	}
	return requests
}

func jsonOf(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// requestLog keeps, in order, the bytes of every ModelRequest a model received.
type requestLog struct {
	mu   sync.Mutex
	sent [][]byte
}

func (l *requestLog) add(b []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sent = append(l.sent, b)
}

func (l *requestLog) all() [][]byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([][]byte(nil), l.sent...)
}

// stripCacheControl removes the cache_control marks, which move to the end of
// the request by design, from decoded JSON.
func stripCacheControl(v any) any {
	switch x := v.(type) {
	case map[string]any:
		delete(x, "cache_control")
		for key, value := range x {
			x[key] = stripCacheControl(value)
		}
	case []any:
		for i := range x {
			x[i] = stripCacheControl(x[i])
		}
	}
	return v
}
