package session

import "github.com/jinhuang712/gotato/gotato"

var (
	// ErrNotFound is returned by stores for an unknown or deleted Session ID, and
	// by Store.Append for a Record that was deleted and created again.
	ErrNotFound = gotato.ErrorOf(gotato.CodeNotFound, "session: not found")

	// ErrInvalidID is returned by a Store for a Session ID or an Instance ID it
	// cannot keep: see MaxIDBytes. It carries gotato.CodeInvalidArgument, and
	// every store refuses the same IDs.
	ErrInvalidID = gotato.ErrorOf(gotato.CodeInvalidArgument, "session: invalid id")

	// ErrExists is returned by Store.Create for an ID the store already holds.
	ErrExists = gotato.ErrorOf(gotato.CodeInvalidState, "session: already exists")

	// ErrConflict is returned by Store.Append when the position after its head is
	// already taken, or the head is not where the writer read it: another writer
	// wrote first. It carries gotato.CodeConflict.
	ErrConflict = gotato.ErrorOf(gotato.CodeConflict, "session: another writer wrote to this Record first")

	// ErrExpired is returned by Store.Beat for a Liveness row that has expired.
	// An expired row never comes back to life: its Instance must start again
	// with a new identity.
	ErrExpired = gotato.ErrorOf(gotato.CodeInvalidState, "session: liveness row expired")
)
