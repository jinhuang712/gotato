package service_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// The warm Sessions of an Instance are bounded by Admission.MaxWarmSessions:
// when one more would pass it, the longest-idle one leaves memory, evicted.
func TestMaxWarmSessionsEvictsTheOldestIdle(t *testing.T) {
	clock := newFakeClock()
	runner, err := service.New(service.WithClock(service.Config{
		Store: session.NewMemoryStore(), Instance: "a",
		Admission: service.Admission{MaxWarmSessions: 2},
		Agents:    []service.Agent{{Name: "echo", Model: testkit.EchoModel{}, Warm: service.WarmPolicy{NoIdleCompaction: true}}},
	}, clock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
	instance := instanceRecorder(t, runner)

	first := runOn(t, runner, "", "one")
	clock.Advance(time.Minute)
	second := runOn(t, runner, "", "two")
	clock.Advance(time.Minute)
	if runner.WarmSessions() != 2 {
		t.Fatalf("WarmSessions = %d, want 2", runner.WarmSessions())
	}
	third := runOn(t, runner, "", "three")
	if runner.WarmSessions() != 2 {
		t.Fatalf("WarmSessions = %d after a third Session went idle, want the limit of 2", runner.WarmSessions())
	}
	waitFor(t, func() bool { return len(evictedIDs(instance)) >= 1 })
	if got := evictedIDs(instance); !slices.Equal(got, []string{first.SessionID}) {
		t.Fatalf("evicted %v, want the longest-idle Session %q (not %q or %q)", got, first.SessionID, second.SessionID, third.SessionID)
	}
}

// Warm memory has a default bound: a Runner with the zero Admission does not
// keep every Session of the hour for ever.
func TestWarmSessionsHaveADefaultBound(t *testing.T) {
	runner, err := service.New(service.Config{
		Store:  session.NewMemoryStore(),
		Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := runner.WarmLimit(); got != service.DefaultMaxWarmSessions || got <= 0 {
		t.Fatalf("MaxWarmSessions = %d, want the default %d", got, service.DefaultMaxWarmSessions)
	}
}
