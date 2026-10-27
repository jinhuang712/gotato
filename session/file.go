package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// FileStore keeps each Record in a directory of its own epoch, one file per
// write, named by the write's first position:
//
//	<dir>/<id>/<epoch>/00000000000000000001.json   {"entries":[…],"info":{…}}
//	<dir>/<id>/<epoch>/00000000000000000003.json
//	<dir>/.instances/<instance>.json               one Liveness row per Instance
//
// <epoch> is a hash of the Record's epoch. A write goes to a temporary file
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

	// beforeWrite, when set, runs at the start of writeBatch, after Append has
	// checked the writer: a test seam that holds a write in that window.
	beforeWrite func()
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

// fileBatch is one file of a Record: the Entries of one write, and the
// RecordInfo after it.
type fileBatch struct {
	Entries []Entry    `json:"entries"`
	Info    RecordInfo `json:"info"`
}

type fileInstance struct {
	Row     InstanceRow `json:"row"`
	Expires time.Time   `json:"expires"`
}

const batchNameDigits = 20

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
	return &FileStore{dir: dir, now: time.Now, heads: map[string]fileHead{}}, nil
}

// Dir returns the directory.
func (f *FileStore) Dir() string { return f.dir }

func validID(id string) bool {
	return id != "" && !strings.ContainsAny(id, `/\`) && !strings.Contains(id, "..") && !strings.HasPrefix(id, ".")
}

func (f *FileStore) recordDir(id string) string { return filepath.Join(f.dir, id) }

// epochDir is the directory of the Record created under id with this epoch.
func (f *FileStore) epochDir(id, epoch string) string {
	sum := sha256.Sum256([]byte(epoch))
	return filepath.Join(f.recordDir(id), hex.EncodeToString(sum[:8]))
}

// currentDir finds the epoch directory of the Record under id: ErrNotFound
// when there is none.
func (f *FileStore) currentDir(id string) (string, error) {
	names, err := os.ReadDir(f.recordDir(id))
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	var current string
	for _, name := range names {
		switch {
		case strings.HasPrefix(name.Name(), "."):
		case !name.IsDir():
			return "", fmt.Errorf("session: unexpected file %s in the file store", filepath.Join(f.recordDir(id), name.Name()))
		case current != "":
			return "", fmt.Errorf("session: %s holds more than one Record", f.recordDir(id))
		default:
			current = filepath.Join(f.recordDir(id), name.Name())
		}
	}
	if current == "" {
		return "", ErrNotFound
	}
	return current, nil
}

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

// readAll reads every write of a Record.
func (f *FileStore) readAll(id string) ([]fileBatch, error) {
	return f.readAfter(id, 0)
}

// readAfter reads the writes of a Record that hold a position after after. A
// write is named by its first position, so the files that end at or before
// after are not opened: reading one spilled body does not read the Record.
func (f *FileStore) readAfter(id string, after uint64) ([]fileBatch, error) {
	if !validID(id) {
		return nil, ErrNotFound
	}
	dir, err := f.currentDir(id)
	if err != nil {
		return nil, err
	}
	firsts, err := batchFirsts(dir)
	if err != nil {
		return nil, err
	}
	if len(firsts) == 0 {
		return nil, ErrNotFound
	}
	batches := make([]fileBatch, 0, len(firsts))
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
		batches = append(batches, batch)
	}
	return batches, nil
}

func lastSeq(batches []fileBatch) uint64 {
	if len(batches) == 0 {
		return 0
	}
	entries := batches[len(batches)-1].Entries
	return entries[len(entries)-1].Seq
}

// Create implements Store.
func (f *FileStore) Create(ctx context.Context, id string, entries []Entry, summary RecordInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validID(id) {
		return ErrNotFound
	}
	if err := checkBatch(0, entries); err != nil {
		return err
	}
	if _, err := f.currentDir(id); !errors.Is(err, ErrNotFound) {
		if err == nil {
			return ErrExists
		}
		return err
	}
	// The Record is built under a temporary name and renamed to its ID in one
	// step. A rename onto a directory that holds a Record fails, so of two
	// creators of one ID only one wins, and the other's epoch never gets a
	// directory beside the winner's.
	building, err := os.MkdirTemp(f.dir, ".creating-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(building) }()
	epochName := filepath.Base(f.epochDir(id, summary.Epoch()))
	if err := os.Mkdir(filepath.Join(building, epochName), 0o755); err != nil {
		return err
	}
	if err := f.writeBatch(filepath.Join(building, epochName), fileBatch{Entries: entries, Info: summary}); err != nil {
		return err
	}
	if err := os.Rename(building, f.recordDir(id)); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrExists
		}
		return err
	}
	if err := syncDir(f.dir); err != nil {
		return err
	}
	dir := f.epochDir(id, summary.Epoch())
	f.setHead(id, fileHead{head: entries[len(entries)-1].Seq, first: entries[0].Seq, dir: dir})
	return nil
}

// Append implements Store.
func (f *FileStore) Append(ctx context.Context, id string, after uint64, entries []Entry, summary RecordInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validID(id) {
		return ErrNotFound
	}
	if err := checkBatch(after, entries); err != nil {
		return err
	}
	// The batch goes into the directory of the writer's own epoch. A Record
	// deleted, or deleted and created again under this ID, has no such
	// directory, and the write fails with ErrNotFound, however late: the
	// directory is the check, and nothing is left between it and the write.
	dir := f.epochDir(id, summary.Epoch())
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

// Load implements Store.
func (f *FileStore) Load(ctx context.Context, id string) (Loaded, error) {
	if err := ctx.Err(); err != nil {
		return Loaded{}, err
	}
	batches, err := f.readAll(id)
	if err != nil {
		return Loaded{}, err
	}
	var entries []Entry
	for _, batch := range batches {
		entries = append(entries, batch.Entries...)
	}
	head := lastSeq(batches)
	facts, segments := loadedFrom(entries)
	return Loaded{Facts: facts, Segments: segments, Head: head, Info: batches[len(batches)-1].Info}, nil
}

// Log implements Store.
func (f *FileStore) Log(ctx context.Context, id string, after uint64) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	batches, err := f.readAfter(id, after)
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
		id := name.Name()
		if strings.HasPrefix(id, ".") {
			continue
		}
		if !name.IsDir() {
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

// info reads a Record's RecordInfo from its last write.
func (f *FileStore) info(id string) (RecordInfo, error) {
	batches, err := f.readAll(id)
	if err != nil {
		return RecordInfo{}, err
	}
	return batches[len(batches)-1].Info, nil
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
	return InstanceState{ID: id, Alive: f.now().Before(current.Expires), Draining: current.Row.Draining, Address: current.Row.Address}, nil
}
