package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type fileInstance struct {
	Row     InstanceRow `json:"row"`
	Expires time.Time   `json:"expires"`
}

func (f *FileStore) instancePath(id string) string {
	return filepath.Join(f.dir, ".instances", nameOf(id)+".json")
}

// Beat implements Store.
func (f *FileStore) Beat(ctx context.Context, row InstanceRow, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkID(row.ID); err != nil {
		return err
	}
	// Beat has a lock of its own: f.mu guards the head cache that every
	// Append reads, and a heartbeat on a slow disk must not hold it.
	f.beatMu.Lock()
	defer f.beatMu.Unlock()
	if f.duringBeat != nil {
		f.duringBeat()
	}
	path := f.instancePath(row.ID)
	if ttl <= 0 {
		// The Instance ends its row: nothing of it is left.
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	now := f.now()
	if current, err := readInstance(path); err == nil && !now.Before(current.Expires) {
		return ErrExpired
	}
	return writeInstance(path, fileInstance{Row: row, Expires: now.Add(ttl)})
}

// writeInstance replaces the file at path with row, in one rename, so a reader
// sees the old row or the new one and never half of it.
func writeInstance(path string, row fileInstance) error {
	data, err := json.Marshal(row)
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
	if checkID(id) != nil {
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
