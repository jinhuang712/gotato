package session

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
)

// MaxIDBytes is the longest ID, in bytes, that CheckID accepts.
const MaxIDBytes = 100

// CheckID returns ErrInvalidID, with the reason, for an ID no Store should
// keep: an empty one, one longer than MaxIDBytes, one that is not valid UTF-8
// or has a control character, one with a path separator or "..", and one that
// starts with ".". A Store calls it first, so all of them refuse these IDs
// the same way. A Store may refuse more IDs, with ErrInvalidID too: the
// FileStore refuses one whose file name would be too long, and a SQL store
// keeps every other ID Gotato makes.
func CheckID(id string) error {
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

// CheckBatch checks that entries continue position after without a gap, that a
// new Record starts with a Checkpoint, and that the Header names the Record's
// epoch: with none, the fence against a writer of a deleted Record is off. It
// returns invalid_argument for a batch that does not hold. A Store calls it
// before any write, so that all of them refuse the same batches the same way.
func CheckBatch(after uint64, entries []record.Entry, h record.Header) error {
	if h.Epoch == "" {
		return gotato.ErrorOf(gotato.CodeInvalidArgument, "session: a write names the Record's epoch (Header.Epoch)")
	}
	if len(entries) == 0 {
		return gotato.ErrorOf(gotato.CodeInvalidArgument, "session: a write has no entries")
	}
	for i, entry := range entries {
		if entry.Seq != after+uint64(i)+1 {
			return gotato.ErrorOf(gotato.CodeInvalidArgument, fmt.Sprintf("session: entry %d of a write after %d has Seq %d", i, after, entry.Seq))
		}
	}
	if after == 0 && (!entries[0].IsCheckpoint() || entries[0].Layer != record.LayerFact) {
		return gotato.ErrorOf(gotato.CodeInvalidArgument, "session: a Record must start with a checkpoint")
	}
	return nil
}

// cloneEntries copies entries, Data included, so a store and its caller never
// share bytes.
func cloneEntries(entries []record.Entry) []record.Entry {
	out := make([]record.Entry, len(entries))
	for i, entry := range entries {
		out[i] = entry
		out[i].Data = append([]byte(nil), entry.Data...)
	}
	return out
}

// loadedFrom returns what Load returns of entries: the facts from the last
// Checkpoint on and every Entry from logFrom (0 for none), each once and in
// order, and the segment_closed Entries before that Checkpoint, which the
// Checkpoint may list. It is the whole of what a Store reads of an Entry.
func loadedFrom(entries []record.Entry, logFrom uint64) (out, segments []record.Entry) {
	start := 0
	for i, entry := range slices.Backward(entries) {
		if entry.IsCheckpoint() {
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
		if entry.Layer == record.LayerFact || (logFrom != 0 && entry.Seq >= logFrom) {
			out = append(out, entry)
		}
	}
	return out, segments
}

// cloneHeader copies h, so a store and its caller never share bytes.
func cloneHeader(h record.Header) record.Header {
	h.Info = cloneInfo(h.Info)
	h.State = bytes.Clone(h.State)
	return h
}

func cloneInfo(info record.Info) record.Info {
	info.Metadata = maps.Clone(info.Metadata)
	return info
}

func sortInfos(out []record.Info) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
}

// defaultListLimit is the page MemoryStore and FileStore return when the
// query names no Limit.
const defaultListLimit = 100

// listCursor is what the reference stores' Cursor holds: the last RecordInfo
// of the page before, by the order of the listing.
type listCursor struct {
	At time.Time `json:"at"`
	ID string    `json:"id"`
}

// pageOf sorts infos, the most recently updated first, and returns the page
// q asks for. A Cursor that was not made by pageOf is invalid_argument.
func pageOf(infos []record.Info, q ListQuery) (ListPage, error) {
	sortInfos(infos)
	if q.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		var after listCursor
		if err == nil {
			err = json.Unmarshal(raw, &after)
		}
		if err != nil {
			return ListPage{}, gotato.ErrorOf(gotato.CodeInvalidArgument, "session: the list cursor is not one this store made")
		}
		i := sort.Search(len(infos), func(i int) bool {
			if infos[i].UpdatedAt.Equal(after.At) {
				return infos[i].ID > after.ID
			}
			return infos[i].UpdatedAt.Before(after.At)
		})
		infos = infos[i:]
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	page := ListPage{Infos: infos}
	if len(infos) > limit {
		page.Infos = infos[:limit]
		last := page.Infos[limit-1]
		raw, err := json.Marshal(listCursor{At: last.UpdatedAt, ID: last.ID})
		if err != nil {
			return ListPage{}, err
		}
		page.Next = base64.RawURLEncoding.EncodeToString(raw)
	}
	return page, nil
}
