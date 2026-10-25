package service_test

import (
	"context"
	"regexp"
	"testing"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

var epochField = regexp.MustCompile(`,?"record_epoch":"[^"]*"`)

// preEpochStore stores Records as the version before epochs did: no epoch in
// session_created, none in the Summary.
type preEpochStore struct{ session.Store }

func strip(entries []session.Entry) []session.Entry {
	out := make([]session.Entry, len(entries))
	for i, e := range entries {
		out[i] = e
		out[i].Data = epochField.ReplaceAll(e.Data, nil)
	}
	return out
}

func (s preEpochStore) Create(ctx context.Context, id string, entries []session.Entry, summary session.Summary) error {
	summary.RecordEpoch = ""
	return s.Store.Create(ctx, id, strip(entries), summary)
}

func (s preEpochStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.Summary) error {
	summary.RecordEpoch = ""
	return s.Store.Append(ctx, id, after, strip(entries), summary)
}

// For a Record written before epochs, every surface reports the same epoch,
// the one made of its creation time: a Run's result, the Session list, and the
// stream. A client that keeps any of them can resume with it.
func TestPreEpochRecordHasOneEpochOnEverySurface(t *testing.T) {
	runner, err := service.New(service.Config{
		Store:  preEpochStore{session.NewMemoryStore()},
		Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}, Warm: service.WarmPolicy{TTL: -1}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	result, err := runner.Run(ctx, service.RunRequest{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	// The first Run made the Record in memory; a second one loads it from the
	// stored facts, which hold no epoch, as a Record of the earlier version does.
	again, err := runner.Run(ctx, service.RunRequest{SessionID: result.SessionID, Prompt: "again"})
	if err != nil {
		t.Fatal(err)
	}
	list, err := runner.Sessions(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("Sessions = %v, %v", list, err)
	}
	want := "created-" + list[0].CreatedAt
	stream, err := runner.StreamSession(ctx, result.SessionID, service.StreamFrom{RecordEpoch: want})
	if err != nil {
		t.Fatalf("the stream refuses the epoch %q: %v", want, err)
	}
	_ = stream.Close()
	for name, got := range map[string]string{"second result": again.RecordEpoch, "list": list[0].RecordEpoch, "stream": stream.RecordEpoch} {
		if got != want {
			t.Errorf("%s reports epoch %q, want %q", name, got, want)
		}
	}
}
