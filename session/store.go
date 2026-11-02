package session

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// RecordInfo is the listing view of a stored Session: the columns a store may
// index and a service lists. It carries nothing a rebuild needs; that is in the
// Header's State. A writer passes it with every write (Header.Info), so a store
// can list Sessions without reading their logs.
type RecordInfo struct {
	ID        string            `json:"id"`
	ParentID  string            `json:"parent_id,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	Messages  int               `json:"messages"`
	Runs      int               `json:"runs"`
	Usage     gotato.Usage      `json:"usage"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// Header is what a writer passes with every write, and what Load returns from
// the last one: the Record's identity, its listing columns and Gotato's own
// bytes. A store keeps all of it as it is, and reads none of it but Epoch (and
// LogFrom, in Load).
type Header struct {
	// Epoch is the Record's identity as a log (Record.RecordEpoch). It is
	// required: a write that names none is refused. Append compares it with
	// the stored Record's before it checks the position.
	Epoch string `json:"epoch"`
	// Info is the listing columns, and only those.
	Info RecordInfo `json:"info"`
	// State is Gotato's bytes: the Run in flight, whether the writer holds the
	// Session, the notices queued, when the last Run ended, and the counts a
	// load starts from. No store reads it. A store keeps it with the write, as
	// it keeps an Entry's Data, and Load returns it.
	State json.RawMessage `json:"state,omitempty"`
	// LogFrom is the position from which Load returns every Entry, log
	// Entries too, or 0 for none. The Writer sets it, to the position of the
	// last Checkpoint, while a Run is in flight: the parts of the reply that
	// was streaming are log Entries, and a crash leaves them for the next
	// writer to read.
	LogFrom uint64 `json:"log_from,omitempty"`
}

// Loaded is what Records.Load returns, from one read of the Record: the
// Entries a rebuild needs, the segment_closed Entries before them, the
// position of the last Entry, and the Header of the last write.
type Loaded struct {
	// Entries are the Entries of Layer fact from the last Checkpoint on, and,
	// when Header.LogFrom is not 0, every Entry at or after it, fact or log,
	// each once, in order.
	Entries []Entry
	// Segments are the segment_closed Entries from before the Checkpoint
	// Entries start at, in order. The Checkpoint lists the Segments it uses by
	// number, and these Entries hold their Messages.
	Segments []Entry
	// Seq is the position of the last Entry the store holds.
	Seq    uint64
	Header Header
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

// Store is the two seams of a Session's storage: the Records it keeps, and the
// Liveness rows of the Instances that run them. Each is an interface of its
// own, so a service may keep them in two databases. Gotato calls what each
// declares and finds an optional capability by type assertion on the field,
// as it finds Lister in Records, so a type that wraps one must pass on the
// capabilities of what it wraps. A Store has no methods and gains none.
type Store struct {
	Records  Records
	Liveness Liveness
}

// Records keeps Records as append-only logs of Entries. Every write is one
// conditional write: Create and Append write a batch of Entries all or
// nothing, and only to positions that are still free, so an adapter maps them
// to an insert on a unique (session, seq) key. Implementations must be safe
// for concurrent use, also by several Instances sharing one store. The
// conformance suite in session/storetest (RunRecords) checks all of it; its
// failure messages name the rule that broke.
//
// A store calls CheckID on every ID and CheckBatch on every write, so that
// all of them refuse the same IDs and batches the same way. It may refuse more
// IDs with ErrInvalidID: the file name rule is the FileStore's own.
//
// The rules an adapter is most likely to miss:
//
//  1. Load returns, in one read of the Record, the facts from the last
//     Checkpoint on and every Entry from Header.LogFrom; Log returns every
//     Entry, facts and log, in order.
//  2. A write continues the log: its Entries have the Seqs after+1, after+2
//     and so on, and a Record starts with a Checkpoint at Seq 1. A batch that
//     does not is refused with CodeInvalidArgument; an Append whose after is
//     not the Record's last position is refused with ErrConflict.
//  3. Header.Epoch is required on Create and Append: a write that names none
//     is refused with CodeInvalidArgument. Append checks it before the
//     position. An epoch that is not the stored Record's is ErrNotFound, not
//     ErrConflict: the Record was deleted and created again, and the writer is
//     of the old one.
//  4. Delete of an unknown Record is ErrNotFound, and so is every later
//     Append, Load and Log of it.
//
// A Writer calls Append under the lock that orders its writes, so an adapter
// must not call back into the Session or the Writer from a method.
//
// How Records grows, for those who write an adapter:
//
//   - Records gains no methods after 0.3.0. A later capability is an optional
//     interface that a caller discovers by type assertion, as Lister is and
//     Journal.Durable is, so an adapter written today keeps compiling and
//     working.
//   - What Load, Log and List return is the caller's to read and the store's
//     to keep: a caller must not modify it, and an adapter with a cache may
//     return it without a copy. MemoryStore returns copies.
//   - RecordInfo, Header and Entry gain fields. An adapter that maps them to
//     columns must round-trip every field, also the ones it was written
//     before; the suite fails an adapter that drops one, and storing the whole
//     value (its JSON) next to the columns it indexes keeps a later field.
type Records interface {
	// Create writes a new Record whose first Entry, at Seq 1, is a
	// Checkpoint (Entry.IsCheckpoint). It fails with ErrExists when the ID is
	// taken. An error other than ErrExists leaves the outcome unknown: the
	// Record may exist, as when a write was made but not confirmed. Create is
	// not idempotent, so a caller that retries after such an error must
	// tolerate ErrExists, which then may be its own Record.
	Create(ctx context.Context, id string, entries []Entry, h Header) error
	// Append writes entries at after+1 onward and replaces the Header. The
	// Header names the epoch of the Record the writer loaded (Header.Epoch).
	// It fails with ErrNotFound for an unknown or deleted Record, and for a
	// Record whose epoch is not the Header's: the Record was deleted and
	// created again under the ID, and its writer is of the old one. That is
	// checked before the position. It fails with ErrConflict unless after is
	// the Record's last position.
	Append(ctx context.Context, id string, after uint64, entries []Entry, h Header) error
	// Load returns, from one read of the Record, the Entries of Layer fact
	// from the last Checkpoint on, and every Entry from the Header's LogFrom
	// (Loaded.Entries); the segment_closed Entries before that Checkpoint
	// (Loaded.Segments), which it may list by number; the last position; and
	// the Header of the last write. An adapter reads the Entry's Kind for the
	// Segments, as it reads it for the Checkpoint.
	Load(ctx context.Context, id string) (Loaded, error)
	// Log returns the Entries, facts and log, after position after, in order:
	// at most limit of them, or all when limit is 0 or less. A caller that
	// needs one Entry, or one page, passes a limit so that the Record is not
	// read to its end.
	Log(ctx context.Context, id string, after uint64, limit int) ([]Entry, error)
	// Delete removes a Record, and fails with ErrNotFound when there is none.
	// A writer that appends to it afterwards gets ErrNotFound.
	Delete(ctx context.Context, id string) error
}

// ListQuery asks a Lister for one page of Records.
type ListQuery struct {
	// Limit is the most RecordInfos the page holds. 0 or less leaves the size
	// to the store; a store may also return fewer than Limit.
	Limit int
	// Cursor is the Next of the page before, or empty for the first page.
	Cursor string
}

// ListPage is one page of Records, the most recently updated first.
type ListPage struct {
	Infos []RecordInfo `json:"sessions"`
	// Next is the Cursor of the page that follows, or empty after the last
	// page. A Record updated while a caller pages may repeat or be skipped.
	Next string `json:"next,omitempty"`
}

// Lister is the optional capability of Records to list what it keeps, the most
// recently updated first, a page at a time. The runtime never lists; a service
// that wants Runner.Sessions has a store that does. A caller finds it with
// store.Records.(Lister), and without it Sessions is not_supported. A Cursor is
// opaque to the caller; a store that cannot read one fails with
// CodeInvalidArgument. Filters are not part of it: a service queries its own
// columns.
type Lister interface {
	List(ctx context.Context, q ListQuery) (ListPage, error)
}

// Liveness is the half of a Store that keeps one row per Instance. A caller
// that only reads or beats rows takes a Liveness. The rules an adapter is most
// likely to miss, which the suite (RunLiveness) checks:
//
//  1. Beat keeps an expired row, so that the next Beat returns ErrExpired and
//     the row never comes back. Only a ttl of 0 removes a row, and a Beat with
//     a ttl of 0 on a missing row is not an error.
//  2. Instance of an unknown ID is not an error: it is a state that is not
//     alive.
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
