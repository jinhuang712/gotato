package workload

import (
	"context"
	"sync"
	"time"

	"github.com/jinhuang712/gotato/session"
)

// stats sums one play's time by where it was spent. The play's Events fill
// in the model's and the tools' time; timedStore fills in the store's, and
// finds the stats in the context the Runner passes to the store.
type stats struct {
	mu                           sync.Mutex
	run, model, tool, load, save time.Duration
}

type statsKey struct{}

func withStats(ctx context.Context, s *stats) context.Context {
	return context.WithValue(ctx, statsKey{}, s)
}

func (s *stats) addTo(field *time.Duration, elapsed time.Duration) {
	s.mu.Lock()
	*field += max(elapsed, 0)
	s.mu.Unlock()
}

func (s *stats) addRun(elapsed time.Duration)   { s.addTo(&s.run, elapsed) }
func (s *stats) addModel(elapsed time.Duration) { s.addTo(&s.model, elapsed) }
func (s *stats) addTool(elapsed time.Duration)  { s.addTo(&s.tool, elapsed) }

func (s *stats) add(other *stats) {
	if other == nil {
		return
	}
	other.mu.Lock()
	defer other.mu.Unlock()
	s.run += other.run
	s.model += other.model
	s.tool += other.tool
	s.load += other.load
	s.save += other.save
}

func (s *stats) timing(elapsed time.Duration, turns, runs int) Timing {
	gotatoTime := max(s.run-s.model-s.tool-s.load, 0)
	t := Timing{
		ElapsedMS: ms(elapsed),
		RunMS:     ms(s.run),
		ModelMS:   ms(s.model),
		ToolMS:    ms(s.tool),
		LoadMS:    ms(s.load),
		SaveMS:    ms(s.save),
		GotatoMS:  ms(gotatoTime),
	}
	if turns > 0 {
		t.GotatoPerTurnMS = t.GotatoMS / float64(turns)
	}
	if runs > 0 {
		t.LoadPerRunMS = t.LoadMS / float64(runs)
	}
	// Below a millisecond the model's time is a fake model's, and a ratio
	// against it says nothing.
	if s.model >= time.Millisecond {
		gotatoShare := t.GotatoMS / t.ModelMS
		loadShare := t.LoadMS / t.ModelMS
		t.GotatoToModel, t.LoadToModel = &gotatoShare, &loadShare
	}
	return t
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// timedStore charges the store's time to load and save a Session to the play
// whose stats the context carries. No Event times a load or a save yet, so
// this timer stands in for one.
type timedStore struct{ inner session.Store }

func (s timedStore) Save(ctx context.Context, sess *session.Session) error {
	defer charge(ctx, func(s *stats) *time.Duration { return &s.save }, time.Now())
	return s.inner.Save(ctx, sess)
}

func (s timedStore) Get(ctx context.Context, id string) (*session.Session, error) {
	defer charge(ctx, func(s *stats) *time.Duration { return &s.load }, time.Now())
	return s.inner.Get(ctx, id)
}

func (s timedStore) List(ctx context.Context) ([]session.Summary, error) { return s.inner.List(ctx) }

func (s timedStore) Delete(ctx context.Context, id string) error { return s.inner.Delete(ctx, id) }

// charge adds the time since started to one field of the play's stats, when
// ctx carries them.
func charge(ctx context.Context, field func(*stats) *time.Duration, started time.Time) {
	if s, _ := ctx.Value(statsKey{}).(*stats); s != nil {
		s.addTo(field(s), time.Since(started))
	}
}
