package session

// The names below left the public API; the external tests reach them here.

// SetBeforeWrite makes f call fn at the start of every write of a batch, after
// Append has checked its writer.
func SetBeforeWrite(f *FileStore, fn func()) { f.beforeWrite = fn }

// CachedHeads is the number of Records whose head f keeps.
func CachedHeads(f *FileStore) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.heads)
}

// SetDuringBeat makes f call fn in every Beat, where its file work starts.
func SetDuringBeat(f *FileStore, fn func()) { f.duringBeat = fn }
