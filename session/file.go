package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FileStore keeps each Record in its own directory, <dir>/<id>/, with one
// file per write, named by the write's first position:
//
//	<dir>/<id>/00000000000000000001.json   {"entries":[…],"summary":{…}}
//	<dir>/<id>/00000000000000000003.json
//	<dir>/.instances/<instance>.json       one Liveness row per Instance
//
// A write goes to a temporary file first, is synced, and is then linked to
// its name. Linking fails when the name exists, so two writers can never both
// write a position, also from two processes on one directory, and a crash
// never leaves a torn write behind. Liveness rows are timed by this machine's
// clock. A Record of the earlier format, one document at <dir>/<id>.json, is
// read as a Checkpoint and moved into its directory by its first write.
type FileStore struct {
	dir string
	now func() time.Time

	mu    sync.Mutex
	heads map[string]uint64
}

// fileBatch is one file of a Record: the Entries of one write, and the
// Summary after it.
type fileBatch struct {
	Entries []Entry `json:"entries"`
	Summary Summary `json:"summary"`
}

type fileInstance struct {
	Row     InstanceRow `json:"row"`
	Expires time.Time   `json:"expires"`
}

const batchNameDigits = 20

// NewFileStore creates the directory if needed.
func NewFileStore(dir string, options ...StoreOption) (*FileStore, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("session: store directory is empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	config := newStoreConfig(options)
	return &FileStore{dir: dir, now: config.now, heads: map[string]uint64{}}, nil
}

// Dir returns the directory.
func (f *FileStore) Dir() string { return f.dir }

func validID(id string) bool {
	return id != "" && !strings.ContainsAny(id, `/\`) && !strings.Contains(id, "..") && !strings.HasPrefix(id, ".")
}

func (f *FileStore) recordDir(id string) string { return filepath.Join(f.dir, id) }

func (f *FileStore) legacyPath(id string) string { return filepath.Join(f.dir, id+".json") }

func batchName(first uint64) string {
	return fmt.Sprintf("%0*d.json", batchNameDigits, first)
}

// batchFirsts returns the first positions of a Record's writes, in order.
func batchFirsts(dir string) ([]uint64, error) {
	names, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
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

// writeBatch writes one batch at its first position, failing with ErrConflict
// when that position's file exists and with ErrNotFound when the Record's
// directory is gone.
func (f *FileStore) writeBatch(dir string, batch fileBatch) error {
	data, err := json.Marshal(batch)
	if err != nil {
		return err
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

// readLegacy reads a Record of the earlier format as its first write: a
// Checkpoint and a log snapshot.
func (f *FileStore) readLegacy(id string) (fileBatch, error) {
	data, err := os.ReadFile(f.legacyPath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fileBatch{}, ErrNotFound
		}
		return fileBatch{}, err
	}
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return fileBatch{}, err
	}
	record, err := Load(doc)
	if err != nil {
		return fileBatch{}, err
	}
	record.now = func() time.Time { return doc.UpdatedAt }
	record.startLog()
	entries, summary, err := record.takePending()
	if err != nil {
		return fileBatch{}, err
	}
	return fileBatch{Entries: entries, Summary: summary}, nil
}

// readAll reads every write of a Record. A legacy document is read as the
// first write, and reported, since it has no directory yet.
func (f *FileStore) readAll(id string) ([]fileBatch, bool, error) {
	if !validID(id) {
		return nil, false, ErrNotFound
	}
	dir := f.recordDir(id)
	firsts, err := batchFirsts(dir)
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(firsts) == 0) {
		legacy, legacyErr := f.readLegacy(id)
		if legacyErr != nil {
			return nil, false, legacyErr
		}
		return []fileBatch{legacy}, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	batches := make([]fileBatch, 0, len(firsts))
	for _, first := range firsts {
		batch, err := readBatch(filepath.Join(dir, batchName(first)))
		if err != nil {
			return nil, false, err
		}
		batches = append(batches, batch)
	}
	return batches, false, nil
}

func lastSeq(batches []fileBatch) uint64 {
	if len(batches) == 0 {
		return 0
	}
	entries := batches[len(batches)-1].Entries
	return entries[len(entries)-1].Seq
}

// Create implements Store.
func (f *FileStore) Create(ctx context.Context, id string, entries []Entry, summary Summary) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validID(id) {
		return ErrNotFound
	}
	if err := checkBatch(0, entries); err != nil {
		return err
	}
	if _, err := os.Stat(f.legacyPath(id)); err == nil {
		return ErrExists
	}
	dir := f.recordDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := f.writeBatch(dir, fileBatch{Entries: entries, Summary: summary}); err != nil {
		if errors.Is(err, ErrConflict) {
			return ErrExists
		}
		return err
	}
	if err := syncDir(f.dir); err != nil {
		return err
	}
	f.setHead(id, entries[len(entries)-1].Seq)
	return nil
}

// Append implements Store.
func (f *FileStore) Append(ctx context.Context, id string, after uint64, entries []Entry, summary Summary) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validID(id) {
		return ErrNotFound
	}
	if err := checkBatch(after, entries); err != nil {
		return err
	}
	dir := f.recordDir(id)
	head, err := f.head(id, after)
	if err != nil {
		return err
	}
	if head != after {
		return ErrConflict
	}
	if err := f.writeBatch(dir, fileBatch{Entries: entries, Summary: summary}); err != nil {
		return err
	}
	f.setHead(id, entries[len(entries)-1].Seq)
	return nil
}

// head returns a Record's last position, from this process's cache when it
// agrees with want, and from the directory otherwise. A legacy Record is
// moved into its directory first.
func (f *FileStore) head(id string, want uint64) (uint64, error) {
	f.mu.Lock()
	cached, ok := f.heads[id]
	f.mu.Unlock()
	if ok && cached == want {
		return cached, nil
	}
	dir := f.recordDir(id)
	firsts, err := batchFirsts(dir)
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(firsts) == 0) {
		legacy, legacyErr := f.readLegacy(id)
		if legacyErr != nil {
			return 0, legacyErr
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return 0, err
		}
		// Another process may have moved it already; its write is the same.
		if err := f.writeBatch(dir, legacy); err != nil && !errors.Is(err, ErrConflict) {
			return 0, err
		}
		if err := os.Remove(f.legacyPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		return f.head(id, want)
	}
	if err != nil {
		return 0, err
	}
	last, err := readBatch(filepath.Join(dir, batchName(firsts[len(firsts)-1])))
	if err != nil {
		return 0, err
	}
	head := lastSeq([]fileBatch{last})
	f.setHead(id, head)
	return head, nil
}

func (f *FileStore) setHead(id string, head uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heads[id] = head
}

// Load implements Store.
func (f *FileStore) Load(ctx context.Context, id string) (Loaded, error) {
	if err := ctx.Err(); err != nil {
		return Loaded{}, err
	}
	batches, legacy, err := f.readAll(id)
	if err != nil {
		return Loaded{}, err
	}
	var entries []Entry
	for _, batch := range batches {
		entries = append(entries, batch.Entries...)
	}
	head := lastSeq(batches)
	if !legacy {
		// A legacy Record is moved into its directory by its first write,
		// which a cached head would skip.
		f.setHead(id, head)
	}
	return Loaded{Facts: factsFrom(entries), Head: head, Summary: batches[len(batches)-1].Summary}, nil
}

// Log implements Store.
func (f *FileStore) Log(ctx context.Context, id string, after uint64) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	batches, _, err := f.readAll(id)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, batch := range batches {
		for _, entry := range batch.Entries {
			if entry.Seq > after {
				out = append(out, entry)
			}
		}
	}
	return out, nil
}

// List implements Store.
func (f *FileStore) List(ctx context.Context) ([]Summary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	names, err := os.ReadDir(f.dir)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]Summary, 0, len(names))
	var unreadable []string
	for _, name := range names {
		id := name.Name()
		if strings.HasPrefix(id, ".") {
			continue
		}
		if !name.IsDir() {
			legacy, ok := strings.CutSuffix(id, ".json")
			if !ok {
				continue
			}
			id = legacy
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		summary, err := f.summary(id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			unreadable = append(unreadable, id+": "+err.Error())
			continue
		}
		out = append(out, summary)
	}
	if len(unreadable) > 0 {
		return nil, fmt.Errorf("session: %d unreadable session(s): %s", len(unreadable), strings.Join(unreadable, "; "))
	}
	sortSummaries(out)
	return out, nil
}

// summary reads a Record's Summary from its last write.
func (f *FileStore) summary(id string) (Summary, error) {
	dir := f.recordDir(id)
	firsts, err := batchFirsts(dir)
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(firsts) == 0) {
		legacy, legacyErr := f.readLegacy(id)
		if legacyErr != nil {
			return Summary{}, legacyErr
		}
		return legacy.Summary, nil
	}
	if err != nil {
		return Summary{}, err
	}
	last, err := readBatch(filepath.Join(dir, batchName(firsts[len(firsts)-1])))
	if err != nil {
		return Summary{}, err
	}
	return last.Summary, nil
}

// Delete implements Store. The Record's directory is renamed away first, so
// a writer that appends meanwhile gets ErrNotFound instead of bringing it
// back.
func (f *FileStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validID(id) {
		return ErrNotFound
	}
	found := false
	dir := f.recordDir(id)
	tomb := filepath.Join(f.dir, ".deleted-"+id+"-"+NewID()[:8])
	if err := os.Rename(dir, tomb); err == nil {
		found = true
		if err := os.RemoveAll(tomb); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(f.legacyPath(id)); err == nil {
		found = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f.mu.Lock()
	delete(f.heads, id)
	f.mu.Unlock()
	if !found {
		return ErrNotFound
	}
	return syncDir(f.dir)
}

func (f *FileStore) instancePath(id string) string {
	return filepath.Join(f.dir, ".instances", id+".json")
}

// Beat implements Store.
func (f *FileStore) Beat(ctx context.Context, row InstanceRow, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validID(row.ID) {
		return ErrNotFound
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	path := f.instancePath(row.ID)
	if ttl <= 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	now := f.now()
	if current, err := readInstance(path); err == nil && !now.Before(current.Expires) {
		return ErrExpired
	}
	data, err := json.Marshal(fileInstance{Row: row, Expires: now.Add(ttl)})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func readInstance(path string) (fileInstance, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return fileInstance{}, err
	}
	var row fileInstance
	err = json.Unmarshal(data, &row)
	return row, err
}

// Instance implements Store.
func (f *FileStore) Instance(ctx context.Context, id string) (InstanceState, error) {
	if err := ctx.Err(); err != nil {
		return InstanceState{}, err
	}
	if !validID(id) {
		return InstanceState{ID: id}, nil
	}
	current, err := readInstance(f.instancePath(id))
	if errors.Is(err, os.ErrNotExist) {
		return InstanceState{ID: id}, nil
	}
	if err != nil {
		return InstanceState{}, err
	}
	return InstanceState{ID: id, Alive: f.now().Before(current.Expires), Draining: current.Row.Draining}, nil
}
