package service_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

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
