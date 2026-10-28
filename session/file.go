package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// FileStore keeps each Record in a directory of its own epoch, one file per
// write, named by the write's first position:
//
//	<dir>/<id>/<epoch>/00000000000000000001.json   {"entries":[…],"info":{…}}
//	<dir>/<id>/<epoch>/00000000000000000003.json
//	<dir>/.instances/<instance>.json               one Liveness row per Instance
//
// <id> is the file name of the ID (nameOf): an ID of lowercase ASCII is its own
// name, and any other character is spelled out, so two IDs never share a
// directory on a file system that ignores case. <epoch> is a hash of the Record's epoch. A write goes to a temporary file
// first, is synced, and is then linked to its name. Linking fails when the
// name exists, so two writers can never both write a position, also from two
// processes on one directory, and a crash never leaves a torn write behind.
// The epoch is in the path, so a writer of a Record that was deleted and
// created again under the same ID finds no directory to write into, however
// late it checks. Create builds the Record's directory under a temporary name
// and renames it to <id>, which fails when the ID is taken. Liveness rows are
// timed by this machine's clock.
type FileStore struct {
	dir string
	now func() time.Time

	mu    sync.Mutex
	heads map[string]fileHead

	beatMu sync.Mutex // orders the heartbeats of this handle; never held with mu

	// beforeWrite, when set, runs at the start of writeBatch, after Append has
	// checked the writer: a test seam that holds a write in that window.
	beforeWrite func()
	// duringBeat, when set, runs in Beat where it starts its file work: a test
	// seam that holds a heartbeat there, as a slow disk would.
	duringBeat func()
}

// fileHead is what this process last saw of a Record's last write: its last
// position, the file of that write and the epoch directory it is in. The cache
// is trusted only while that file is still in the directory. A Record deleted
// and created again has another epoch, so another directory, and never shares
// a cache entry with the old one.
type fileHead struct {
	head  uint64
	first uint64
	dir   string
}

// NewFileStore returns a store that keeps its Records under dir. It creates
// dir, with any missing parents (mode 0755), when it does not exist, so a
// service needs no setup step; an empty dir, or one that cannot be created, is
// an error.
func NewFileStore(dir string) (*FileStore, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("session: store directory is empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	sweepDir(dir, leftoverAge, ".creating-", ".deleted-")
	sweepDir(filepath.Join(dir, ".instances"), leftoverAge, ".tmp-")
	return &FileStore{dir: dir, now: time.Now, heads: map[string]fileHead{}}, nil
}

// Create implements Store.
func (f *FileStore) Create(ctx context.Context, id string, entries []Entry, summary RecordInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkID(id); err != nil {
		return err
	}
	if err := checkBatch(0, entries, summary); err != nil {
		return err
	}
	if _, err := f.currentDir(id); !errors.Is(err, ErrNotFound) {
		if err == nil {
			return ErrExists
		}
		return err
	}
	if err := f.publishRecord(id, entries, summary); err != nil {
		return err
	}
	dir := f.epochDir(id, summary.RecordEpoch)
	f.setHead(id, fileHead{head: entries[len(entries)-1].Seq, first: entries[0].Seq, dir: dir})
	return nil
}

// publishRecord writes a new Record's first batch and makes the Record appear
// under its ID.
//
// The Record is built under a temporary name and renamed to its ID in one
// step. A rename onto a directory that holds a Record fails, so of two
// creators of one ID only one wins, and the other's epoch never gets a
// directory beside the winner's.
func (f *FileStore) publishRecord(id string, entries []Entry, summary RecordInfo) error {
	building, err := os.MkdirTemp(f.dir, ".creating-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(building) }()
	epochDir := filepath.Join(building, filepath.Base(f.epochDir(id, summary.RecordEpoch)))
	if err := os.Mkdir(epochDir, 0o755); err != nil {
		return err
	}
	if err := f.writeBatch(epochDir, fileBatch{Entries: entries, Info: summary}); err != nil {
		return err
	}
	// writeBatch synced the epoch directory; building holds that directory's
	// entry, and it must be durable before the rename publishes the Record.
	if err := syncDir(building); err != nil {
		return err
	}
	if err := os.Rename(building, f.recordDir(id)); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrExists
		}
		return err
	}
	return syncDir(f.dir)
}

// Append implements Store.
func (f *FileStore) Append(ctx context.Context, id string, after uint64, entries []Entry, summary RecordInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkID(id); err != nil {
		return err
	}
	if err := checkBatch(after, entries, summary); err != nil {
		return err
	}
	// The batch goes into the directory of the writer's own epoch. A Record
	// deleted, or deleted and created again under this ID, has no such
	// directory, and the write fails with ErrNotFound, however late: the
	// directory is the check, and nothing is left between it and the write.
	dir := f.epochDir(id, summary.RecordEpoch)
	last, err := f.head(id, dir, after)
	if err != nil {
		return err
	}
	if last.head != after {
		return ErrConflict
	}
	if err := f.writeBatch(dir, fileBatch{Entries: entries, Info: summary}); err != nil {
		return err
	}
	f.setHead(id, fileHead{head: entries[len(entries)-1].Seq, first: entries[0].Seq, dir: dir})
	return nil
}

// head returns what the last write in dir says: its last position. It comes
// from this process's cache when the cache agrees with want and its file is
// still the one in the directory, and from the directory otherwise. A
// directory that is gone is ErrNotFound.
func (f *FileStore) head(id, dir string, want uint64) (fileHead, error) {
	f.mu.Lock()
	cached, ok := f.heads[id]
	f.mu.Unlock()
	if ok && cached.dir == dir && cached.head == want {
		if _, err := os.Stat(filepath.Join(dir, batchName(cached.first))); err == nil {
			return cached, nil
		}
	}
	firsts, err := batchFirsts(dir)
	if err != nil {
		return fileHead{}, err
	}
	if len(firsts) == 0 {
		return fileHead{}, ErrNotFound
	}
	path := filepath.Join(dir, batchName(firsts[len(firsts)-1]))
	last, err := readBatch(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileHead{}, ErrNotFound
	}
	if err != nil {
		return fileHead{}, err
	}
	head := fileHead{head: lastSeq([]fileBatch{last}), first: firsts[len(firsts)-1], dir: dir}
	f.setHead(id, head)
	return head, nil
}

func (f *FileStore) setHead(id string, head fileHead) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heads[id] = head
}

// Load implements Store. It reads the writes back from the last one to the
// write that holds the last Checkpoint, and from the writes before that only
// their segment_closed Entries.
func (f *FileStore) Load(ctx context.Context, id string) (Loaded, error) {
	if err := ctx.Err(); err != nil {
		return Loaded{}, err
	}
	dir, firsts, err := f.batchList(id)
	if err != nil {
		return Loaded{}, err
	}
	var tail []fileBatch
	start := 0
	for i, first := range slices.Backward(firsts) {
		batch, err := readBatch(filepath.Join(dir, batchName(first)))
		if errors.Is(err, os.ErrNotExist) {
			return Loaded{}, ErrNotFound
		}
		if err != nil {
			return Loaded{}, err
		}
		tail = append(tail, batch)
		if slices.ContainsFunc(batch.Entries, Entry.IsCheckpoint) {
			start = i
			break
		}
	}
	slices.Reverse(tail)
	entries, err := readSegments(dir, firsts[:start])
	if err != nil {
		return Loaded{}, err
	}
	for _, batch := range tail {
		entries = append(entries, batch.Entries...)
	}
	facts, segments := loadedFrom(entries)
	return Loaded{Facts: facts, Segments: segments, Head: lastSeq(tail), Info: tail[len(tail)-1].Info}, nil
}

// Log implements Store.
func (f *FileStore) Log(ctx context.Context, id string, after uint64, limit int) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, firsts, err := f.batchList(id)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for i, first := range firsts {
		if i+1 < len(firsts) && firsts[i+1] <= after+1 {
			continue
		}
		batch, err := readBatch(filepath.Join(dir, batchName(first)))
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range batch.Entries {
			if entry.Seq > after {
				out = append(out, entry)
			}
		}
		if limit > 0 && len(out) >= limit {
			return out[:limit], nil
		}
	}
	return out, nil
}

// List implements Store.
func (f *FileStore) List(ctx context.Context) ([]RecordInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	names, err := os.ReadDir(f.dir)
	if err != nil {
		return nil, err
	}
	out := make([]RecordInfo, 0, len(names))
	var unreadable []string
	for _, name := range names {
		if strings.HasPrefix(name.Name(), ".") || !name.IsDir() {
			continue
		}
		id, ok := idOf(name.Name())
		if !ok {
			continue
		}
		summary, err := f.info(id)
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
	sortInfos(out)
	return out, nil
}

// info reads a Record's RecordInfo from its last write alone.
func (f *FileStore) info(id string) (RecordInfo, error) {
	dir, firsts, err := f.batchList(id)
	if err != nil {
		return RecordInfo{}, err
	}
	info, err := readBatchInfo(filepath.Join(dir, batchName(firsts[len(firsts)-1])))
	if errors.Is(err, os.ErrNotExist) {
		return RecordInfo{}, ErrNotFound
	}
	return info, err
}

// Delete implements Store. The Record's directory is renamed away first, so
// a writer that appends meanwhile gets ErrNotFound instead of bringing it
// back.
func (f *FileStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkID(id); err != nil {
		return err
	}
	found := false
	var removeErr, syncErr error
	dir := f.recordDir(id)
	tomb := filepath.Join(f.dir, ".deleted-"+nameOf(id)+"-"+NewID()[:8])
	if err := os.Rename(dir, tomb); err == nil {
		found = true
		// The rename keeps the Record's old modification time; a sweep by
		// another process must not take the tomb for a leftover.
		now := time.Now()
		_ = os.Chtimes(tomb, now, now)
		// The rename is durable before any file goes: after a power cut the
		// Record is whole or gone, never whole but for some batches. When
		// the sync fails the tomb stays, and the sweep takes it.
		if syncErr = syncDir(f.dir); syncErr == nil {
			removeErr = os.RemoveAll(tomb)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// The Record is gone once the rename is done, whether or not its files
	// could be removed: forget it either way, so a retry says not found
	// instead of leaving a stale head behind.
	f.mu.Lock()
	delete(f.heads, id)
	f.mu.Unlock()
	if !found {
		return ErrNotFound
	}
	if syncErr != nil {
		return syncErr
	}
	if removeErr != nil {
		return fmt.Errorf("session: %s is deleted, but its files were not all removed: %w", id, removeErr)
	}
	return nil
}
