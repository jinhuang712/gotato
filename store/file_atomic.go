package store

import (
	"os"
	"path/filepath"
)

// dirMode is the mode of every directory the file stores create.
const dirMode = 0o755

// writeFileAtomic replaces the file at path with data, in one rename, so a
// reader sees the old file or the new one and never half of it. It creates the
// directory of path when it is missing.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
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
