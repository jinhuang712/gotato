package workload_test

import (
	"context"
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

// faultStore counts the writes made through it. Once crashAfter writes have
// succeeded, every later write fails, and so does every renewal of the
// instance's Liveness row, as if the instance died there, and the row is
// removed at that moment, as if it had expired, so the next instance takes the
// Sessions over without waiting for a TTL. When record is set, it keeps the
// Transcript the store held after each write.
type faultStore struct {
	session.Store
	crashAfter int
	record     bool

	mu       sync.Mutex
	writes   int
	states   [][]gotato.Message
	instance string // the ID of the Liveness row the Runner renews
	buried   bool
}

// bury removes the dead instance's row once, as its expiry would.
func (s *faultStore) bury(ctx context.Context) {
	s.mu.Lock()
	row, buried := s.instance, s.buried
	s.buried = true
	s.mu.Unlock()
	if row != "" && !buried {
		_ = s.Store.Beat(ctx, session.InstanceRow{ID: row}, 0)
	}
}

var errCrashed = errors.New("the instance died")

func (s *faultStore) write(ctx context.Context, id string, do func() error) error {
	s.mu.Lock()
	if s.crashAfter > 0 && s.writes >= s.crashAfter {
		s.mu.Unlock()
		s.bury(ctx)
		return errCrashed
	}
	s.mu.Unlock()
	if err := do(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes++
	if s.record {
		stored, err := session.Open(ctx, s.Store, id)
		if err != nil {
			return err
		}
		s.states = append(s.states, stored.Messages())
	}
	return nil
}

func (s *faultStore) Beat(ctx context.Context, row session.InstanceRow, ttl time.Duration) error {
	s.mu.Lock()
	s.instance = row.ID
	dead := s.crashAfter > 0 && s.writes >= s.crashAfter
	s.mu.Unlock()
	if dead {
		s.bury(ctx)
		return errCrashed
	}
	return s.Store.Beat(ctx, row, ttl)
}

func (s *faultStore) Create(ctx context.Context, id string, entries []session.Entry, summary session.RecordInfo) error {
	return s.write(ctx, id, func() error { return s.Store.Create(ctx, id, entries, summary) })
}

func (s *faultStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.RecordInfo) error {
	return s.write(ctx, id, func() error { return s.Store.Append(ctx, id, after, entries, summary) })
}

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

// TestCrashAtEveryWriteRebuildsTheLastStep checks GOALS.md G-G01 on the
// reference workload: the instance dies after each write a task makes, in
// turn, and another instance must rebuild exactly what that write left, with
// a Context whose prefix hash matches it, and go on.
func TestCrashAtEveryWriteRebuildsTheLastStep(t *testing.T) {
	ctx := context.Background()
	for _, task := range reftasks.ReferenceTasks() {
		t.Run(task.Name, func(t *testing.T) {
			clean := &faultStore{Store: session.NewMemoryStore(), record: true}
			if _, err := play(ctx, taskRunner(t, clean, task, "clean"), task); err != nil {
				t.Fatalf("a clean play failed: %v", err)
			}
			if clean.writes < 3 {
				t.Fatalf("a play made %d writes", clean.writes)
			}
			t.Logf("%d writes, a crash after each", clean.writes)
			// Each write holds at most one more stored Message than the one
			// before (the answers repair adds are not stored): a Run is
			// written step by step, so a crash loses at most the step in
			// flight.
			for i := 1; i < len(clean.states); i++ {
				if grew := stored(clean.states[i]) - stored(clean.states[i-1]); grew > 1 {
					t.Fatalf("write %d added %d Messages at once", i+1, grew)
				}
			}
			if final, first := stored(clean.states[len(clean.states)-1]), stored(clean.states[0]); final-first < 2 {
				t.Fatalf("the play wrote %d Messages after the history", final-first)
			}
			for n := 1; n < clean.writes; n++ {
				backing := session.NewMemoryStore()
				dying := &faultStore{Store: backing, crashAfter: n}
				id, err := play(ctx, taskRunner(t, dying, task, "dying"), task)
				// The last write only releases the Session. A crash after the
				// one before it loses nothing of the Run, so the Run completes.
				releaseOnly := n == clean.writes-1
				if id == "" || (err == nil && !releaseOnly) || (err != nil && releaseOnly) {
					t.Fatalf("crash after write %d: the play ended with %v", n, err)
				}

				// Another instance opens the Session: it holds what the
				// n-th write left, with every Tool call answered.
				rebuilt, err := session.Open(ctx, backing, id)
				if err != nil {
					t.Fatalf("crash after write %d: %v", n, err)
				}
				if got, want := shape(rebuilt.Messages()), shape(clean.states[n-1]); got != want {
					t.Fatalf("crash after write %d rebuilt\n%s\nwant\n%s", n, got, want)
				}
				next := taskRunner(t, backing, task, "next")
				report, err := next.Inspect(ctx, id, service.InspectConfig{})
				if err != nil {
					t.Fatalf("crash after write %d: inspect: %v", n, err)
				}
				reference := taskRunner(t, session.NewMemoryStore(), task, "reference")
				before, err := reference.CreateSession(ctx, task.Name, nil, session.WithMessages(clean.states[n-1]...), reftasks.WithLoadedToolsOf(clean.states[n-1]))
				if err != nil {
					t.Fatal(err)
				}
				expected, err := reference.Inspect(ctx, before.ID(), service.InspectConfig{})
				if err != nil {
					t.Fatal(err)
				}
				if report.PrefixHash != expected.PrefixHash {
					t.Fatalf("crash after write %d: the rebuilt Context has prefix %s, the clean one %s", n, report.PrefixHash, expected.PrefixHash)
				}
				result, err := next.Run(ctx, service.RunRequest{SessionID: id, Prompt: task.Prompts[len(task.Prompts)-1]})
				if err != nil || result.Result.Status != gotato.RunStatusCompleted {
					t.Fatalf("crash after write %d: the next Run = %+v, %v", n, result.Result, err)
				}
			}
		})
	}
}
