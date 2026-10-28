package session

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// leftoverAge is how old a crash leftover must be before a store removes it.
// A leftover is a ".creating-*" or ".deleted-*" directory of the store's
// directory, or a ".tmp-*" file of an epoch directory or of ".instances", by
// its modification time. Several processes can share one directory, and a
// live Create, Delete or write finishes in seconds, so a day is far longer
// than any of them, and a leftover costs little disk meanwhile. A Record is
// never named like a leftover: an ID cannot start with ".".
const leftoverAge = 24 * time.Hour

// sweepDir removes the entries of dir whose names start with one of prefixes
// and that were last modified more than age ago. It is best effort: another
// process may remove an entry first, and an error is ignored.
func sweepDir(dir string, age time.Duration, prefixes ...string) {
	names, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	sweepEntries(dir, names, age, prefixes...)
}

func sweepEntries(dir string, names []os.DirEntry, age time.Duration, prefixes ...string) {
	for _, name := range names {
		if !slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(name.Name(), p) }) {
			continue
		}
		if info, err := name.Info(); err != nil || time.Since(info.ModTime()) < age {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, name.Name()))
	}
}
