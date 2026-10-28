package service_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// createErrorStore fails to create one ID, standing in for a Store that is
// broken rather than holding the Session already.
type createErrorStore struct {
	session.Store
	failID string
	err    error
}

func (s *createErrorStore) Create(ctx context.Context, id string, entries []session.Entry, summary session.RecordInfo) error {
	if id == s.failID {
		return s.err
	}
	return s.Store.Create(ctx, id, entries, summary)
}

func TestForkTreatsStoreErrorAsFailure(t *testing.T) {
	backing := session.NewMemoryStore()
	getErr := errors.New("store: unavailable")
	runner, err := service.New(service.Config{
		Store:  &createErrorStore{Store: backing, failID: "child", err: getErr},
		Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	parent, err := runner.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Fork(ctx, parent.ID(), "child"); !errors.Is(err, getErr) {
		t.Fatalf("fork err = %v, want the store error", err)
	}
	if _, err := session.View(ctx, backing, "child"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("fork saved a session despite a store error: %v", err)
	}
}

func TestForkWithExplicitIDIsExclusive(t *testing.T) {
	runner, _ := newRunner(t)
	ctx := context.Background()
	parent, err := runner.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	var wg sync.WaitGroup
	results := make(chan error, n)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := runner.Fork(ctx, parent.ID(), "shared-fork")
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
			continue
		}
		// A loser either finds the ID taken or is rejected while the winner
		// holds the lock; neither may overwrite the winner.
		if !errors.Is(err, service.ErrSessionExists) && !gotato.IsCode(err, gotato.CodeBusy) {
			t.Fatalf("unexpected fork err = %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful forks = %d, want exactly 1", succeeded)
	}
}

// A Fork whose ID is taken reports ErrSessionExists, as CreateSessionExclusive
// does, and leaves the Session that holds the ID as it was.
func TestForkWithATakenIDIsErrSessionExists(t *testing.T) {
	runner, _ := newRunner(t)
	ctx := context.Background()
	parent, err := runner.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Fork(ctx, parent.ID(), "taken"); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Fork(ctx, parent.ID(), "taken"); !errors.Is(err, service.ErrSessionExists) {
		t.Fatalf("second Fork = %v, want ErrSessionExists", err)
	}
	if _, err := runner.CreateSessionExclusive(ctx, "echo", "taken", nil); !errors.Is(err, service.ErrSessionExists) {
		t.Fatalf("CreateSessionExclusive = %v, want ErrSessionExists", err)
	}
}

// wantNotRunStoreFailure checks that err is a store_failure that says it did
// not run and may be retried, and that the store's raw error is its cause.
func wantNotRunStoreFailure(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s succeeded, want a store failure", what)
	}
	if !gotato.IsCode(err, gotato.CodeStoreFailure) {
		t.Fatalf("%s = %v (%T), want code store_failure", what, err, err)
	}
	if !gotato.NotRun(err) || !gotato.Retryable(err) {
		t.Fatalf("%s = %v: not_run=%v retryable=%v, want both", what, err, gotato.NotRun(err), gotato.Retryable(err))
	}
}

// Every store call a request makes before it writes any step of a Run is
// classified: a raw error of the store is store_failure, marked not run and
// retryable, as the write of a step is store_failure.
func TestStoreErrorsBeforeAnyRunWriteAreClassified(t *testing.T) {
	ctx := context.Background()
	newRunner := func(t *testing.T) (*service.Runner, *faultStore, string) {
		store := &faultStore{Store: session.NewMemoryStore()}
		runner := instanceRunner(t, store, "instance-a", testkit.EchoModel{}, steadyLiveness)
		s, err := runner.CreateSession(ctx, "echo", nil)
		if err != nil {
			t.Fatal(err)
		}
		return runner, store, s.ID()
	}

	t.Run("Load", func(t *testing.T) {
		runner, store, id := newRunner(t)
		store.fail("Load", true)
		_, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "hi"})
		wantNotRunStoreFailure(t, "Run with Load failing", err)
		store.fail("Load", false)
		if _, err := runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: "hi"}); err != nil {
			t.Fatalf("the same request after the store returned: %v", err)
		}
	})
	t.Run("Create by Run", func(t *testing.T) {
		runner, store, _ := newRunner(t)
		store.fail("Create", true)
		_, err := runner.Run(ctx, service.RunRequest{Prompt: "hi"})
		wantNotRunStoreFailure(t, "Run that creates, with Create failing", err)
	})
	t.Run("Create", func(t *testing.T) {
		runner, store, _ := newRunner(t)
		store.fail("Create", true)
		_, err := runner.CreateSession(ctx, "echo", nil)
		wantNotRunStoreFailure(t, "CreateSession with Create failing", err)
	})
	t.Run("Beat", func(t *testing.T) {
		store := &faultStore{Store: session.NewMemoryStore()}
		runner := instanceRunner(t, store, "instance-a", testkit.EchoModel{}, steadyLiveness)
		store.fail("Beat", true)
		_, err := runner.CreateSession(ctx, "echo", nil)
		wantNotRunStoreFailure(t, "CreateSession with the first Beat failing", err)
		_, err = runner.Run(ctx, service.RunRequest{Prompt: "hi"})
		wantNotRunStoreFailure(t, "Run with the first Beat failing", err)
	})
	t.Run("Delete", func(t *testing.T) {
		runner, store, id := newRunner(t)
		store.fail("Delete", true)
		wantNotRunStoreFailure(t, "DeleteSession with Delete failing", runner.DeleteSession(ctx, id))
	})
	t.Run("Fork", func(t *testing.T) {
		runner, store, id := newRunner(t)
		store.fail("Log", true)
		_, err := runner.Fork(ctx, id, "")
		wantNotRunStoreFailure(t, "Fork with the read failing", err)
	})
	t.Run("Record", func(t *testing.T) {
		runner, store, id := newRunner(t)
		store.fail("Log", true)
		_, err := runner.Record(ctx, id)
		wantNotRunStoreFailure(t, "Record with the read failing", err)
		_, err = runner.Events(ctx, id)
		wantNotRunStoreFailure(t, "Events with the read failing", err)
	})
}

// A missing Session stays not_found: the store's own answer is not a failure.
func TestStoreAnswersKeepTheirCode(t *testing.T) {
	ctx := context.Background()
	store := &faultStore{Store: session.NewMemoryStore()}
	runner := instanceRunner(t, store, "instance-a", testkit.EchoModel{}, steadyLiveness)
	if _, err := runner.Run(ctx, service.RunRequest{SessionID: "missing", Prompt: "hi"}); !gotato.IsCode(err, gotato.CodeNotFound) {
		t.Fatalf("Run on a missing Session = %v, want not_found", err)
	}
	if err := runner.DeleteSession(ctx, "missing"); !gotato.IsCode(err, gotato.CodeNotFound) {
		t.Fatalf("DeleteSession of a missing Session = %v, want not_found", err)
	}
}

// pausingStore pauses the delete between its look at the Session and its end:
// at the claim's Load, after it has read (the second Load), or at the store's
// Delete, whichever comes first. The caller then holds a Record that another
// Instance can still change.
type pausingStore struct {
	session.Store
	loads            atomic.Int32
	once             sync.Once
	entered, proceed chan struct{}
}

func (s *pausingStore) pause() {
	s.once.Do(func() {
		close(s.entered)
		<-s.proceed
	})
}

func (s *pausingStore) Load(ctx context.Context, id string) (session.Loaded, error) {
	loaded, err := s.Store.Load(ctx, id)
	if s.loads.Add(1) == 2 {
		s.pause()
	}
	return loaded, err
}

func (s *pausingStore) Delete(ctx context.Context, id string) error {
	s.pause()
	return s.Store.Delete(ctx, id)
}

// A delete is made by the Instance that holds the Claim. Another Instance that
// claimed the Session after the delete looked at it wins the claim, and the
// delete is refused: the Session and its Run stay.
func TestDeleteSessionDoesNotRemoveARunClaimedAfterTheLook(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	base := session.NewMemoryStore()
	gate := &pausingStore{Store: base, entered: make(chan struct{}), proceed: make(chan struct{})}
	liveness := service.Liveness{TTL: time.Second, Wait: 30 * time.Millisecond}
	a, err := service.New(service.Config{Store: gate, Liveness: liveness,
		Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}, Warm: service.WarmPolicy{TTL: -1}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(context.Background()) })

	called, finish := make(chan struct{}), make(chan struct{})
	model := testkit.NewFuncModel(func(ctx context.Context, _ gotato.ModelRequest) (testkit.Script, error) {
		close(called)
		select {
		case <-finish:
			return testkit.Text("done"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	b, err := service.New(service.Config{Store: base, Liveness: liveness,
		Agents: []service.Agent{{Name: "echo", Model: model, Warm: service.WarmPolicy{TTL: -1}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close(context.Background()) })

	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	deleted := make(chan error, 1)
	go func() { deleted <- a.DeleteSession(ctx, s.ID()) }()
	<-gate.entered
	ran := make(chan error, 1)
	go func() {
		_, runErr := b.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "execute"})
		ran <- runErr
	}()
	<-called
	close(gate.proceed)
	if err := <-deleted; err == nil {
		t.Fatal("DeleteSession removed a Session another Instance claimed after its look")
	}
	if _, err := base.Load(ctx, s.ID()); err != nil {
		t.Fatalf("the Session is gone: %v", err)
	}
	close(finish)
	if err := <-ran; err != nil {
		t.Fatalf("the Run on the other Instance: %v", err)
	}
}

// heldRun starts a Run on a that stays in the model call until release is
// closed, and returns once the model has been called.
func heldRun(t *testing.T, a *service.Runner, model *testkit.FakeModel, sessionID string) (release func(), result chan error) {
	t.Helper()
	block := make(chan struct{})
	model.Block = block
	result = make(chan error, 1)
	go func() {
		_, err := a.Run(context.Background(), service.RunRequest{SessionID: sessionID, Prompt: "first"})
		result <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for model.Calls() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the Run did not reach the model")
		}
		time.Sleep(5 * time.Millisecond)
	}
	var once bool
	return func() {
		if !once {
			once = true
			close(block)
		}
	}, result
}

// A Run going on another Instance is not deleted from under it: the delete is
// refused as not_writer, as a message is, and the Session is still there.
func TestDeleteSessionRefusesARunOnAnotherInstance(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	model := testkit.NewFakeModel(testkit.Text("slow"))
	a := instanceRunner(t, store, "instance-a", model, steadyLiveness)
	b := instanceRunner(t, store, "instance-b", testkit.EchoModel{}, steadyLiveness)
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	release, result := heldRun(t, a, model, s.ID())
	defer release()

	err = b.DeleteSession(ctx, s.ID())
	if !gotato.IsCode(err, gotato.CodeNotWriter) || !gotato.NotRun(err) {
		t.Fatalf("DeleteSession of a Session with a Run on another Instance = %v, want not_writer, not run", err)
	}
	release()
	if err := <-result; err != nil {
		t.Fatalf("the Run that was not deleted from under it: %v", err)
	}
	if _, err := b.Record(ctx, s.ID()); err != nil {
		t.Fatalf("the Session is gone: %v", err)
	}
	// With the Run over, the delete goes through.
	if err := b.DeleteSession(ctx, s.ID()); err != nil {
		t.Fatalf("DeleteSession of an idle Session: %v", err)
	}
}

// A Session deleted under a Run (by a store operation, or an Instance that
// did not check) ends that Run as not_found, not as a store_failure that
// asks the caller to retry a request that cannot succeed.
func TestRunOfASessionDeletedUnderItIsNotAStoreFailure(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	model := testkit.NewFakeModel(testkit.Text("slow"))
	a := instanceRunner(t, store, "instance-a", model, steadyLiveness)
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	release, result := heldRun(t, a, model, s.ID())
	if err := store.Delete(ctx, s.ID()); err != nil {
		t.Fatal(err)
	}
	release()
	err = <-result
	if err == nil {
		t.Fatal("a Run whose Session was deleted under it returned no error")
	}
	if gotato.IsCode(err, gotato.CodeStoreFailure) && !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("Run error = %v: a deleted Session is not a store failure", err)
	}
	if !gotato.IsCode(err, gotato.CodeNotFound) {
		t.Fatalf("Run error = %v, want not_found", err)
	}
	if gotato.Retryable(err) || gotato.NotRun(err) {
		t.Fatalf("Run error = %v: retryable=%v not_run=%v; the Run ran and a retry finds nothing", err, gotato.Retryable(err), gotato.NotRun(err))
	}
}

// A Compact that waits for a Session whose Run is on another Instance answers
// busy after the wait, as a Run does; it does not wait for ever or compact
// under the Run.
func TestCompactOfASessionRunningElsewhereAnswersBusy(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	model := testkit.NewFakeModel(testkit.Text("slow"))
	a := instanceRunner(t, store, "instance-a", model, steadyLiveness)
	b := instanceRunner(t, store, "instance-b", testkit.EchoModel{}, service.Liveness{TTL: 5 * time.Second, Wait: 200 * time.Millisecond})
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	release, result := heldRun(t, a, model, s.ID())
	defer release()
	_, err = b.Compact(ctx, s.ID(), modelctx.CompactConfig{})
	if !gotato.IsCode(err, gotato.CodeBusy) || !gotato.NotRun(err) {
		t.Fatalf("Compact of a Session with a Run on another Instance = %v, want busy, not run", err)
	}
	release()
	<-result
}

// A Fork taken while a Run is going on a live Instance copies what the Record
// holds; it must not carry the tool result "the instance running this tool
// stopped", which is true only of a Record whose writer is gone.
func TestForkMidRunDoesNotCarryAFalseStoppedToolResult(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	started := make(chan struct{})
	finish := make(chan struct{})
	tool, err := gotato.NewFuncTool("slow", "slow tool", func(context.Context, struct{}) (string, error) {
		close(started)
		<-finish
		return "done", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	model := testkit.NewFakeModel(testkit.ToolCalls(gotato.ToolCall{ID: "call-1", ToolID: "slow", Arguments: []byte(`{}`)}), testkit.Text("ok"))
	a, err := service.New(service.Config{
		Store: store, Instance: "instance-a", Liveness: steadyLiveness,
		Agents: []service.Agent{{Name: "echo", Model: model, Tools: []gotato.Tool{tool}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(ctx) })
	b := instanceRunner(t, store, "instance-b", testkit.EchoModel{}, steadyLiveness)
	s, err := a.CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := a.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "go"})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the tool did not start")
	}
	child, err := b.Fork(ctx, s.ID(), "")
	if err != nil {
		t.Fatal(err)
	}
	close(finish)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	for _, message := range child.Messages() {
		if text := gotato.TextOf(message); len(text) > 0 && containsStopped(text) {
			t.Fatalf("the Fork carries a false tool result: %q", text)
		}
	}
}

func containsStopped(text string) bool {
	const marker = "stopped before the tool finished"
	for i := 0; i+len(marker) <= len(text); i++ {
		if text[i:i+len(marker)] == marker {
			return true
		}
	}
	return false
}
