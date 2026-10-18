package session

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"
	"time"

	gotato "github.com/jinhuang712/gotato"
)

// summaryTime is how a Summary writes a time.
const summaryTime = "2006-01-02T15:04:05.000Z07:00"

// ErrNotFound is returned by stores for an unknown Session ID.
var ErrNotFound = errors.New("session: not found")

// ErrExists is returned by Store.Create for an ID the store already holds.
var ErrExists = errors.New("session: already exists")

// ErrConflict is returned by Store.Append when the position after its head is
// already taken, or the head is not where the writer read it: another writer
// wrote first. It carries gotato.ErrConflict.
var ErrConflict = gotato.ErrorOf(gotato.ErrConflict, "session: another writer wrote to this Record first")

// ErrExpired is returned by Store.Beat for a Liveness row that has expired.
// An expired row never comes back to life: its Instance must start again
// with a new identity.
var ErrExpired = errors.New("session: liveness row expired")

// Summary is the listing view of a stored Session. A writer passes it with
// every write, so a store can list Sessions without reading their logs.
type Summary struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	// RecordEpoch is the Record's identity as a log (Record.RecordEpoch). A
	// store keeps it with the rest of the Summary; Epoch reads it.
	RecordEpoch string            `json:"record_epoch,omitempty"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
	Messages    int               `json:"messages"`
	Runs        int               `json:"runs"`
	Usage       gotato.Usage      `json:"usage"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	// Seq is the position of the last Entry the store holds.
	Seq uint64 `json:"seq,omitempty"`
	// Running is the Run that started and has not ended, if any.
	Running gotato.RunID `json:"running,omitempty"`
	// Busy says the writer opened the Session and has not released it: a
	// Run or a change is under way, also before its Run has started.
	Busy bool `json:"busy,omitempty"`
}

// Epoch returns the Record's epoch: its own, or for a Record written before
// epochs, one made of its creation time, which also tells a Record from one
// created later under the same ID.
func (s Summary) Epoch() string {
	if s.RecordEpoch != "" {
		return s.RecordEpoch
	}
	return "created-" + s.CreatedAt
}

// Loaded is what Store.Load returns: the facts of a Record from its last
// Checkpoint on, the position of its last Entry, and its Summary.
type Loaded struct {
	Facts   []Entry
	Head    uint64
	Summary Summary
}

// InstanceRow is one Instance's Liveness row.
type InstanceRow struct {
	ID       string `json:"id"`
	Draining bool   `json:"draining,omitempty"`
}

// InstanceState is what a store knows of an Instance: whether its Liveness
// row is alive by the store's clock, and whether it is draining.
type InstanceState struct {
	ID       string `json:"id"`
	Alive    bool   `json:"alive"`
	Draining bool   `json:"draining,omitempty"`
}

// Store keeps Records as append-only logs of Entries, and one Liveness row
// per Instance. Every write is one conditional write: Create and Append write
// a batch of Entries all or nothing, and only to positions that are still
// free, so an adapter maps them to an insert on a unique (session, seq) key.
// Implementations must be safe for concurrent use, also by several Instances
// sharing one store. The conformance suite in testkit/storetest checks all of
// it.
type Store interface {
	// Create writes a new Record whose first Entry, at Seq 1, is a
	// Checkpoint (Entry.IsCheckpoint). It fails with ErrExists when the ID is taken.
	Create(ctx context.Context, id string, entries []Entry, summary Summary) error
	// Append writes entries at after+1 onward and replaces the Summary. It
	// fails with ErrConflict unless after is the Record's last position, and
	// with ErrNotFound for an unknown or deleted Record.
	Append(ctx context.Context, id string, after uint64, entries []Entry, summary Summary) error
	// Load returns the facts from the last Checkpoint on.
	Load(ctx context.Context, id string) (Loaded, error)
	// Log returns every Entry, facts and log, after position after, in order.
	Log(ctx context.Context, id string, after uint64) ([]Entry, error)
	// List returns the Summaries, the most recently updated first.
	List(ctx context.Context) ([]Summary, error)
	// Delete removes a Record. A writer that appends to it afterwards gets
	// ErrNotFound.
	Delete(ctx context.Context, id string) error
	// Beat renews an Instance's Liveness row until ttl from now, by the
	// store's clock, and creates it the first time. It fails with ErrExpired
	// once the row has expired, so a row that expired never comes back. A
	// ttl of 0 removes the row: an Instance ends its row so when it stops,
	// and never beats again, so the store keeps nothing of it.
	Beat(ctx context.Context, row InstanceRow, ttl time.Duration) error
	// Instance returns an Instance's state. An unknown or removed Instance is
	// not alive, so every Claim that names it is dead.
	Instance(ctx context.Context, id string) (InstanceState, error)
}

// StoreOption configures a built-in Store.
type StoreOption func(*storeConfig)

type storeConfig struct{ now func() time.Time }

// WithStoreClock sets the clock a store times Liveness rows with (tests).
func WithStoreClock(now func() time.Time) StoreOption {
	return func(c *storeConfig) { c.now = now }
}

func newStoreConfig(options []StoreOption) storeConfig {
	config := storeConfig{now: time.Now}
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	return config
}

// checkBatch checks that entries continue position after without a gap, and
// that a new Record starts with a Checkpoint.
func checkBatch(after uint64, entries []Entry) error {
	if len(entries) == 0 {
		return gotato.ErrorOf(gotato.ErrInvalidArgument, "session: a write has no entries")
	}
	for i, entry := range entries {
		if entry.Seq != after+uint64(i)+1 {
			return gotato.ErrorOf(gotato.ErrInvalidArgument, fmt.Sprintf("session: entry %d of a write after %d has Seq %d", i, after, entry.Seq))
		}
	}
	if after == 0 && (!entries[0].IsCheckpoint() || entries[0].Layer != LayerFact) {
		return gotato.ErrorOf(gotato.ErrInvalidArgument, "session: a Record must start with a checkpoint")
	}
	return nil
}

// cloneEntries copies entries, Data included, so a store and its caller never
// share bytes.
func cloneEntries(entries []Entry) []Entry {
	out := make([]Entry, len(entries))
	for i, entry := range entries {
		out[i] = entry
		out[i].Data = append([]byte(nil), entry.Data...)
	}
	return out
}

// factsFrom returns the facts of entries from the last Checkpoint on.
func factsFrom(entries []Entry) []Entry {
	start := 0
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].IsCheckpoint() {
			start = i
			break
		}
	}
	var facts []Entry
	for _, entry := range entries[start:] {
		if entry.Layer == LayerFact {
			facts = append(facts, entry)
		}
	}
	return facts
}

// SummaryOf builds the listing view of a Session.
func SummaryOf(s *Record) Summary { return s.Summary() }

// Summary returns the listing view without copying Messages or Events.
func (s *Record) Summary() Summary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.summaryLocked()
}

func (s *Record) summaryLocked() Summary {
	summary := summaryOf(s.id, s.parentID, s.createdAt, s.updatedAt, len(s.messages), s.baseRuns+len(s.runs), s.usage, s.metadata)
	summary.RecordEpoch = s.epochLocked()
	summary.Seq = s.head
	summary.Running = s.running
	summary.Busy = s.busy
	return summary
}

func summaryOf(id, parentID string, createdAt, updatedAt time.Time, messages, runs int, usage gotato.Usage, metadata map[string]string) Summary {
	return Summary{
		ID:        id,
		ParentID:  parentID,
		CreatedAt: createdAt.Format(summaryTime),
		UpdatedAt: updatedAt.Format(summaryTime),
		Messages:  messages,
		Runs:      runs,
		Usage:     usage,
		Metadata:  maps.Clone(metadata),
	}
}

func sortSummaries(out []Summary) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
}
