package session

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jinhuang712/gotato/gotato"
)

// MaxIDBytes is the longest ID, in bytes, a Store keeps.
const MaxIDBytes = 100

// MaxNameBytes is the longest file name, in bytes, a Store gives an ID (see
// the file name rule of FileStore): 200, so that the name with the suffixes the
// FileStore adds stays under the 255 bytes a file name may have. It limits the
// ID's encoded form, so an ID of fewer than MaxIDBytes bytes is refused when
// each of its bytes needs 3 of name, as the bytes of CJK characters do (33 of
// them are too long). Every Store refuses the same IDs, not only the FileStore.
const MaxNameBytes = 200

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
	case len(nameOf(id)) > MaxNameBytes:
		reason = fmt.Sprintf("its file name would be longer than %d bytes", MaxNameBytes)
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

// checkBatch checks that entries continue position after without a gap, that a
// new Record starts with a Checkpoint, and that the RecordInfo names the
// Record's epoch: with none, the fence against a writer of a deleted Record is
// off.
func checkBatch(after uint64, entries []Entry, summary RecordInfo) error {
	if summary.RecordEpoch == "" {
		return gotato.ErrorOf(gotato.CodeInvalidArgument, "session: a write names the Record's epoch (RecordInfo.RecordEpoch)")
	}
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

func sortInfos(out []RecordInfo) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
}
