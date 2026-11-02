package session

import "github.com/jinhuang712/gotato/gotato"

var (
	// ErrInvalidID is returned by a Store for a Session ID or an Instance ID it
	// cannot keep: see CheckID. It carries gotato.CodeInvalidArgument. Every
	// store refuses the IDs CheckID does, and may refuse more.
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
