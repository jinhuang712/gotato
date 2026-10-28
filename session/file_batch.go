package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
)

// fileBatch is one file of a Record: the Entries of one write, and the
// RecordInfo after it.
type fileBatch struct {
	Entries []Entry    `json:"entries"`
	Info    RecordInfo `json:"info"`
}

const batchNameDigits = 20

func batchName(first uint64) string {
	return fmt.Sprintf("%0*d.json", batchNameDigits, first)
}

// batchFirsts returns the first positions of a Record's writes, in order.
func batchFirsts(dir string) ([]uint64, error) {
	names, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
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
		return fileBatch{}, fmt.Errorf("session: %s: %w", filepath.Base(path), err)
	}
	return batch, nil
}

// readBatchInfo reads only the RecordInfo of a batch file. The Entries are
// scanned for syntax but not decoded, so a listing does not build a large
// batch's Entries to drop them.
func readBatchInfo(path string) (RecordInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RecordInfo{}, err
	}
	var batch struct {
		Info RecordInfo `json:"info"`
	}
	if err := json.Unmarshal(data, &batch); err != nil {
		return RecordInfo{}, fmt.Errorf("session: %s: %w", filepath.Base(path), err)
	}
	return batch.Info, nil
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
			return ErrNotFound
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
			return ErrNotFound
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
	if err := checkID(id); err != nil {
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
		return "", nil, ErrNotFound
	}
	return dir, firsts, nil
}

// segmentBatch is a write read for its segment_closed Entries alone: any other
// Entry is scanned and dropped, so a Record's long past is not held in memory.
type segmentBatch struct {
	Entries []segmentPick `json:"entries"`
}

type segmentPick struct{ entry *Entry }

func (p *segmentPick) UnmarshalJSON(data []byte) error {
	var head struct {
		Kind gotato.EventKind `json:"kind"`
	}
	if err := json.Unmarshal(data, &head); err != nil || head.Kind != gotato.EventKindSegmentClosed {
		return err
	}
	p.entry = new(Entry)
	return json.Unmarshal(data, p.entry)
}

// readSegments returns the segment_closed Entries of the writes that begin at
// firsts.
func readSegments(dir string, firsts []uint64) ([]Entry, error) {
	var out []Entry
	for _, first := range firsts {
		data, err := os.ReadFile(filepath.Join(dir, batchName(first)))
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		var batch segmentBatch
		if err := json.Unmarshal(data, &batch); err != nil {
			return nil, fmt.Errorf("session: %s: %w", batchName(first), err)
		}
		for _, pick := range batch.Entries {
			if pick.entry != nil {
				out = append(out, *pick.entry)
			}
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
