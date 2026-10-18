package session

import (
	"context"
	"errors"
	"sync"
	"time"

	gotato "github.com/jinhuang712/gotato"
)

// Writer writes one Record's Entries to a Store, as the Instance that holds
// the Record's Claim. Every change to the Record queues an Entry, and Flush
// writes what is queued in one write.
//
// A Writer is also a gotato.Transcript and a gotato.Journal: given to a live
// Session as its Transcript, it writes every step of a Run as it happens, so
// a crash loses at most the step in flight. The Run's end is written by the
// Flush that follows the Run.
//
// A Writer stops at its first failed write: a conflict means another writer
// took the Record, and any other failure leaves the outcome unknown. Every
// later Flush returns the same error, and the Record must be opened again
// from the store, which shows whether the write landed.
type Writer struct {
	store  Store
	record *Record
	guard  func() error

	mu     sync.Mutex
	failed error
	stats  WriteStats
}

// WriterOption configures Create and OpenWriter.
type WriterOption func(*writerConfig)

type writerConfig struct {
	guard  func() error
	holder func(ctx context.Context, holder Claim, busy bool) error
}

// WithGuard runs guard before every write. When it fails, the write is not
// made and the Writer stops: an Instance whose Liveness row may have expired
// must not write, since another Instance may hold the Record by now.
func WithGuard(guard func() error) WriterOption {
	return func(c *writerConfig) { c.guard = guard }
}

// WithHolderCheck has OpenWriter ask check before it claims a Record that
// another Instance holds, with that Claim and whether the holder is using the
// Session (busy, or a Run not ended). When check fails, OpenWriter returns its
// error and writes nothing.
func WithHolderCheck(check func(ctx context.Context, holder Claim, busy bool) error) WriterOption {
	return func(c *writerConfig) { c.holder = check }
}

func newWriterConfig(options []WriterOption) writerConfig {
	var config writerConfig
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	return config
}

// WriteStats counts a Writer's work with its store: the load that opened it,
// and every write since.
type WriteStats struct {
	Load    time.Duration
	Writes  int
	Entries int
	Elapsed time.Duration
	Failed  bool
}

// Create writes r to store as a new Record, held by instance, and returns its
// Writer. The first write is a Checkpoint of r with the first Claim, so r's
// Messages and metadata are its starting point. It fails with ErrExists when
// the ID is taken.
func Create(ctx context.Context, store Store, r *Record, instance string, options ...WriterOption) (*Writer, error) {
	config := newWriterConfig(options)
	if r.Head() > 0 {
		return nil, gotato.ErrorOf(gotato.ErrInvalidState, "session: the Record is stored already")
	}
	r.mu.Lock()
	r.instance = instance
	r.claim = Claim{Instance: instance, Epoch: 1}
	r.mu.Unlock()
	r.startLog()
	entries, summary, err := r.takePending()
	if err != nil {
		return nil, storeFailure(err)
	}
	w := &Writer{store: store, record: r, guard: config.guard}
	if w.guard != nil {
		if err := w.guard(); err != nil {
			return nil, err
		}
	}
	began := time.Now()
	err = store.Create(ctx, r.ID(), entries, summary)
	w.count(entries, time.Since(began), err)
	if err != nil {
		return nil, err
	}
	r.stored(entries[len(entries)-1].Seq)
	return w, nil
}

// OpenWriter loads a Record from its facts and returns a Writer for it, held
// by instance, and marks the Record busy until Release. When the Record's
// last Claim names another Instance, it asks the holder check, if any, and
// then claims the Record with the next Epoch, and a Run that started and
// never ended is ended as interrupted. When instance holds the Claim already,
// it writes a busy mark instead. Either is written before OpenWriter returns,
// so a writer that held the Record before can no longer write to it, and an
// Instance that opens it at the same moment gets a conflict.
func OpenWriter(ctx context.Context, store Store, id, instance string, options ...WriterOption) (*Writer, error) {
	config := newWriterConfig(options)
	began := time.Now()
	loaded, err := store.Load(ctx, id)
	loadTime := time.Since(began)
	if err != nil {
		return nil, err
	}
	r, err := fromLoaded(loaded)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.instance = instance
	r.logging = true
	claimed := r.claim.Instance == instance
	holder := r.claim
	running := r.running
	busy := r.busy || running != ""
	r.mu.Unlock()
	if !claimed && config.holder != nil {
		if err := config.holder(ctx, holder, busy); err != nil {
			return nil, err
		}
	}
	// The Session is busy from this write on, so another Instance waits
	// instead of claiming it before this writer's Run has started. A writer
	// that holds the Claim already writes a busy mark instead: it takes the
	// next position too, so when another Instance claims the Record at the
	// same moment, exactly one of the two writes lands, and the other sees a
	// conflict before it calls the model.
	r.mu.Lock()
	r.busy = true
	if claimed {
		r.queue(EntryBusy, LayerLog, r.claim)
	}
	r.mu.Unlock()
	if !claimed {
		r.claimFor(instance)
	}
	if running != "" {
		r.interruptRun(running)
	}
	w := &Writer{store: store, record: r, guard: config.guard}
	w.stats.Load = loadTime
	if err := w.Flush(ctx); err != nil {
		return nil, err
	}
	return w, nil
}

// Open loads a Record from its facts, for reading only: everything a Run
// needs, without its Run records and Events.
func Open(ctx context.Context, store Store, id string) (*Record, error) {
	loaded, err := store.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	return fromLoaded(loaded)
}

// View loads a Record from its whole log, for reading only: its facts, and
// its Run records, usage and Events.
func View(ctx context.Context, store Store, id string) (*Record, error) {
	entries, err := store.Log(ctx, id, 0)
	if err != nil {
		return nil, err
	}
	r, err := FromEntries(entries)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.logging = false
	r.mu.Unlock()
	return r, nil
}

// fromLoaded rebuilds a Record from its facts, with its Run count and usage
// from the Summary, since its Run records are in the log.
func fromLoaded(loaded Loaded) (*Record, error) {
	r, err := FromEntries(loaded.Facts)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.head = loaded.Head
	r.baseRuns = loaded.Summary.Runs
	r.usage = loaded.Summary.Usage
	r.running = loaded.Summary.Running
	r.busy = loaded.Summary.Busy
	r.logging = false
	return r, nil
}

// Record returns the Record the Writer writes.
func (w *Writer) Record() *Record { return w.record }

// ID returns the Record's ID, so a live Session takes it as its own.
func (w *Writer) ID() string { return w.record.ID() }

// Len implements gotato.Transcript.
func (w *Writer) Len() int { return w.record.Len() }

// Messages implements gotato.Transcript.
func (w *Writer) Messages() []gotato.Message { return w.record.Messages() }

// Append implements gotato.Transcript. The Message is written by the step
// that follows it.
func (w *Writer) Append(message gotato.Message) error { return w.record.Append(message) }

// Step implements gotato.Journal: it adds what the step needs to the log and
// writes everything queued, so the step is durable when Step returns.
func (w *Writer) Step(ctx context.Context, step gotato.Step) error {
	switch step.Kind {
	case gotato.StepModelCall:
		w.record.note(EntryModelCall, LayerLog, ModelCall{RunID: step.RunID, Turn: step.Turn})
	case gotato.StepReplyPart:
		w.record.note(EntryReplyPart, LayerLog, ReplyPart{RunID: step.RunID, Turn: step.Turn, MessageID: step.MessageID, Text: step.Text, Reasoning: step.Reasoning})
	case gotato.StepToolStart:
		w.record.note(EntryToolsStarted, LayerFact, ToolsStarted{MessageID: step.MessageID, Calls: step.Calls})
	}
	return w.Flush(ctx)
}

// writeTimeout bounds one write. A write does not end with the caller's
// context: a cancelled Run still writes its last steps.
const writeTimeout = 10 * time.Second

// Flush writes the Record's queued Entries, if any, in one write.
func (w *Writer) Flush(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failed != nil {
		return w.failed
	}
	entries, summary, err := w.record.takePending()
	if err != nil {
		w.failed = storeFailure(err)
		return w.failed
	}
	if len(entries) == 0 {
		return nil
	}
	if w.guard != nil {
		if err := w.guard(); err != nil {
			w.failed = err
			return err
		}
	}
	began := time.Now()
	err = w.store.Append(ctx, w.record.ID(), entries[0].Seq-1, entries, summary)
	w.count(entries, time.Since(began), err)
	if err != nil {
		if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrNotFound) {
			err = storeFailure(err)
		}
		w.failed = err
		return err
	}
	w.record.stored(entries[len(entries)-1].Seq)
	return nil
}

// Release writes everything queued, with a release Entry, so the Session is
// no longer busy: another Instance may then claim it without waiting.
func (w *Writer) Release(ctx context.Context) error {
	w.record.mu.Lock()
	if w.record.busy {
		w.record.busy = false
		w.record.queue(EntryRelease, LayerLog, w.record.claim)
	}
	w.record.mu.Unlock()
	return w.Flush(ctx)
}

// Since returns the writes counted after before, a WriteStats taken earlier
// from the same Writer. The load is not counted again.
func (s WriteStats) Since(before WriteStats) WriteStats {
	return WriteStats{
		Writes:  s.Writes - before.Writes,
		Entries: s.Entries - before.Entries,
		Elapsed: s.Elapsed - before.Elapsed,
		Failed:  s.Failed && !before.Failed,
	}
}

// Stats returns the Writer's work with its store so far.
func (w *Writer) Stats() WriteStats {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stats
}

func (w *Writer) count(entries []Entry, elapsed time.Duration, err error) {
	w.stats.Writes++
	w.stats.Entries += len(entries)
	w.stats.Elapsed += elapsed
	w.stats.Failed = w.stats.Failed || err != nil
}

// storeFailure reports a write that failed for another reason than a
// conflict, keeping the store's error as its cause.
func storeFailure(err error) error {
	return &gotato.RuntimeError{Code: gotato.ErrStoreFailure, Operation: "session.Writer", Message: "the store could not write the Session's Record", Cause: err}
}
