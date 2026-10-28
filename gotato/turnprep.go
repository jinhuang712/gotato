package gotato

import (
	"context"
)

// TurnPreparer is an Extension stage that runs inside the Session goroutine
// before every model call, with no Tool call open: before the first call of a
// Run, before each call after a Tool batch, and once more when a call was
// refused as too long (PendingTurn.Overflow). It is the one safe point to
// rewrite the Transcript, for example to compact a Session that has grown past
// its window, because no Turn is using it. The policy (when to rewrite) stays
// with the preparer; the hook only gives it the chance.
//
// A preparer that rewrote the Transcript returns TurnPrepared{Changed: true},
// and the Loop builds the request again from it. An error ends the Run; one
// that is a *RuntimeError ends it with that code, any other ends it
// with CodeExtensionFailure.
type TurnPreparer interface {
	PrepareTurn(context.Context, PendingTurn) (TurnPrepared, error)
}

// PendingTurn is what a TurnPreparer is given about the model call that is
// about to be made.
type PendingTurn struct {
	SessionID SessionID
	RunID     RunID
	Turn      TurnNumber
	// Transcript is the Session's Transcript. A preparer may rewrite it, and
	// must say so in TurnPrepared.
	Transcript Transcript
	// RunStart is how many Messages the Transcript held when the Run began:
	// the Run's own Messages follow them. A rewrite changes the Transcript,
	// so afterwards the Run's Messages are found by Message.RunID.
	RunStart int
	// Request is the request about to be sent, built from the Transcript as it
	// stands. On an overflow retry it is the request the Model refused.
	Request ModelRequest
	// Overflow is true when the Model refused Request as longer than its
	// window before any output, and this is the one chance to shorten it. A
	// preparer that cannot returns Changed false, and the Run ends with
	// CodeContextOverflow.
	Overflow bool
	// Refusal is the error the Model refused Request with, when Overflow is
	// true, so a preparer can read what the refusal names, such as the limit
	// of the request (an error method `ContextLimit() int` says it in
	// tokens, as the gateway's does when the provider's message names one).
	Refusal error
	// Flush writes the Events the Run has staged, so a preparer that appends
	// to the Transcript's store writes after them and not before.
	Flush func(context.Context) error
}

// TurnPrepared is the result of PrepareTurn.
type TurnPrepared struct {
	// Changed says the preparer rewrote the Transcript, so the Loop builds
	// the request again before it calls the Model.
	Changed bool
}
