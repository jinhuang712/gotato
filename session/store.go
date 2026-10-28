package session

import (
	"context"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// RecordInfo is the listing view of a stored Session. A writer passes it with
// every write, so a store can list Sessions without reading their logs.
type RecordInfo struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	// RecordEpoch is the Record's identity as a log (Record.RecordEpoch). A
	// store keeps it with the rest of the RecordInfo.
	RecordEpoch string            `json:"record_epoch,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
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
	// LastRunEnd is when the last Run that ended did, zero when none has
	// (Record.LastRunEnd). The summary keeps it so that a Record loaded from
	// its facts, whose Run records stay in the log, still knows it.
	LastRunEnd time.Time `json:"last_run_end,omitzero"`
	// Notices are the notices queued and neither committed nor dropped
	// (Record.PendingNotices), so that the next Run on any instance can queue
	// them again.
	Notices []gotato.PendingNotice `json:"notices,omitempty"`
}

// Loaded is what Store.Load returns: the facts of a Record from its last
// Checkpoint on, the segment_closed Entries before it, the position of its
// last Entry, and its RecordInfo.
type Loaded struct {
	Facts []Entry
	// Segments are the segment_closed Entries from before the Checkpoint
	// Facts start at, in order. The Checkpoint lists the Segments it uses by
	// number, and these Entries hold their Messages.
	Segments []Entry
	Head     uint64
	Info     RecordInfo
}

// InstanceRow is one Instance's Liveness row.
type InstanceRow struct {
	ID       string `json:"id"`
	Draining bool   `json:"draining,omitempty"`
	// Address is where a caller can reach this Instance's own endpoint, if its
	// operator set one. Gotato only carries it: a message that reaches another
	// Instance is answered with the writer's Address (not_writer).
	Address string `json:"address,omitempty"`
}

// InstanceState is what a store knows of an Instance: whether its Liveness
// row is alive by the store's clock, whether it is draining, and the Address
// it advertises.
type InstanceState struct {
	ID       string `json:"id"`
	Alive    bool   `json:"alive"`
	Draining bool   `json:"draining,omitempty"`
	Address  string `json:"address,omitempty"`
}

// Store keeps Records as append-only logs of Entries, and one Liveness row
// per Instance. Every write is one conditional write: Create and Append write
// a batch of Entries all or nothing, and only to positions that are still
// free, so an adapter maps them to an insert on a unique (session, seq) key.
// Implementations must be safe for concurrent use, also by several Instances
// sharing one store. The conformance suite in session/storetest checks all of
// it; its failure messages name the rule that broke.
//
// The rules an adapter is most likely to miss:
//
//  1. Load returns facts only (Entry.Layer is LayerFact), from the last
//     Checkpoint on; Log returns every Entry, facts and log, in order.
//  2. A write continues the log: its Entries have the Seqs after+1, after+2
//     and so on, and a Record starts with a Checkpoint at Seq 1. A batch that
//     does not is refused with CodeInvalidArgument; an Append whose after is
//     not the Record's last position is refused with ErrConflict.
//  3. RecordInfo.RecordEpoch is required on Create and Append: a write that
//     names none is refused with CodeInvalidArgument. Append checks it before
//     the position. An epoch that is not the stored Record's is ErrNotFound,
//     not ErrConflict: the Record was deleted and created again, and the
//     writer is of the old one.
//  4. Delete of an unknown Record is ErrNotFound, and so is every later
//     Append, Load and Log of it.
//  5. Beat keeps an expired Liveness row, so that the next Beat returns
//     ErrExpired and the row never comes back. Only a ttl of 0 removes a row,
//     and a Beat with a ttl of 0 on a missing row is not an error.
//  6. Instance of an unknown ID is not an error: it is a state that is not
//     alive.
//
// How the Store grows, for those who write an adapter:
//
//   - Store gains no methods after 0.2.0. A later capability is an optional
//     interface that a caller discovers by type assertion, as Journal.Durable
//     is, so an adapter written today keeps compiling and working.
//   - What Load, Log and List return is the caller's to read and the store's
//     to keep: a caller must not modify it, and an adapter with a cache may
//     return it without a copy. MemoryStore returns copies.
//   - RecordInfo, Entry and InstanceRow gain fields. An adapter that maps them
//     to columns must round-trip every field, also the ones it was written
//     before; the suite fails an adapter that drops one, and storing the whole
//     value (its JSON) next to the columns it indexes keeps a later field.
type Store interface {
	// Create writes a new Record whose first Entry, at Seq 1, is a
	// Checkpoint (Entry.IsCheckpoint). It fails with ErrExists when the ID is
	// taken. An error other than ErrExists leaves the outcome unknown: the
	// Record may exist, as when a write was made but not confirmed. Create is
	// not idempotent, so a caller that retries after such an error must
	// tolerate ErrExists, which then may be its own Record.
	Create(ctx context.Context, id string, entries []Entry, summary RecordInfo) error
	// Append writes entries at after+1 onward and replaces the RecordInfo. The
	// RecordInfo names the epoch of the Record the writer loaded
	// (RecordInfo.RecordEpoch). It fails with ErrNotFound for an unknown or deleted
	// Record, and for a Record whose epoch is not the RecordInfo's: the Record
	// was deleted and created again under the ID, and its writer is of the
	// old one. That is checked before the position. It fails with ErrConflict
	// unless after is the Record's last position.
	Append(ctx context.Context, id string, after uint64, entries []Entry, summary RecordInfo) error
	// Load returns the facts (Entries of Layer fact; log Entries are left out)
	// from the last Checkpoint on, and the segment_closed Entries before it
	// (Loaded.Segments), which that Checkpoint may list by number. An adapter
	// reads the Entry's Kind for them, as it reads it for the Checkpoint.
	Load(ctx context.Context, id string) (Loaded, error)
	// Log returns the Entries, facts and log, after position after, in order:
	// at most limit of them, or all when limit is 0 or less. A caller that
	// needs one Entry, or one page, passes a limit so that the Record is not
	// read to its end.
	Log(ctx context.Context, id string, after uint64, limit int) ([]Entry, error)
	// List returns the RecordInfo of every Record, the most recently updated first.
	List(ctx context.Context) ([]RecordInfo, error)
	// Delete removes a Record, and fails with ErrNotFound when there is none.
	// A writer that appends to it afterwards gets ErrNotFound.
	Delete(ctx context.Context, id string) error
	// Liveness is the Instance half of the Store.
	Liveness
}

// Liveness is the half of a Store that keeps one row per Instance. It is its
// own interface so that the Store's two concerns are separate, and a caller
// that only reads or beats rows takes a Liveness.
type Liveness interface {
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
