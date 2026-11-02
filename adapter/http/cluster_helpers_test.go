package http_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/internal/reftasks"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/store"
)

// What the cluster tests (cluster_test.go, instances_test.go) share: several
// instances of a Runner over one store, behind a proxy, each serving the HTTP
// API of this package.

// memberRunner runs a task with a Liveness TTL that a loaded machine does not
// outrun: a Runner refuses a write once its last renewal is older than 4/5 of
// the TTL. A test that kills an instance does it through killStore, which
// removes the dead instance's row, so no short TTL is needed to wait it out.
func memberRunner(t *testing.T, st store.Store, task reftasks.Task, instance string) *host.Runner {
	t.Helper()
	runner, err := host.New(withAgents(host.Config{Store: st, Instance: instance, Heartbeat: host.Heartbeat{TTL: 5 * time.Second, Wait: 2 * time.Second}}, []profile.AgentProfile{{
		Name: task.Name, Model: task.Model, Instruction: task.Instruction, Tools: task.Tools, ToolSets: task.ToolSets, Limits: task.Limits,
	}}...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	return runner
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
