package session

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
// it.
type Store interface {
	// Create writes a new Record whose first Entry, at Seq 1, is a
	// Checkpoint (Entry.IsCheckpoint). It fails with ErrExists when the ID is
	// taken.
	Create(ctx context.Context, id string, entries []Entry, summary RecordInfo) error
	// Append writes entries at after+1 onward and replaces the RecordInfo. The
	// RecordInfo names the epoch of the Record the writer loaded
	// (RecordInfo.RecordEpoch). It fails with ErrNotFound for an unknown or deleted
	// Record, and for a Record whose epoch is not the RecordInfo's: the Record
	// was deleted and created again under the ID, and its writer is of the
	// old one. That is checked before the position. It fails with ErrConflict
	// unless after is the Record's last position.
	Append(ctx context.Context, id string, after uint64, entries []Entry, summary RecordInfo) error
	// Load returns the facts from the last Checkpoint on, and the
	// segment_closed Entries before it (Loaded.Segments), which that
	// Checkpoint may list by number. An adapter reads the Entry's Kind for
	// them, as it reads it for the Checkpoint.
	Load(ctx context.Context, id string) (Loaded, error)
	// Log returns the Entries, facts and log, after position after, in order:
	// at most limit of them, or all when limit is 0 or less. A caller that
	// needs one Entry, or one page, passes a limit so that the Record is not
	// read to its end.
	Log(ctx context.Context, id string, after uint64, limit int) ([]Entry, error)
	// List returns the RecordInfo of every Record, the most recently updated first.
	List(ctx context.Context) ([]RecordInfo, error)
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

// MaxIDBytes is the longest ID, in bytes, a Store keeps.
const MaxIDBytes = 100

// checkID returns ErrInvalidID, with the reason, for an ID a Store cannot keep:
// an empty one, one longer than MaxIDBytes, one that is not valid UTF-8 or has
// a control character, one with a path separator or "..", and one that starts
// with ".". Every store calls it first, so all of them refuse the same IDs the
// same way.
func checkID(id string) error {
	reason := ""
	switch {
	case id == "":
		reason = "it is empty"
	case len(id) > MaxIDBytes:
		reason = fmt.Sprintf("it is longer than %d bytes", MaxIDBytes)
	case !utf8.ValidString(id):
		reason = "it is not valid UTF-8"
	case strings.IndexFunc(id, unicode.IsControl) >= 0:
		reason = "it has a control character"
	case strings.ContainsAny(id, `/\`):
		reason = "it has a path separator"
	case strings.Contains(id, ".."):
		reason = `it has ".."`
	case strings.HasPrefix(id, "."):
		reason = `it starts with "."`
	}
	if reason == "" {
		return nil
	}
	return fmt.Errorf("%w %q: %s", ErrInvalidID, id, reason)
}

// checkBatch checks that entries continue position after without a gap, and
// that a new Record starts with a Checkpoint.
func checkBatch(after uint64, entries []Entry) error {
	if len(entries) == 0 {
		return gotato.ErrorOf(gotato.CodeInvalidArgument, "session: a write has no entries")
	}
	for i, entry := range entries {
		if entry.Seq != after+uint64(i)+1 {
			return gotato.ErrorOf(gotato.CodeInvalidArgument, fmt.Sprintf("session: entry %d of a write after %d has Seq %d", i, after, entry.Seq))
		}
	}
	if after == 0 && (!entries[0].IsCheckpoint() || entries[0].Layer != LayerFact) {
		return gotato.ErrorOf(gotato.CodeInvalidArgument, "session: a Record must start with a checkpoint")
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

// loadedFrom returns the facts of entries from the last Checkpoint on, and
// the segment_closed Entries before that Checkpoint, which the Checkpoint
// may list. It is the whole of what a Store reads of an Entry's kind.
func loadedFrom(entries []Entry) (facts, segments []Entry) {
	start := 0
	for i, entrie := range slices.Backward(entries) {
		if entrie.IsCheckpoint() {
			start = i
			break
		}
	}
	for _, entry := range entries[:start] {
		if entry.Kind == gotato.EventKindSegmentClosed {
			segments = append(segments, entry)
		}
	}
	for _, entry := range entries[start:] {
		if entry.Layer == LayerFact {
			facts = append(facts, entry)
		}
	}
	return facts, segments
}

// Info returns the listing view without copying Messages or Events.
func (s *Record) Info() RecordInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.infoLocked()
}

func (s *Record) infoLocked() RecordInfo {
	summary := infoOf(s.id, s.parentID, s.createdAt, s.updatedAt, len(s.messages), s.baseRuns+len(s.runs), s.usage, s.metadata)
	summary.RecordEpoch = s.epoch
	summary.Seq = s.head
	summary.Running = s.running
	summary.Busy = s.busy
	summary.Notices = slices.Clone(s.notices)
	if !s.lastRunEnd.IsZero() {
		summary.LastRunEnd = s.lastRunEnd
	}
	return summary
}

func infoOf(id, parentID string, createdAt, updatedAt time.Time, messages, runs int, usage gotato.Usage, metadata map[string]string) RecordInfo {
	return RecordInfo{
		ID:        id,
		ParentID:  parentID,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
		Messages:  messages,
		Runs:      runs,
		Usage:     usage,
		Metadata:  maps.Clone(metadata),
	}
}

func sortInfos(out []RecordInfo) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
}
