package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// FileDefinitions is a Definitions store that keeps each document as the file
// <dir>/<kind>/<name>.json, with kind and name encoded by the same rule as the
// FileStore's file names (so keys that differ only by case stay apart on a
// case-insensitive file system). A save replaces the file in one rename, so a
// reader sees the old document or the new one. The file holds the bytes as
// they were written, whether or not they are JSON. Handles on one dir see each
// other's writes.
type FileDefinitions struct {
	dir string
}

// NewFileDefinitions returns a FileDefinitions that keeps its documents under
// dir. It creates dir when it is not there.
func NewFileDefinitions(dir string) (*FileDefinitions, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &FileDefinitions{dir: dir}, nil
}

// pathOf returns the file of (kind, name). A key whose encoded form would make
// a file name longer than maxNameBytes is ErrInvalidID, the FileStore's rule.
func (f *FileDefinitions) pathOf(kind, name string) (string, error) {
	if err := CheckDefinitionKey(kind, name); err != nil {
		return "", err
	}
	k, n := nameOf(kind), nameOf(name)
	if len(k) > maxNameBytes || len(n) > maxNameBytes {
		return "", fmt.Errorf("%w: definition (%q, %q): its file name would be longer than %d bytes", ErrInvalidID, kind, name, maxNameBytes)
	}
	return filepath.Join(f.dir, k, n+".json"), nil
}

// SaveDefinition writes doc to the file of (kind, name), atomically.
func (f *FileDefinitions) SaveDefinition(_ context.Context, kind, name string, doc []byte) error {
	path, err := f.pathOf(kind, name)
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
	if _, err := tmp.Write(doc); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// LoadDefinition reads the file of (kind, name), or returns ErrNotFound.
func (f *FileDefinitions) LoadDefinition(_ context.Context, kind, name string) ([]byte, error) {
	path, err := f.pathOf(kind, name)
	if err != nil {
		return nil, err
	}
	doc, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return doc, err
}
