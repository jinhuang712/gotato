package orchestration

import (
	"context"
	"errors"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
)

func TestSpawnCreatesAnIndependentRoutine(t *testing.T) {
	o := testOrchestrator(t)
	_, parent, err := o.Dispatch(context.Background(), Request{AgentName: "default", ConversationKey: "parent"}, gotato.UserMessage("hello"))
	if err != nil {
		t.Fatal(err)
	}
	child, childRecord, err := o.Spawn(context.Background(), SpawnRequest{
		Request: Request{AgentName: "default"},
		Origin:  Provenance{OriginAgentID: parent.LiveAgentID, OriginRunID: "run-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if childRecord.ID == parent.ID {
		t.Fatal("spawn reused the origin Conversation")
	}
	if childRecord.LiveAgentID == parent.LiveAgentID {
		t.Fatal("spawn reused the origin Agent")
	}
	if childRecord.Origin == nil || childRecord.Origin.OriginAgentID != parent.LiveAgentID {
		t.Fatalf("provenance = %+v", childRecord.Origin)
	}
	if childRecord.Origin.SpawnID == "" {
		t.Fatal("spawn did not assign a SpawnID")
	}

	// Provenance is correlation, not ownership: closing the origin leaves the
	// child running.
	if err := o.CloseAgent(context.Background(), parent.LiveAgentID); err != nil {
		t.Fatal(err)
	}
	current, ok := o.Get(childRecord.ID)
	if !ok || current.Status != ConversationActive || current.LiveAgentID == "" {
		t.Fatalf("child after the origin closed = %+v", current)
	}
	if _, err := child.Prompt(context.Background(), gotato.UserMessage("still here")); err != nil {
		t.Fatalf("child Prompt after the origin closed = %v", err)
	}
}

func groupTasks(keys ...string) []GroupTask {
	tasks := make([]GroupTask, 0, len(keys))
	for _, key := range keys {
		tasks = append(tasks, GroupTask{
			Request: Request{AgentName: "default", ConversationKey: gotato.ConversationKey(key)},
			Message: gotato.UserMessage("work"),
		})
	}
	return tasks
}

func TestGroupCollectAllWaitsForEveryMember(t *testing.T) {
	o := testOrchestrator(t)
	outcomes, err := o.RunGroup(context.Background(), Group{Policy: CollectAll}, groupTasks("a", "b", "c"))
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 3 {
		t.Fatalf("outcomes = %d", len(outcomes))
	}
	for _, outcome := range outcomes {
		if !outcome.Settled || outcome.Err != nil || outcome.Result.Status != gotato.RunCompleted {
			t.Fatalf("outcome %d = %+v", outcome.Index, outcome)
		}
	}
}

// failingModel fails for one named Conversation and succeeds for the rest.
type failingModel struct{ failKey gotato.ConversationKey }

func (m failingModel) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	return nil, errors.New("model unavailable")
}

func groupOrchestrator(t *testing.T, failing map[string]bool) *Orchestrator {
	t.Helper()
	o := New()
	err := o.Register(Definition{Name: "default", New: func(ctx context.Context, request Request) (gotato.Agent, error) {
		if failing[string(request.ConversationKey)] {
			return gotato.NewAgent(gotato.WithModel(failingModel{}))
		}
		return gotato.NewAgent(gotato.WithModel(testModel{}))
	}})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestGroupCollectAllReportsAMemberFailure(t *testing.T) {
	o := groupOrchestrator(t, map[string]bool{"b": true})
	outcomes, err := o.RunGroup(context.Background(), Group{Policy: CollectAll}, groupTasks("a", "b", "c"))
	if err == nil {
		t.Fatal("collect-all hid a member failure")
	}
	for _, outcome := range outcomes {
		if !outcome.Settled {
			t.Fatalf("collect-all returned before member %d settled", outcome.Index)
		}
	}
	if outcomes[1].Err == nil {
		t.Fatalf("failing member outcome = %+v", outcomes[1])
	}
}

func TestGroupCollectPartialNeverFailsTheGroup(t *testing.T) {
	o := groupOrchestrator(t, map[string]bool{"b": true})
	outcomes, err := o.RunGroup(context.Background(), Group{Policy: CollectPartial}, groupTasks("a", "b", "c"))
	if err != nil {
		t.Fatalf("collect-partial failed the group: %v", err)
	}
	if outcomes[0].Err != nil || outcomes[2].Err != nil {
		t.Fatalf("successful members = %+v %+v", outcomes[0], outcomes[2])
	}
	if outcomes[1].Err == nil {
		t.Fatal("collect-partial lost the member failure")
	}
}

func TestGroupFailFastStopsWaitingWithoutCancellingSiblings(t *testing.T) {
	release := make(chan struct{})
	o := New()
	err := o.Register(Definition{Name: "default", New: func(ctx context.Context, request Request) (gotato.Agent, error) {
		if request.ConversationKey == "fails" {
			return gotato.NewAgent(gotato.WithModel(failingModel{}))
		}
		return gotato.NewAgent(gotato.WithModel(gatedTestModel{release: release}))
	}})
	if err != nil {
		t.Fatal(err)
	}
	outcomes, groupErr := o.RunGroup(context.Background(), Group{Policy: FailFast}, groupTasks("fails", "slow"))
	if groupErr == nil {
		t.Fatal("fail-fast did not report the failure")
	}
	if !outcomes[0].Settled || outcomes[0].Err == nil {
		t.Fatalf("failing member = %+v", outcomes[0])
	}
	if outcomes[1].Settled {
		t.Fatalf("fail-fast waited for the slow member: %+v", outcomes[1])
	}
	// The sibling was not cancelled: it is still running and settles normally.
	waitFor(t, func() bool { return o.ActiveRuns() == 1 })
	close(release)
	waitFor(t, func() bool { return o.ActiveRuns() == 0 })
}

func TestGroupCancelSiblingsIsOptIn(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	o := New()
	err := o.Register(Definition{Name: "default", New: func(ctx context.Context, request Request) (gotato.Agent, error) {
		if request.ConversationKey == "fails" {
			return gotato.NewAgent(gotato.WithModel(failingModel{}))
		}
		return gotato.NewAgent(gotato.WithModel(gatedTestModel{release: release}))
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, groupErr := o.RunGroup(context.Background(), Group{Policy: FailFast, CancelSiblings: true}, groupTasks("fails", "slow"))
	if groupErr == nil {
		t.Fatal("fail-fast did not report the failure")
	}
	// The explicit opt-in cancels the sibling instead of leaving it running.
	waitFor(t, func() bool { return o.ActiveRuns() == 0 })
}

func TestGroupFirstSuccessStopsAtTheFirstWin(t *testing.T) {
	o := groupOrchestrator(t, map[string]bool{"a": true, "b": true})
	outcomes, err := o.RunGroup(context.Background(), Group{Policy: FirstSuccess}, groupTasks("a", "b", "c"))
	if err != nil {
		t.Fatalf("first-success with one winner = %v", err)
	}
	won := false
	for _, outcome := range outcomes {
		if outcome.Settled && outcome.Err == nil {
			won = true
		}
	}
	if !won {
		t.Fatalf("no successful outcome recorded: %+v", outcomes)
	}
}

func TestGroupFirstSuccessFailsWhenEveryMemberFails(t *testing.T) {
	o := groupOrchestrator(t, map[string]bool{"a": true, "b": true})
	_, err := o.RunGroup(context.Background(), Group{Policy: FirstSuccess}, groupTasks("a", "b"))
	if !gotato.IsCode(err, gotato.ErrInvalidState) {
		t.Fatalf("first-success with no winner = %v", err)
	}
}

func TestGroupHonoursTheCallerContext(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	o := New()
	err := o.Register(Definition{Name: "default", New: func(ctx context.Context, request Request) (gotato.Agent, error) {
		return gotato.NewAgent(gotato.WithModel(gatedTestModel{release: release}))
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, groupErr := o.RunGroup(ctx, Group{Policy: CollectAll}, groupTasks("x", "y"))
	if groupErr == nil {
		t.Fatal("group ignored its own Context")
	}
}
