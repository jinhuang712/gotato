package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	gotato "github.com/jinhuang712/gotato"
)

// ErrNotFound is returned by stores for an unknown Session ID.
var ErrNotFound = errors.New("session: not found")

// Summary is the listing view of a stored Session.
type Summary struct {
	ID        string            `json:"id"`
	ParentID  string            `json:"parent_id,omitempty"`
	CreatedAt string            `json:"created_at"`
	UpdatedAt string            `json:"updated_at"`
	Messages  int               `json:"messages"`
	Runs      int               `json:"runs"`
	Usage     gotato.Usage      `json:"usage"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// Store persists Sessions. Implementations must be safe for concurrent use.
// Save overwrites; Get returns ErrNotFound for unknown IDs.
type Store interface {
	Save(context.Context, *Session) error
	Get(context.Context, string) (*Session, error)
	List(context.Context) ([]Summary, error)
	Delete(context.Context, string) error
}

// SummaryOf builds the listing view of a Session.
func SummaryOf(s *Session) Summary { return s.Summary() }

// Summary returns the listing view without copying Messages or Events.
func (s *Session) Summary() Summary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return summaryOf(s.id, s.parentID, s.createdAt, s.updatedAt, len(s.messages), len(s.runs), s.usage, s.metadata)
}

// summaryOfDocument builds the listing view of a persisted Document without
// materializing a Session.
func summaryOfDocument(doc Document) Summary {
	return summaryOf(doc.ID, doc.ParentID, doc.CreatedAt, doc.UpdatedAt, len(doc.Messages), len(doc.Runs), doc.Usage, doc.Metadata)
}

func summaryOf(id, parentID string, createdAt, updatedAt time.Time, messages, runs int, usage gotato.Usage, metadata map[string]string) Summary {
	return Summary{
		ID:        id,
		ParentID:  parentID,
		CreatedAt: createdAt.Format("2006-01-02T15:04:05.000Z07:00"),
		UpdatedAt: updatedAt.Format("2006-01-02T15:04:05.000Z07:00"),
		Messages:  messages,
		Runs:      runs,
		Usage:     usage,
		Metadata:  maps.Clone(metadata),
	}
}

// MemoryStore keeps Documents in memory. It is the store for library use and
// tests; nothing about the runtime requires anything more durable.
type MemoryStore struct {
	mu   sync.RWMutex
	docs map[string]Document
}

// NewMemoryStore creates an empty MemoryStore.
func NewMemoryStore() *MemoryStore { return &MemoryStore{docs: map[string]Document{}} }

// Save implements Store.
func (m *MemoryStore) Save(ctx context.Context, s *Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	doc := s.Snapshot()
	m.mu.Lock()
	m.docs[doc.ID] = doc
	m.mu.Unlock()
	return nil
}

// Get implements Store.
func (m *MemoryStore) Get(ctx context.Context, id string) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	doc, ok := m.docs[id]
	m.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	return Load(doc)
}

// List implements Store; results are sorted by UpdatedAt descending.
func (m *MemoryStore) List(ctx context.Context) ([]Summary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	out := make([]Summary, 0, len(m.docs))
	for _, doc := range m.docs {
		out = append(out, summaryOfDocument(doc))
	}
	m.mu.RUnlock()
	sortSummaries(out)
	return out, nil
}

// Delete implements Store.
func (m *MemoryStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.docs[id]; !ok {
		return ErrNotFound
	}
	delete(m.docs, id)
	return nil
}

// FileStore keeps one JSON document per Session under a directory:
// <dir>/<id>.json. Writes are atomic (temp file + rename). It needs no
// database and is the CLI's default store.
type FileStore struct {
	dir string
	mu  sync.Mutex
}

// NewFileStore creates the directory if needed.
func NewFileStore(dir string) (*FileStore, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("session: store directory is empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &FileStore{dir: dir}, nil
}

// Dir returns the directory.
func (f *FileStore) Dir() string { return f.dir }

func (f *FileStore) path(id string) (string, error) {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return "", ErrNotFound
	}
	return filepath.Join(f.dir, id+".json"), nil
}

// Save implements Store.
func (f *FileStore) Save(ctx context.Context, s *Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	doc := s.Snapshot()
	path, err := f.path(doc.ID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	tmp, err := os.CreateTemp(f.dir, "."+doc.ID+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	// Flush the directory entry so the rename itself survives a crash.
	return f.syncDir()
}

// syncDir fsyncs the store directory so a completed rename is durable.
func (f *FileStore) syncDir() error {
	dir, err := os.Open(f.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		// Some filesystems reject fsync on a directory; the rename is still
		// ordered, so treat that as non-fatal.
		if errors.Is(err, os.ErrInvalid) {
			return nil
		}
		return err
	}
	return nil
}

// Get implements Store.
func (f *FileStore) Get(ctx context.Context, id string) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	doc, err := f.readDocument(id)
	if err != nil {
		return nil, err
	}
	return Load(doc)
}

// readDocument loads one persisted Document without materializing a Session.
func (f *FileStore) readDocument(id string) (Document, error) {
	path, err := f.path(id)
	if err != nil {
		return Document{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Document{}, ErrNotFound
		}
		return Document{}, err
	}
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return Document{}, err
	}
	return doc, nil
}

// List implements Store; results are sorted by UpdatedAt descending.
func (f *FileStore) List(ctx context.Context) ([]Summary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(entries))
	var unreadable []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".") {
			continue
		}
		doc, err := f.readDocument(strings.TrimSuffix(name, ".json"))
		if err != nil {
			unreadable = append(unreadable, name+": "+err.Error())
			continue
		}
		out = append(out, summaryOfDocument(doc))
	}
	if len(unreadable) > 0 {
		return nil, fmt.Errorf("session: %d unreadable session file(s): %s", len(unreadable), strings.Join(unreadable, "; "))
	}
	sortSummaries(out)
	return out, nil
}

// Delete implements Store.
func (f *FileStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := f.path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func sortSummaries(out []Summary) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
}
