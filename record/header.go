package record

import (
	"encoding/json"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// Info is the listing view of a Record: the columns a store may
// index and a service lists. It carries nothing a rebuild needs; that is in the
// Header's State. A writer passes it with every write (Header.Info), so a store
// can list Sessions without reading their logs.
type Info struct {
	ID        string            `json:"id"`
	ParentID  string            `json:"parent_id,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	Messages  int               `json:"messages"`
	Runs      int               `json:"runs"`
	Usage     gotato.Usage      `json:"usage"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// Header is what a Record hands out with every batch of Entries, and what a
// load returns from the last one: the Record's identity, its listing columns
// and Gotato's own bytes. A store keeps all of it as it is, and reads none of
// it but Epoch (and LogFrom, in Load).
type Header struct {
	// Epoch is the Record's identity as a log (Record.RecordEpoch). It is
	// required: a write that names none is refused. Append compares it with
	// the stored Record's before it checks the position.
	Epoch string `json:"epoch"`
	// Info is the listing columns, and only those.
	Info Info `json:"info"`
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

// Loaded is what a store returns from one read of a Record (Records.Load), and
// what FromLoaded rebuilds the Record from: the Entries a rebuild needs, the
// segment_closed Entries before them, the position of the last Entry, and the
// Header of the last write.
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
