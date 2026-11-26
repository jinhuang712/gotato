package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
)

// fileBatch is one file of a Record: the Entries of one write, and the Header
// after it.
type fileBatch struct {
	Entries []record.Entry `json:"entries"`
	Header  record.Header  `json:"header"`
}

const batchNameDigits = 20

func batchName(first uint64) string {
	return fmt.Sprintf("%0*d.json", batchNameDigits, first)
}

// batchFirsts returns the first positions of a Record's writes, in order.
func batchFirsts(dir string) ([]uint64, error) {
	names, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, record.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sweepEntries(dir, names, leftoverAge, ".tmp-")
	var firsts []uint64
	for _, name := range names {
		base, ok := strings.CutSuffix(name.Name(), ".json")
		if !ok || len(base) != batchNameDigits || name.IsDir() {
			continue
		}
		first, err := strconv.ParseUint(base, 10, 64)
		if err != nil {
			continue
		}
		firsts = append(firsts, first)
	}
	slices.Sort(firsts)
	return firsts, nil
}

func readBatch(path string) (fileBatch, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return fileBatch{}, err
	}
	var batch fileBatch
	if err := json.Unmarshal(data, &batch); err != nil {
		return fileBatch{}, fmt.Errorf("store: %s: %w", filepath.Base(path), err)
	}
	return batch, nil
}

// readBatchInfo reads only the Header's Info of a batch file. The Entries are
// scanned for syntax but not decoded, so a listing does not build a large
// batch's Entries to drop them.
func readBatchInfo(path string) (record.Info, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return record.Info{}, err
	}
	var batch struct {
		Header struct {
			Info record.Info `json:"info"`
		} `json:"header"`
	}
	if err := json.Unmarshal(data, &batch); err != nil {
		return record.Info{}, fmt.Errorf("store: %s: %w", filepath.Base(path), err)
	}
	return batch.Header.Info, nil
}

// writeBatch writes one batch at its first position, failing with ErrConflict
// when that position's file exists and with ErrNotFound when the Record's
// directory is gone.
func (f *FileStore) writeBatch(dir string, batch fileBatch) error {
	data, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	if f.beforeWrite != nil {
		f.beforeWrite()
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return record.ErrNotFound
		}
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmpName, filepath.Join(dir, batchName(batch.Entries[0].Seq))); err != nil {
		switch {
		case errors.Is(err, os.ErrExist):
			return ErrConflict
		case errors.Is(err, os.ErrNotExist):
			return record.ErrNotFound
		}
		return err
	}
	return syncDir(dir)
}

// syncDir fsyncs a directory so a completed link or rename is durable.
func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		// Some filesystems reject fsync on a directory; the link is still
		// ordered, so treat that as non-fatal.
		if errors.Is(err, os.ErrInvalid) {
			return nil
		}
		return err
	}
	return nil
}

// batchList returns the directory of a Record's current epoch and the first
// positions of its writes, in order.
func (f *FileStore) batchList(id string) (string, []uint64, error) {
	if err := checkFileID(id); err != nil {
		return "", nil, err
	}
	dir, err := f.currentDir(id)
	if err != nil {
		return "", nil, err
	}
	firsts, err := batchFirsts(dir)
	if err != nil {
		return "", nil, err
	}
	if len(firsts) == 0 {
		return "", nil, record.ErrNotFound
	}
	return dir, firsts, nil
}

// readLog returns the Entries after position after of the writes that begin at
// firsts in dir, at most limit of them when limit is positive.
func readLog(dir string, firsts []uint64, after uint64, limit int) ([]record.Entry, error) {
	var out []record.Entry
	for i, first := range firsts {
		if endsBefore(firsts, i, after) {
			continue
		}
		batch, err := readBatchFound(filepath.Join(dir, batchName(first)))
		if err != nil {
			return nil, err
		}
		out = appendAfter(out, batch.Entries, after)
		if limit > 0 && len(out) >= limit {
			return out[:limit], nil
		}
	}
	return out, nil
}

// endsBefore reports whether the write firsts[i] holds only positions up to
// after, because the next write begins at or before after+1.
func endsBefore(firsts []uint64, i int, after uint64) bool {
	return i+1 < len(firsts) && firsts[i+1] <= after+1
}

// appendAfter appends to out the Entries whose position is past after.
func appendAfter(out, entries []record.Entry, after uint64) []record.Entry {
	for _, entry := range entries {
		if entry.Seq > after {
			out = append(out, entry)
		}
	}
	return out
}

// readBatchFound reads the write at path, and returns ErrNotFound when the file is gone.
func readBatchFound(path string) (fileBatch, error) {
	batch, err := readBatch(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileBatch{}, record.ErrNotFound
	}
	return batch, err
}

// segmentBatch is a write read for its segment_closed Entries alone: any other
// Entry is scanned and dropped, so a Record's long past is not held in memory.
type segmentBatch struct {
	Entries []segmentPick `json:"entries"`
}

type segmentPick struct{ entry *record.Entry }

func (p *segmentPick) UnmarshalJSON(data []byte) error {
	var head struct {
		Kind gotato.EventKind `json:"kind"`
	}
	if err := json.Unmarshal(data, &head); err != nil || head.Kind != gotato.EventKindSegmentClosed {
		return err
	}
	p.entry = new(record.Entry)
	return json.Unmarshal(data, p.entry)
}

// readSegments returns the segment_closed Entries of the writes that begin at
// firsts.
func readSegments(dir string, firsts []uint64) ([]record.Entry, error) {
	var out []record.Entry
	for _, first := range firsts {
		picked, err := readSegmentBatch(dir, first)
		if err != nil {
			return nil, err
		}
		out = append(out, picked...)
	}
	return out, nil
}

// readSegmentBatch returns the segment_closed Entries of the write that begins at first.
func readSegmentBatch(dir string, first uint64) ([]record.Entry, error) {
	data, err := os.ReadFile(filepath.Join(dir, batchName(first)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, record.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var batch segmentBatch
	if err := json.Unmarshal(data, &batch); err != nil {
		return nil, fmt.Errorf("store: %s: %w", batchName(first), err)
	}
	var out []record.Entry
	for _, pick := range batch.Entries {
		if pick.entry != nil {
			out = append(out, *pick.entry)
		}
	}
	return out, nil
}

func lastSeq(batches []fileBatch) uint64 {
	if len(batches) == 0 {
		return 0
	}
	entries := batches[len(batches)-1].Entries
	return entries[len(entries)-1].Seq
}
