package session

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// Writer writes one Record's Entries to a Store, as the Instance that holds
// the Record's Claim. Every change to the Record queues an Entry, and Flush
// writes what is queued in one write.
//
// A Writer is also a gotato.Transcript and a gotato.Journal: given to a live
// Session as its Transcript, it writes the Events of every step of a Run as
// they happen, so a crash loses at most the step in flight, and returns them
// numbered, for the Session to send. The Events the Record queues itself, such
// as a claim or a release, are written by Flush and Release, and handed to the
// Writer's publisher, if it has one, once they are stored.
//
// A Writer stops at its first failed write: a conflict means another writer
// took the Record, and any other failure leaves the outcome unknown. Every
// later Flush returns the same error, and the Record must be opened again
// from the store, which shows whether the write landed.
type Writer struct {
	store   Store
	record  *Record
	guard   func() error
	publish func(gotato.Event)

	mu     sync.Mutex
	failed error
	stats  WriteStats
}

// WriterOption configures Create and OpenWriter.
type WriterOption func(*writerConfig)

type writerConfig struct {
	guard   func() error
	holder  func(ctx context.Context, holder Claim, busy bool) error
	publish func(gotato.Event)
	waited  bool
	idle    bool
}

// WithIdle tells Create that nothing runs against the new Record: it writes
// the Record's release in the same write as its first Claim, so another Instance
// may claim it at once. Without it, a Record is busy from Create until Release,
// as one that OpenWriter opens is.
func WithIdle() WriterOption {
	return func(c *writerConfig) { c.idle = true }
}

// WithPublisher has the Writer hand every Event that Flush, Release, Create
// and OpenWriter write to publish, in order, once its write has succeeded.
// The Events a live Session writes through Write are returned to it instead,
// and it sends them, so the Events the Writer publishes and the ones the
// Session publishes may interleave; each Event carries its number, which
// orders them. Flush, Release and OpenWriter call publish under the Writer's
// lock, which orders their writes; Create calls it after the write, before it
// returns the Writer. publish must not block, or call back into the Writer.
func WithPublisher(publish func(gotato.Event)) WriterOption {
	return func(c *writerConfig) { c.publish = publish }
}

// WithWaited tells OpenWriter that the caller waited for the Record's Writer to
// finish before it opened the Record, so the Claim says so.
func WithWaited() WriterOption {
	return func(c *writerConfig) { c.waited = true }
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

// WriteStats counts a Writer's work with its store: Load is how long the load
// that opened it took, which is set once; the other fields count every write
// since.
type WriteStats struct {
	Load    time.Duration
	Writes  int
	Entries int
	Elapsed time.Duration
	Failed  bool
}

// CreateWriter writes r to store as a new Record, held by instance, and returns its
// Writer. The first write is r's state as a session_created Event, with the
// first Claim, so r's Messages and metadata are its starting point. The Record
// is busy until Release, unless WithIdle says nothing runs against it. It fails
// with ErrExists when the ID is taken.
func CreateWriter(ctx context.Context, store Store, r *Record, instance string, options ...WriterOption) (*Writer, error) {
	config := newWriterConfig(options)
	if r.Head() > 0 {
		return nil, gotato.ErrorOf(gotato.CodeInvalidState, "session: the Record is stored already")
	}
	r.mu.Lock()
	r.instance = instance
	r.claim = Claim{}
	r.mu.Unlock()
	r.startLog()
	r.claimFor(instance, gotato.ClaimWhyNewSession)
	r.mu.Lock()
	r.busy = true
	if config.idle {
		r.busy = false
		r.emit(gotato.SessionReleased{Header: r.header(), Instance: r.claim.Instance, Epoch: r.claim.Epoch})
	}
	r.mu.Unlock()
	entries, events, summary, err := r.takePending()
	if err != nil {
		return nil, storeFailure(err)
	}
	w := &Writer{store: store, record: r, guard: config.guard, publish: config.publish}
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
	w.send(events)
	return w, nil
}

// OpenWriter loads a Record from its facts and returns a Writer for it, held
// by instance, and marks the Record busy until Release. When the Record's
// last Claim names another Instance, it asks the holder check, if any, and
// then claims the Record with the next Epoch, and a Run that started and
// never ended is ended as interrupted. When instance holds the Claim already,
// it writes the Claim again, with the same Epoch. Either is written before
// OpenWriter returns, so a writer that held the Record before can no longer
// write to it, and an Instance that opens it at the same moment gets a
// conflict.
func OpenWriter(ctx context.Context, store Store, id, instance string, options ...WriterOption) (*Writer, error) {
	config := newWriterConfig(options)
	began := time.Now()
	loaded, err := store.Load(ctx, id)
	loadTime := time.Since(began)
	if err != nil {
		return nil, err
	}
	r, err := fromLoaded(ctx, store, id, loaded)
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
	// that holds the Claim already writes it again: it takes the next
	// position too, so when another Instance claims the Record at the same
	// moment, exactly one of the two writes lands, and the other sees a
	// conflict before it calls the model.
	r.mu.Lock()
	r.busy = true
	r.mu.Unlock()
	why := gotato.ClaimWhyIdle
	switch {
	case config.waited:
		why = gotato.ClaimWhyWaited
	case busy:
		why = gotato.ClaimWhyWriterGone
	}
	r.claimFor(instance, why)
	// A reply that was streaming when the last Writer stopped is kept as the
	// Cut-off reply it had reached, as a fact, so every later load has it.
	r.mu.Lock()
	for _, message := range r.cutOff {
		r.emit(gotato.ReplyCutOff{Header: r.header(), Message: message.Clone(), Cause: "interrupted"})
	}
	r.cutOff = nil
	r.mu.Unlock()
	if running != "" {
		r.interruptRun(running)
	}
	w := &Writer{store: store, record: r, guard: config.guard, publish: config.publish}
	w.stats.Load = loadTime
	if err := w.Flush(ctx); err != nil {
		return nil, err
	}
	return w, nil
}

// Resume claims the Record again for another Run, on a Writer that released
// it and kept the Record in memory (a warm copy). It writes the Claim again
// with the same Epoch, as OpenWriter does for a Record this Instance holds,
// and marks the Record busy until Release. The write takes the next position
// only while the store's head is still the Record's, so it fails with
// ErrConflict, or ErrNotFound for a Record deleted or created again, when the
// copy is stale; the Writer has stopped then, and the Record must be opened
// again. Call Stale first to learn that without a write.
func (w *Writer) Resume(ctx context.Context) error {
	if err := w.Failed(); err != nil {
		return err
	}
	r := w.record
	r.mu.Lock()
	r.busy = true
	instance := r.instance
	r.mu.Unlock()
	r.claimFor(instance, gotato.ClaimWhyIdle)
	return w.Flush(ctx)
}

// Stale reports whether the store holds Entries after the Record's head: some
// other Instance wrote the Record since this Writer did, so a copy of it in
// memory is out of date. A Record that was deleted returns ErrNotFound. It
// reads only what follows the head, which is nothing for a copy that is
// current. It does not check the epoch: a Record deleted and created again
// with a log shorter than the head reports a copy as current, and the next
// write fails with ErrNotFound (see Resume).
func (w *Writer) Stale(ctx context.Context) (bool, error) {
	entries, err := w.store.Log(ctx, w.record.ID(), w.record.Head())
	if err != nil {
		return false, err
	}
	return len(entries) > 0, nil
}

// Open loads a Record from its facts, for reading only: everything a Run
// needs, without its Run records and Events.
func Open(ctx context.Context, store Store, id string) (*Record, error) {
	loaded, err := store.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	return fromLoaded(ctx, store, id, loaded)
}

// View loads a Record from its whole log, for reading only: its facts, and
// its Run records, usage and Events.
func View(ctx context.Context, store Store, id string) (*Record, error) {
	entries, err := store.Log(ctx, id, 0)
	if err != nil {
		return nil, err
	}
	r, err := fromEntries(entries)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.logging = false
	r.mu.Unlock()
	return r, nil
}

// fromLoaded rebuilds a Record from its facts, with its Run count and usage
// from the RecordInfo, since its Run records are in the log. When the RecordInfo says
// a Run started and never ended, the log since the last Checkpoint is read too,
// because it holds the parts of a reply that was streaming, which become a
// Cut-off reply: a crash loses at most the last part.
func fromLoaded(ctx context.Context, store Store, id string, loaded Loaded) (*Record, error) {
	entries, head := loaded.Facts, loaded.Head
	if loaded.Info.Running != "" && len(loaded.Facts) > 0 {
		tail, err := store.Log(ctx, id, loaded.Facts[0].Seq-1)
		if err != nil {
			return nil, err
		}
		if len(tail) > 0 && tail[0].IsCheckpoint() {
			entries, head = tail, max(head, tail[len(tail)-1].Seq)
		}
	}
	r, err := rebuildRecord(entries, false, loaded.Segments)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(entries) != len(loaded.Facts) {
		// The log was read for its parts only: its Runs are in the RecordInfo.
		r.runs, r.eventHead = nil, 0
	}
	r.head = head
	r.last = head
	// A load that starts at a compaction_ended Checkpoint has not read
	// session_created: what it held comes from the RecordInfo.
	if r.createdAt.IsZero() {
		r.createdAt = loaded.Info.CreatedAt
	}
	if r.parentID == "" {
		r.parentID = loaded.Info.ParentID
	}
	if r.epoch == "" {
		r.epoch = loaded.Info.RecordEpoch
	}
	r.baseRuns = loaded.Info.Runs
	r.usage = loaded.Info.Usage
	r.running = loaded.Info.Running
	r.busy = loaded.Info.Busy
	r.notices = slices.Clone(loaded.Info.Notices)
	if !loaded.Info.LastRunEnd.IsZero() {
		r.lastRunEnd = loaded.Info.LastRunEnd.UTC()
	}
	r.logging = false
	r.retainRead = true
	return r, nil
}

// SetPublisher sets the publisher, once the host knows where the Session's
// Events go. It may be called at any time: it waits for a write in progress,
// and the new publisher gets the Events of the writes after it.
func (w *Writer) SetPublisher(publish func(gotato.Event)) {
	w.mu.Lock()
	w.publish = publish
	w.mu.Unlock()
}

// Durable implements an optional method of gotato.Journal: a Writer stores.
func (w *Writer) Durable() bool { return true }

// KeepsSpilledResults implements gotato.SpillKeeper: a Writer stores every
// Entry, so the body of a spilled result can be read back.
func (w *Writer) KeepsSpilledResults() bool { return true }

// Record returns the Record the Writer writes.
func (w *Writer) Record() *Record { return w.record }

// ID returns the Record's ID, so a live Session takes it as its own.
func (w *Writer) ID() string { return w.record.ID() }

// Len implements gotato.Transcript.
func (w *Writer) Len() int { return w.record.Len() }

// LoadedTools implements gotato.LoadedToolSource over the Record.
func (w *Writer) LoadedTools() []string { return w.record.LoadedTools() }

// Messages implements gotato.Transcript.
func (w *Writer) Messages() []gotato.Message { return w.record.Messages() }

// Append implements gotato.Transcript. The Message is stored by the Event that
// carries it, which the Session writes next.
func (w *Writer) Append(message gotato.Message) error { return w.record.Append(message) }

// Write implements gotato.Journal: it numbers events as the next Events of the
// Record, writes them with everything the Record had queued, and returns every
// Event written, so the step is durable when Write returns.
func (w *Writer) Write(ctx context.Context, events []gotato.Event) ([]gotato.Event, error) {
	if _, err := w.record.Write(ctx, events); err != nil {
		return nil, err
	}
	return w.flush(ctx, false)
}

// EventsAfter implements gotato.EventSource: the stored Events after number
// after, read again from the store. A subscriber that fell behind catches up
// from it.
func (w *Writer) EventsAfter(ctx context.Context, after uint64, _ int) ([]gotato.Event, error) {
	return StoredEvents(ctx, w.store, w.record.ID(), after)
}

// StoredEvents reads a Record's stored Events after number after from store,
// in order, as typed Events. A tool_result_spilled Event comes
// without its body, as a subscriber sees it: read_result reads the body.
func StoredEvents(ctx context.Context, store Store, id string, after uint64) ([]gotato.Event, error) {
	entries, err := store.Log(ctx, id, after)
	if err != nil {
		return nil, err
	}
	events := make([]gotato.Event, 0, len(entries))
	for _, entry := range entries {
		event, ok, err := eventOf(entry, gotato.SessionID(id))
		if err != nil {
			return nil, err
		}
		if ok {
			events = append(events, gotato.WithoutBody(event))
		}
	}
	return events, nil
}

// writeTimeout bounds one write. A write does not end with the caller's
// context: a canceled Run still writes its last steps.
const writeTimeout = 10 * time.Second

// Flush writes the Record's queued Events, if any, in one write, and hands
// them to the publisher.
func (w *Writer) Flush(ctx context.Context) error {
	_, err := w.flush(ctx, true)
	return err
}

func (w *Writer) send(events []gotato.Event) {
	w.mu.Lock()
	publish := w.publish
	w.mu.Unlock()
	if publish == nil {
		return
	}
	for _, event := range events {
		publish(gotato.WithoutBody(event))
	}
}

// flush writes the Record's queued Events in one write and returns them. With
// publish, it hands them to the publisher before it lets go of the lock that
// orders writes, so Events are published in the order they were written.
func (w *Writer) flush(ctx context.Context, publish bool) ([]gotato.Event, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failed != nil {
		return nil, w.failed
	}
	entries, events, summary, err := w.record.takePending()
	if err != nil {
		w.failed = storeFailure(err)
		return nil, w.failed
	}
	if len(entries) == 0 {
		return nil, nil
	}
	if w.guard != nil {
		if err := w.guard(); err != nil {
			w.failed = err
			return nil, err
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
		return nil, err
	}
	w.record.stored(entries[len(entries)-1].Seq)
	if publish && w.publish != nil {
		for _, event := range events {
			w.publish(gotato.WithoutBody(event))
		}
	}
	return events, nil
}

// Release writes everything queued, with a session_released Event, so the
// Session is no longer busy: another Instance may then claim it without
// waiting.
func (w *Writer) Release(ctx context.Context) error {
	w.record.mu.Lock()
	if w.record.busy {
		w.record.busy = false
		w.record.emit(gotato.SessionReleased{Header: w.record.header(), Instance: w.record.claim.Instance, Epoch: w.record.claim.Epoch})
	}
	w.record.mu.Unlock()
	return w.Flush(ctx)
}

// Failed returns the failure that stopped the Writer, or nil while every write
// has succeeded. A Writer stops at its first failed write and refuses the
// rest, the release of the Session among them.
func (w *Writer) Failed() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.failed
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
	failure := gotato.ErrorOf(gotato.CodeStoreFailure, "the store could not write the Session's Record")
	failure.Operation, failure.Cause = "session.Writer", err
	return failure
}
