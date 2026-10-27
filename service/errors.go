package service

import "github.com/jinhuang712/gotato/gotato"

var (
	// ErrBusy is returned when a Session already has a Run in flight and the
	// policy is RejectWhileBusy.
	ErrBusy = gotato.ErrorOf(gotato.CodeBusy, "service: session has a run in flight")

	// ErrCapacity is returned when MaxActiveRuns is reached.
	// It is refused at admission, so the Run is not run, and another Instance, or
	// this one later, may take it.
	ErrCapacity = refusedAtAdmission(gotato.ErrorOf(gotato.CodeLimitExceeded, "service: maximum active runs reached"))

	// ErrOverloaded is returned when the Instance is at its MaxActiveSessions and
	// the Run would activate another Session. The Run did not start and nothing
	// was written, so it is safe to send again to another Instance.
	ErrOverloaded = gotato.ErrorOf(gotato.CodeOverloaded, "service: this instance is at its limit of active sessions")

	// ErrUnknownAgent is returned for an unregistered Agent name.
	// The Run is refused before it starts, so it is not run.
	ErrUnknownAgent = notRunError(gotato.ErrorOf(gotato.CodeInvalidArgument, "service: unknown agent"))

	// ErrSessionExists is returned when an explicit Session ID is already taken.
	ErrSessionExists = gotato.ErrorOf(gotato.CodeInvalidState, "service: session already exists")

	// ErrDraining refuses a new Run, and taking a Session over from another
	// Instance, once the Runner drains.
	ErrDraining = gotato.ErrorOf(gotato.CodeDraining, "service: draining")

	// ErrNotPersisted reports that a write of a Run failed, so the Record does not
	// hold the Run. The Run outcome is still returned, settled as gotato.RunFailed
	// with store_failure; callers must treat the Session state as lost. A Run whose
	// only failed write is the release of the Session keeps its status and
	// returns no error: its Events are all stored.
	ErrNotPersisted = notPersistedSentinel()

	// ErrCannotResume is returned by StreamSession when the position a client
	// holds no longer means what it did: the Record's epoch is not the one the
	// client saw, because the Record was deleted and created again under the same
	// ID, or the number is past the Record's last Event. The client starts over
	// from the first Event.
	ErrCannotResume = gotato.ErrorOf(gotato.CodeCannotResume, "service: the stream cannot resume from that position")

	// ErrNoRun is returned by AbortSession when no Run is going on the Session,
	// here or on the Instance that writes it: a Stop finds nothing to stop.
	ErrNoRun = gotato.ErrorOf(gotato.CodeInvalidState, "service: session has no active run")
)

// refusedAtAdmission marks a refusal made before a Run started: nothing ran,
// and the same request may succeed elsewhere or later.
func refusedAtAdmission(e *gotato.RuntimeError) *gotato.RuntimeError {
	e.NotRun, e.Retryable = true, true
	return e
}

// notRunError marks a refusal made before a Run started.
func notRunError(e *gotato.RuntimeError) *gotato.RuntimeError {
	e.NotRun = true
	return e
}

// notPersistedSentinel builds ErrNotPersisted. It carries store_failure, and is
// not retryable by itself, as it was before it carried a code.
func notPersistedSentinel() *gotato.RuntimeError {
	e := gotato.ErrorOf(gotato.CodeStoreFailure, "service: session was not persisted")
	e.Retryable = false
	return e
}

// notPersisted is ErrNotPersisted joined to the store failure that caused it.
// Its message is the sentinel's and the failure's; the failure comes first in
// the chain, so errors.As reads its code and Retryable, and errors.Is matches
// both.
type notPersisted struct{ cause error }

func (e notPersisted) Error() string   { return ErrNotPersisted.Error() + ": " + e.cause.Error() }
func (e notPersisted) Unwrap() []error { return []error{e.cause, ErrNotPersisted} }
