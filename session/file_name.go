package session

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// nameOf is the file name a store gives an ID, so that two IDs never share a
// name, on a file system that ignores case or Unicode form too. A lowercase
// letter, a digit and "._-" stand for themselves; an uppercase letter is "!"
// and its lowercase; any other byte is "%" and two uppercase hex digits. An ID
// of lowercase ASCII is its own name.
func nameOf(id string) string {
	var b strings.Builder
	for i := range len(id) {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
			b.WriteByte(c)
		case c >= 'A' && c <= 'Z':
			b.WriteByte('!')
			b.WriteByte(c + 'a' - 'A')
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// idOf is the ID whose file name is name; ok is false for a name nameOf
// cannot have made.
func idOf(name string) (id string, ok bool) {
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
			b.WriteByte(c)
		case c == '!' && i+1 < len(name) && name[i+1] >= 'a' && name[i+1] <= 'z':
			b.WriteByte(name[i+1] - 'a' + 'A')
			i++
		case c == '%' && i+2 < len(name):
			v, err := strconv.ParseUint(name[i+1:i+3], 16, 8)
			if err != nil || strings.ToUpper(name[i+1:i+3]) != name[i+1:i+3] {
				return "", false
			}
			b.WriteByte(byte(v))
			i += 2
		default:
			return "", false
		}
	}
	return b.String(), true
}

func (f *FileStore) recordDir(id string) string { return filepath.Join(f.dir, nameOf(id)) }

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
