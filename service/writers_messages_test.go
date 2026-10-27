package service_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
)

// holdingOwner is an ownerModel whose calls all wait for a gate, so a Run on
// it stays in flight while messages arrive. A call that an Interrupt cancels
// ends with the context's error, and the next call waits for the gate again.
type holdingOwner struct {
	ownerModel
	gate    chan struct{}
	started chan struct{}
	once    sync.Once
}

func (m *holdingOwner) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	m.once.Do(func() { close(m.started) })
	select {
	case <-m.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return m.ownerModel.Stream(ctx, request)
}

func addressedRunner(t *testing.T, store session.Store, instance string, model gotato.Model) *service.Runner {
	t.Helper()
	runner, err := service.New(service.Config{
		Store: store, Instance: instance, Address: "http://" + instance + ".example:8080", Liveness: steadyLiveness,
		Admission: service.Admission{Queue: service.WaitWhileBusy},
		Agents:    []service.Agent{{Name: "echo", Model: model}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	return runner
}

// messageEvents returns, per source, the message IDs in the order they were
// queued, and the order they were committed, and the IDs that were dropped.
func messageEvents(t *testing.T, store session.Store, id string) (queued, committed map[gotato.MessageSource][]gotato.MessageID, dropped map[gotato.MessageID]bool) {
	t.Helper()
	events, err := session.StoredEvents(context.Background(), store, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	queued = map[gotato.MessageSource][]gotato.MessageID{}
	committed = map[gotato.MessageSource][]gotato.MessageID{}
	dropped = map[gotato.MessageID]bool{}
	for _, event := range events {
		switch e := event.(type) {
		case gotato.MessageQueued:
			queued[e.Source] = append(queued[e.Source], e.MessageID)
		case gotato.MessageCommitted:
			committed[e.Source] = append(committed[e.Source], e.Message.ID)
		case gotato.MessageDropped:
			dropped[e.MessageID] = true
		}
	}
	return queued, committed, dropped
}

// A Run in flight on one instance takes the messages sent to it from three
// instances at once: those sent to the writer land once each in order, those
// sent to the others are refused with the writer's address and land nowhere,
// and no instance but the writer calls the model (GOALS.md G-G08).
func TestMessagesMidRunLandOnceOnTheWriter(t *testing.T) {
	ctx := context.Background()
	store := session.NewMemoryStore()
	var violations, calls atomic.Int64
	var dead atomic.Bool
	hold := &holdingOwner{
		ownerModel: ownerModel{instance: "instance-0", store: store, dead: &dead, violations: &violations, calls: &calls},
		gate:       make(chan struct{}), started: make(chan struct{}),
	}
	runners := []*service.Runner{
		addressedRunner(t, store, "instance-0", hold),
		addressedRunner(t, store, "instance-1", ownerModel{instance: "instance-1", store: store, dead: &dead, violations: &violations, calls: &calls}),
		addressedRunner(t, store, "instance-2", ownerModel{instance: "instance-2", store: store, dead: &dead, violations: &violations, calls: &calls}),
	}
	s, err := runners[0].CreateSession(ctx, "echo", nil)
	if err != nil {
		t.Fatal(err)
	}
	id := s.ID()
	runDone := make(chan error, 1)
	go func() {
		_, err := runners[0].Run(ctx, service.RunRequest{SessionID: id, Prompt: id + "/run"})
		runDone <- err
	}()
	<-hold.started

	kinds := []service.MessageKind{service.MessageSteer, service.MessageFollowUp, service.MessageInterrupt}
	const senders = 18
	type sent struct {
		text   string
		result service.MessageResult
		err    error
		runner int
	}
	results := make([]sent, senders)
	var wg sync.WaitGroup
	for k := range senders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			text := fmt.Sprintf("%s/m-%d", id, k)
			result, err := runners[k%3].Send(ctx, service.MessageRequest{SessionID: id, Kind: kinds[k/3%3], Text: text})
			results[k] = sent{text: text, result: result, err: err, runner: k % 3}
		}()
	}
	wg.Wait()
	close(hold.gate)
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}

	accepted, refused := map[gotato.MessageID]string{}, map[string]bool{}
	for _, r := range results {
		switch r.runner {
		case 0:
			if r.err != nil || !r.result.Queued {
				t.Fatalf("a message to the writer: %+v, %v; want queued", r.result, r.err)
			}
			accepted[gotato.MessageID(r.result.MessageID)] = r.text
		default:
			address, ok := service.WriterOf(r.err)
			if !gotato.IsCode(r.err, gotato.ErrNotWriter) || !gotato.NotRun(r.err) || !gotato.Retryable(r.err) || !ok || address != "http://instance-0.example:8080" {
				t.Fatalf("a message to instance-%d: %v; want not_writer with instance-0's address", r.runner, r.err)
			}
			refused[r.text] = true
		}
	}

	queued, committed, dropped := messageEvents(t, store, id)
	for source, order := range queued {
		var got []gotato.MessageID
		for _, messageID := range committed[source] {
			if _, ok := accepted[messageID]; ok {
				got = append(got, messageID)
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(order) {
			t.Fatalf("%s messages were queued in the order %v and committed in the order %v", source, order, got)
		}
	}
	committedOnce := map[gotato.MessageID]int{}
	for _, ids := range committed {
		for _, messageID := range ids {
			committedOnce[messageID]++
		}
	}
	for messageID := range accepted {
		if committedOnce[messageID] != 1 || dropped[messageID] {
			t.Fatalf("message %s: committed %d times, dropped %v; want committed once", messageID, committedOnce[messageID], dropped[messageID])
		}
	}
	view, err := session.View(ctx, store, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range view.Messages() {
		if message.Role == gotato.RoleUser && refused[gotato.TextOf(message)] {
			t.Fatalf("a refused message landed in the Record: %q", gotato.TextOf(message))
		}
	}
	if n := violations.Load(); n != 0 {
		t.Fatalf("%d of %d model calls came from an instance that did not hold the Session", n, calls.Load())
	}
}

// TestManyInstancesOneWriterWithMessages is G-G08 with prompts and messages
// together: several instances take, concurrently and at random, prompts and
// Steers, FollowUps and Interrupts for the same Sessions, and one instance is
// killed partway. A message lands at most once; one a live instance accepted
// is committed or dropped, never lost without a word; one that was refused
// lands nowhere; and no instance calls the model for a Session it does not hold.
func TestManyInstancesOneWriterWithMessages(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			var open func() session.Store
			if kind == "memory" {
				backing := session.NewMemoryStore()
				open = func() session.Store { return backing }
			} else {
				dir := t.TempDir()
				open = func() session.Store {
					store, err := session.NewFileStore(dir)
					if err != nil {
						t.Fatal(err)
					}
					return store
				}
			}
			var violations, calls atomic.Int64
			const instances = 3
			runners := make([]*service.Runner, instances)
			stores := make([]*killableStore, instances)
			for i := range instances {
				stores[i] = &killableStore{Store: open()}
				model := ownerModel{instance: fmt.Sprintf("instance-%d", i), store: open(), dead: &stores[i].dead, violations: &violations, calls: &calls}
				runners[i] = addressedRunner(t, stores[i], model.instance, model)
			}
			var ids []string
			for range 2 {
				s, err := runners[0].CreateSession(ctx, "echo", nil)
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, s.ID())
			}

			type outcome struct {
				text, session string
				runner        int
				result        service.MessageResult
				err           error
			}
			outcomes := make(chan outcome, 128)
			var done atomic.Int64
			var wg sync.WaitGroup
			kinds := []service.MessageKind{service.MessageSteer, service.MessageFollowUp, service.MessageInterrupt}
			for worker := range 6 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					random := rand.New(rand.NewPCG(uint64(worker), 11))
					for k := range 8 {
						id := ids[random.IntN(len(ids))]
						text := fmt.Sprintf("%s/p-%d-%d", id, worker, k)
						index := random.IntN(instances)
						result, err := runners[index].Send(ctx, service.MessageRequest{SessionID: id, Kind: kinds[random.IntN(len(kinds))], Text: text})
						outcomes <- outcome{text: text, session: id, runner: index, result: result, err: err}
						// One instance is killed once a fifth of the work is done.
						if done.Add(1) == 10 {
							stores[instances-1].dead.Store(true)
						}
					}
				}()
			}
			wg.Wait()
			close(outcomes)

			var all []outcome
			for o := range outcomes {
				all = append(all, o)
			}
			if n := violations.Load(); n != 0 {
				t.Fatalf("%d of %d model calls came from an instance that did not hold the Session", n, calls.Load())
			}
			seen := map[string]int{}
			ended := map[string]map[gotato.MessageID]bool{}
			replies := map[string]string{}
			for _, id := range ids {
				queued, committed, dropped := messageEvents(t, open(), id)
				_ = queued
				ended[id] = map[gotato.MessageID]bool{}
				for _, list := range committed {
					for _, messageID := range list {
						ended[id][messageID] = true
					}
				}
				for messageID := range dropped {
					ended[id][messageID] = true
				}
				view, err := session.View(ctx, open(), id)
				if err != nil {
					t.Fatal(err)
				}
				messages := view.Messages()
				for i, message := range messages {
					if message.Role != gotato.RoleUser {
						continue
					}
					text := gotato.TextOf(message)
					seen[text]++
					// An Interrupt can follow a prompt before its reply, so the
					// reply is the next assistant Message, not the next Message.
					for _, later := range messages[i+1:] {
						if later.Role == gotato.RoleAssistant {
							replies[text] = gotato.TextOf(later)
							break
						}
					}
				}
			}
			var queuedCount, ranCount, refusedCount int
			for _, o := range all {
				killed := o.runner == instances-1
				switch {
				case o.err != nil:
					refusedCount++
					if gotato.NotRun(o.err) && !killed && seen[o.text] != 0 {
						t.Fatalf("a refused message landed in the Record: %q (%v)", o.text, o.err)
					}
				case o.result.Queued:
					queuedCount++
					if !killed && !ended[o.session][gotato.MessageID(o.result.MessageID)] {
						t.Fatalf("message %s was accepted and neither committed nor dropped", o.result.MessageID)
					}
				case o.result.Run != nil && o.result.Run.Result.Status == gotato.RunCompleted:
					ranCount++
					if seen[o.text] != 1 || !strings.Contains(replies[o.text], ": "+o.session) {
						t.Fatalf("the Run that message %q started landed %d times, answered by %q", o.text, seen[o.text], replies[o.text])
					}
				}
			}
			for text, n := range seen {
				if n > 1 {
					t.Fatalf("message %q landed %d times", text, n)
				}
			}
			t.Logf("%d queued, %d started a Run, %d refused or failed; %d model calls", queuedCount, ranCount, refusedCount, calls.Load())
		})
	}
}
