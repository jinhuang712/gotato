package session

import "github.com/jinhuang712/gotato/gotato"

var (
	// ErrNotFound is returned by stores for an unknown Session ID.
	ErrNotFound = gotato.ErrorOf(gotato.CodeNotFound, "session: not found")

	// ErrExists is returned by Store.Create for an ID the store already holds.
	ErrExists = gotato.ErrorOf(gotato.CodeInvalidState, "session: already exists")

	// ErrOldLayout is returned by the file store for an ID held in a layout of an
	// earlier version, which it refuses to read. Gotato does not migrate it: the
	// error names the path, and the team moves it away or deletes it.
	ErrOldLayout = gotato.ErrorOf(gotato.CodeNotSupported, "session: the file store holds this Record in an earlier layout, which is not read")

	// ErrConflict is returned by Store.Append when the position after its head is
	// already taken, or the head is not where the writer read it: another writer
	// wrote first. It carries gotato.CodeConflict.
	ErrConflict = gotato.ErrorOf(gotato.CodeConflict, "session: another writer wrote to this Record first")

	// ErrExpired is returned by Store.Beat for a Liveness row that has expired.
	// An expired row never comes back to life: its Instance must start again
	// with a new identity.
	ErrExpired = gotato.ErrorOf(gotato.CodeInvalidState, "session: liveness row expired")
)
