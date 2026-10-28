package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// hangingToolSet never answers until its context ends.
type hangingToolSet struct{}

func (hangingToolSet) Spec() gotato.ToolSetSpec { return gotato.ToolSetSpec{Name: "slow"} }

func (hangingToolSet) Tools(ctx context.Context) ([]gotato.Tool, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func slowRunner(t *testing.T) (*service.Runner, string) {
	t.Helper()
	agent := service.Agent{
		Name:    "a",
		Model:   testkit.NewFakeModel(testkit.Text("ok")),
		Tools:   []gotato.Tool{testkit.NewFakeTool("own", "x")},
		Options: []gotato.SessionOption{gotato.WithToolSet(hangingToolSet{})},
	}
	runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{agent}, ToolSetTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	s, err := runner.CreateSession(context.Background(), "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	return runner, s.ID()
}

// A ToolSet that does not answer cannot hold up the listing: Tools gives up
// after ToolSetTimeout and still lists the Agent's own tools, with the error.
func TestToolsReturnsWithinTheBoundWhenAToolSetHangs(t *testing.T) {
	runner, id := slowRunner(t)
	start := time.Now()
	entries, err := runner.Tools(context.Background(), "", id)
	if time.Since(start) > 5*time.Second {
		t.Fatalf("Tools took %v", time.Since(start))
	}
	if !gotato.IsCode(err, gotato.CodeDeadlineExceeded) {
		t.Fatalf("err = %v, want deadline_exceeded", err)
	}
	if _, ok := entryOf(entries, "own"); !ok {
		t.Fatalf("entries = %+v, want the Agent's own tool", entries)
	}
}

// Switching an own tool never waits for a ToolSet, and switching a tool of a
// ToolSet that hangs is refused within the bound, so the Session lock is not
// held for a ToolSet's answer.
func TestSetToolActiveDoesNotWaitForAHangingToolSet(t *testing.T) {
	runner, id := slowRunner(t)
	ctx := context.Background()
	start := time.Now()
	entry, err := runner.SetToolActive(ctx, id, "own", false)
	if err != nil || entry.Active {
		t.Fatalf("own = %+v, %v", entry, err)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatalf("an own tool waited for a ToolSet: %v", time.Since(start))
	}
	start = time.Now()
	_, err = runner.SetToolActive(ctx, id, "slow.x", false)
	if !gotato.IsCode(err, gotato.CodeDeadlineExceeded) {
		t.Fatalf("err = %v, want deadline_exceeded", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("SetToolActive took %v", time.Since(start))
	}
}
