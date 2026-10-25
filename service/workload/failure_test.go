package workload_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
)

// G-G11 (GOALS.md): a failure leaves the Session usable. Every test below
// injects one fault into a Run of a Session that already holds a finished
// Run, through a Runner as a service would, and checks three things: the
// Record keeps every step committed before the fault, the failed Run settles
// with a status and a cause, and the next Run on the healthy Session succeeds
// with a first request that holds the history the store holds.
//
// An instance that dies is TestCrashAtEveryWriteRebuildsTheLastStep (G-G01),
// and a client that disconnects mid-stream is
// TestStreamDisconnectSettlesAndSavesTheRun (G-G02), over HTTP and gRPC.

var errProvider = errors.New("provider unavailable")

// rigFault names what the rig's model and tool do while it is armed.
type rigFault struct {
	// model is "error" (Stream returns an error) or "midstream" (the reply
	// starts, then its stream fails), at the call named by afterTool.
	model     string
	afterTool bool
	// tool is "panic" or "hang".
	tool string
}

// rig is one Runner over a store, with a model and a tool that follow the
// armed fault. A prompt that holds "use-tool" has the model call the tool
// once; every other prompt gets a plain reply.
type rig struct {
	t      *testing.T
	runner *service.Runner
	store  session.Store

	mu       sync.Mutex
	fault    rigFault
	requests []gotato.ModelRequest
	started  chan struct{}
}

func (r *rig) arm(f rigFault) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fault = f
	r.started = make(chan struct{})
}

func (r *rig) heal() { r.arm(rigFault{}) }

func (r *rig) current() (rigFault, chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fault, r.started
}

type rigModel struct{ r *rig }

type midstreamError struct{ sent bool }

func (m *midstreamError) Recv(context.Context) (gotato.ModelEvent, error) {
	if !m.sent {
		m.sent = true
		return gotato.ModelEvent{Kind: gotato.ModelTextDelta, Text: "half a rep"}, nil
	}
	return gotato.ModelEvent{}, errProvider
}

func (*midstreamError) Close() error { return nil }

type scripted struct {
	events []gotato.ModelEvent
}

func (s *scripted) Recv(context.Context) (gotato.ModelEvent, error) {
	if len(s.events) == 0 {
		return gotato.ModelEvent{}, io.EOF
	}
	e := s.events[0]
	s.events = s.events[1:]
	return e, nil
}

func (*scripted) Close() error { return nil }

func (m rigModel) Stream(_ context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	f, _ := m.r.current()
	messages := make([]gotato.Message, len(request.Messages))
	for i, msg := range request.Messages {
		messages[i] = msg.Clone()
	}
	request.Messages = messages
	last := messages[len(messages)-1]
	afterTool := last.Role == gotato.RoleToolResult
	m.r.mu.Lock()
	m.r.requests = append(m.r.requests, request)
	m.r.mu.Unlock()
	if f.model != "" && f.afterTool == afterTool {
		if f.model == "error" {
			return nil, errProvider
		}
		return &midstreamError{}, nil
	}
	switch {
	case afterTool:
		return &scripted{[]gotato.ModelEvent{{Kind: gotato.ModelTextDelta, Text: "done"}, {Kind: gotato.ModelDone, StopReason: gotato.StopEndTurn}}}, nil
	case strings.Contains(gotato.TextOf(last), "use-tool"):
		call := gotato.ToolCall{ID: "call-" + gotato.ToolCallID(gotato.TextOf(last)[len("use-tool "):]), ToolID: "probe", Arguments: []byte(`{}`)}
		return &scripted{[]gotato.ModelEvent{{Kind: gotato.ModelToolCall, ToolCall: &call}, {Kind: gotato.ModelDone, StopReason: gotato.StopToolCalls}}}, nil
	default:
		return &scripted{[]gotato.ModelEvent{{Kind: gotato.ModelTextDelta, Text: "ok"}, {Kind: gotato.ModelDone, StopReason: gotato.StopEndTurn}}}, nil
	}
}

type rigTool struct{ r *rig }

func (rigTool) Spec() gotato.ToolSpec {
	return gotato.ToolSpec{ID: "probe", Name: "probe", Description: "a probe", InputSchema: []byte(`{"type":"object"}`)}
}

func (t rigTool) Execute(ctx context.Context, _ gotato.ToolUse, _ gotato.ToolProgressFunc) (gotato.ToolResult, error) {
	f, started := t.r.current()
	switch f.tool {
	case "panic":
		panic("the probe broke")
	case "hang":
		close(started)
		<-ctx.Done()
		return gotato.ToolResult{}, ctx.Err()
	}
	return gotato.ToolResult{Status: gotato.ToolResultOK, Content: []gotato.ContentPart{{Kind: gotato.ContentText, Text: "probed"}}}, nil
}

// flakyStore counts the writes made through it since its last reset, keeps
// the Messages the store held after each, and fails the write numbered
// failAt (from 1) once.
type flakyStore struct {
	session.Store
	mu     sync.Mutex
	writes int
	failAt int
	states [][]gotato.Message
}

func (s *flakyStore) reset(failAt int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes, s.failAt, s.states = 0, failAt, nil
}

func (s *flakyStore) write(ctx context.Context, id string, do func() error) error {
	s.mu.Lock()
	s.writes++
	if s.writes == s.failAt {
		s.mu.Unlock()
		return errors.New("the disk is full")
	}
	s.mu.Unlock()
	if err := do(); err != nil {
		return err
	}
	stored, err := session.Open(ctx, s.Store, id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.states = append(s.states, stored.Messages())
	s.mu.Unlock()
	return nil
}

func (s *flakyStore) Create(ctx context.Context, id string, entries []session.Entry, summary session.Summary) error {
	return s.write(ctx, id, func() error { return s.Store.Create(ctx, id, entries, summary) })
}

func (s *flakyStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.Summary) error {
	return s.write(ctx, id, func() error { return s.Store.Append(ctx, id, after, entries, summary) })
}

func newRig(t *testing.T) (*rig, *flakyStore) {
	t.Helper()
	store := &flakyStore{Store: session.NewMemoryStore()}
	r := &rig{t: t, store: store}
	runner, err := service.New(service.Config{Store: store, Agents: []service.Agent{{
		Name: "probe", Model: rigModel{r}, Tools: []gotato.Tool{rigTool{r}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	r.runner = runner
	r.heal()
	return r, store
}

// seed creates a Session and plays one finished Run on it, so the fault
// strikes a Session with history.
func (r *rig) seed() (string, []gotato.Message) {
	r.t.Helper()
	ctx := context.Background()
	s, err := r.runner.CreateSession(ctx, "probe", nil)
	if err != nil {
		r.t.Fatal(err)
	}
	if res, err := r.runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: "use-tool one"}); err != nil || res.Result.Status != gotato.RunCompleted {
		r.t.Fatalf("the first Run = %+v, %v", res.Result, err)
	}
	return s.ID(), r.load(s.ID())
}

func (r *rig) load(id string) []gotato.Message {
	r.t.Helper()
	loaded, err := session.Open(context.Background(), r.store, id)
	if err != nil {
		r.t.Fatalf("the Session did not load: %v", err)
	}
	return loaded.Messages()
}

type outcome struct {
	res service.RunResult
	err error
}

// settled runs a request and fails the test if the Run does not return.
func (r *rig) settled(request service.RunRequest) (service.RunResult, error) {
	r.t.Helper()
	done := make(chan outcome, 1)
	go func() {
		res, err := r.runner.Run(context.Background(), request)
		done <- outcome{res, err}
	}()
	select {
	case o := <-done:
		return o.res, o.err
	case <-time.After(10 * time.Second):
		r.t.Fatal("the Run did not settle")
		return service.RunResult{}, nil
	}
}

// wantSettled checks that a Run that met a fault ended with a status that is
// not "running", and with a cause unless it completed.
func wantSettled(t *testing.T, res service.RunResult, want gotato.RunStatus) {
	t.Helper()
	got := res.Result.Status
	if got == "" || got == gotato.RunRunning {
		t.Fatalf("the Run did not settle: %+v", res.Result)
	}
	if want != "" && got != want {
		t.Fatalf("the Run settled as %s, want %s (%+v)", got, want, res.Result.Error)
	}
	if got != gotato.RunCompleted && (res.Result.Error == nil || res.Result.Error.Code == "") {
		t.Fatalf("the Run settled as %s with no cause", got)
	}
	if got == gotato.RunCompleted && res.Result.Error != nil {
		t.Fatalf("the Run completed and carries an error: %v", res.Result.Error)
	}
}

// wantUsable checks the other promises of G-G11 after a fault: the Record
// holds the steps committed before it, and the next Run on a healthy
// Session succeeds with a first request that holds what the store holds.
func (r *rig) wantUsable(id string, before []gotato.Message) {
	r.t.Helper()
	r.heal()
	after := r.load(id)
	if len(after) < len(before) || shape(after[:len(before)]) != shape(before) {
		r.t.Fatalf("the Record lost or changed a committed step:\n%s\nwas\n%s", shape(after), shape(before))
	}
	res, err := r.settled(service.RunRequest{SessionID: id, Prompt: "use-tool next"})
	if err != nil || res.Result.Status != gotato.RunCompleted {
		r.t.Fatalf("the next Run = %+v, %v", res.Result, err)
	}
	r.mu.Lock()
	var first gotato.ModelRequest
	for _, request := range r.requests {
		// The next Run's first request is the first one that holds its prompt.
		if strings.Contains(gotato.TextOf(request.Messages[len(request.Messages)-1]), "use-tool next") {
			first = request
			break
		}
	}
	r.mu.Unlock()
	// The model sees the stored history as ForModel reduces it, then the
	// new prompt.
	seen := gotato.ForModel(after)
	if len(first.Messages) != len(seen)+1 || shape(gotato.ForModel(first.Messages[:len(seen)])) != shape(seen) {
		r.t.Fatalf("the next Run's first request does not hold the stored history:\n%s\nstored\n%s", shape(first.Messages), shape(seen))
	}
	final := r.load(id)
	if len(final) <= len(after) || shape(final[:len(after)]) != shape(after) {
		r.t.Fatalf("the next Run did not extend the Record:\n%s\nafter the fault\n%s", shape(final), shape(after))
	}
}

func TestFailureLeavesTheSessionUsable(t *testing.T) {
	t.Run("a provider error", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			fault rigFault
		}{
			{"on the first call", rigFault{model: "error"}},
			{"after a tool step", rigFault{model: "error", afterTool: true}},
			{"mid-reply on the first call", rigFault{model: "midstream"}},
			{"mid-reply after a tool step", rigFault{model: "midstream", afterTool: true}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				r, _ := newRig(t)
				id, before := r.seed()
				r.arm(tc.fault)
				res, _ := r.settled(service.RunRequest{SessionID: id, Prompt: "use-tool two"})
				wantSettled(t, res, gotato.RunFailed)
				r.wantUsable(id, before)
			})
		}
	})

	t.Run("a tool that panics", func(t *testing.T) {
		r, _ := newRig(t)
		id, before := r.seed()
		r.arm(rigFault{tool: "panic"})
		res, _ := r.settled(service.RunRequest{SessionID: id, Prompt: "use-tool two"})
		wantSettled(t, res, "")
		// The panic is a failed tool call the model is told about.
		var failed bool
		for _, m := range r.load(id)[len(before):] {
			if m.ToolResult != nil && m.ToolResult.Status != gotato.ToolResultOK {
				failed = true
			}
		}
		if !failed && res.Result.Status == gotato.RunCompleted {
			t.Fatal("the panic left no failed tool result in the Record")
		}
		r.wantUsable(id, before)
	})

	t.Run("a tool that hangs", func(t *testing.T) {
		t.Run("until the Run's deadline", func(t *testing.T) {
			r, _ := newRig(t)
			id, before := r.seed()
			r.arm(rigFault{tool: "hang"})
			res, _ := r.settled(service.RunRequest{SessionID: id, Prompt: "use-tool two", Timeout: 100 * time.Millisecond})
			wantSettled(t, res, gotato.RunDeadlineExceeded)
			r.wantUsable(id, before)
		})
		t.Run("until the Run is cancelled", func(t *testing.T) {
			r, _ := newRig(t)
			id, before := r.seed()
			r.arm(rigFault{tool: "hang"})
			_, started := r.current()
			done := make(chan outcome, 1)
			go func() {
				res, err := r.runner.Run(context.Background(), service.RunRequest{SessionID: id, Prompt: "use-tool two"})
				done <- outcome{res, err}
			}()
			select {
			case <-started:
			case <-time.After(10 * time.Second):
				t.Fatal("the tool never started")
			}
			if err := r.runner.CancelSession(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			select {
			case o := <-done:
				wantSettled(t, o.res, gotato.RunCanceled)
			case <-time.After(10 * time.Second):
				t.Fatal("the Run did not settle after it was cancelled")
			}
			r.wantUsable(id, before)
		})
	})

	t.Run("a store write that fails once", func(t *testing.T) {
		// A clean play of the faulted Run counts its writes and keeps the
		// Record after each.
		clean, cleanStore := newRig(t)
		cleanID, _ := clean.seed()
		cleanStore.reset(0)
		if res, err := clean.settled(service.RunRequest{SessionID: cleanID, Prompt: "use-tool two"}); err != nil || res.Result.Status != gotato.RunCompleted {
			t.Fatalf("a clean Run = %+v, %v", res.Result, err)
		}
		writes, states := cleanStore.writes, cleanStore.states
		if writes < 3 {
			t.Fatalf("a Run made %d writes", writes)
		}
		t.Logf("%d writes in a Run, a failure at each", writes)
		for n := 1; n <= writes; n++ {
			r, store := newRig(t)
			id, before := r.seed()
			store.reset(n)
			res, err := r.settled(service.RunRequest{SessionID: id, Prompt: "use-tool two"})
			if err == nil && res.Result.Status == gotato.RunCompleted {
				t.Fatalf("write %d failed, and the Run reported success", n)
			}
			// completed never carries an error: a Run whose last writes failed
			// settles as not_saved, and says why.
			if err != nil && res.Result.Status == gotato.RunCompleted {
				t.Fatalf("write %d failed: the Run says completed and returns %v", n, err)
			}
			if res.Result.Status == gotato.RunNotSaved && !errors.Is(err, service.ErrNotPersisted) {
				t.Fatalf("write %d failed: not_saved with err = %v, want ErrNotPersisted", n, err)
			}
			if res.Result.Status == "" {
				// A Run refused before it started has no result, and says why.
				if err == nil {
					t.Fatalf("write %d failed: the Run returned neither a result nor an error", n)
				}
			} else {
				wantSettled(t, res, "")
			}
			// The Record holds what the write before the failed one left,
			// and nothing the clean Run would not have written.
			held := before
			if n > 1 {
				held = states[n-2]
			}
			// Opening a Session answers a tool call left open with a repair
			// Message that is not stored; it is not a step of the Run.
			got, held, final := unrepaired(r.load(id)), unrepaired(held), unrepaired(states[len(states)-1])
			if len(got) < len(held) || shape(got[:len(held)]) != shape(held) {
				t.Fatalf("write %d failed: the Record holds\n%s\nwant it to start with\n%s", n, shape(got), shape(held))
			}
			if len(got) > len(final) || shape(got) != shape(final[:len(got)]) {
				t.Fatalf("write %d failed: the Record holds\n%s\nwhich the clean Run never wrote", n, shape(got))
			}
			r.wantUsable(id, before)
		}
	})

	t.Run("a subscriber that stops reading", func(t *testing.T) {
		r, _ := newRig(t)
		ctx := context.Background()
		id, before := r.seed()
		instance, err := r.runner.SubscribeInstance(ctx, gotato.SubscribeBuffer(1))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = instance.Close() })
		stream, err := r.runner.SubscribeSession(ctx, id, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = stream.Close() })
		res, err := r.settled(service.RunRequest{SessionID: id, Prompt: "use-tool two"})
		if err != nil || res.Result.Status != gotato.RunCompleted {
			t.Fatalf("a Run with a stalled subscriber = %+v, %v", res.Result, err)
		}
		if got := r.load(id); len(got) != len(before)+4 {
			t.Fatalf("the Run left %d new Messages, want 4:\n%s", len(got)-len(before), shape(got))
		}
		r.wantUsable(id, before)
	})

	t.Run("a stream sink that stops reading", func(t *testing.T) {
		r, _ := newRig(t)
		ctx := context.Background()
		id, before := r.seed()
		release := make(chan struct{})
		got := make(chan struct{})
		var once sync.Once
		finished := make(chan error, 1)
		go func() {
			_, err := r.runner.StreamRun(ctx, service.RunRequest{SessionID: id, Prompt: "use-tool two"}, func(gotato.Event) error {
				once.Do(func() { close(got) })
				<-release
				return nil
			})
			finished <- err
		}()
		select {
		case <-got:
		case <-time.After(10 * time.Second):
			t.Fatal("the sink got no Event")
		}
		// The sink is stuck; the Run must still settle and be saved.
		deadline := time.Now().Add(10 * time.Second)
		for {
			view, err := r.runner.Session(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if runs := view.Runs(); len(runs) == 2 && runs[1].Status == gotato.RunCompleted {
				break
			}
			if time.Now().After(deadline) {
				close(release)
				t.Fatal("the Run waited for a sink that stopped reading")
			}
			time.Sleep(5 * time.Millisecond)
		}
		close(release)
		if err := <-finished; err != nil {
			t.Fatalf("StreamRun: %v", err)
		}
		r.wantUsable(id, before)
	})
}

// unrepaired leaves out the answers that opening a Session adds to tool calls
// left open.
func unrepaired(messages []gotato.Message) []gotato.Message {
	var out []gotato.Message
	for _, m := range messages {
		if !strings.HasPrefix(string(m.ID), "repair-") {
			out = append(out, m)
		}
	}
	return out
}
