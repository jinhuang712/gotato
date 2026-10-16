package workload

import (
	"sync"
	"time"
)

// stats sums one play's time by where it was spent, from the play's Events.
type stats struct {
	mu                           sync.Mutex
	run, model, tool, load, save time.Duration
}

func (s *stats) addTo(field *time.Duration, elapsed time.Duration) {
	s.mu.Lock()
	*field += max(elapsed, 0)
	s.mu.Unlock()
}

func (s *stats) addRun(elapsed time.Duration)   { s.addTo(&s.run, elapsed) }
func (s *stats) addModel(elapsed time.Duration) { s.addTo(&s.model, elapsed) }
func (s *stats) addTool(elapsed time.Duration)  { s.addTo(&s.tool, elapsed) }
func (s *stats) addLoad(elapsed time.Duration)  { s.addTo(&s.load, elapsed) }
func (s *stats) addSave(elapsed time.Duration)  { s.addTo(&s.save, elapsed) }

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
