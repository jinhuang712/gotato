package host

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
)

var (
	// ErrBusy is returned when a Session already has a Run in flight and the
	// policy is QueuePolicyReject.
	ErrBusy = gotato.ErrorOf(gotato.CodeBusy, "host: session has a run in flight")

	// ErrCapacity is returned when MaxActiveRuns is reached.
	// It is refused at admission, so the Run is not run, and another Instance, or
	// this one later, may take it.
	ErrCapacity = refusedAtAdmission(gotato.ErrorOf(gotato.CodeLimitExceeded, "host: maximum active runs reached"))

	// ErrOverloaded is returned when the Instance is at its MaxActiveSessions and
	// the Run would activate another Session. The Run did not start and nothing
	// was written, so it is safe to send again to another Instance.
	ErrOverloaded = errorOf(CodeOverloaded, "host: this instance is at its limit of active sessions")

	// ErrUnknownAgent is returned for an unregistered Agent name.
	// The Run is refused before it starts, so it is not run.
	ErrUnknownAgent = notRunError(gotato.ErrorOf(gotato.CodeInvalidArgument, "host: unknown agent"))

	// ErrReservedKey is returned when a caller sets a Session metadata key under
	// ReservedPrefix ("gotato."): CreateSession, RunRequest.Metadata and
	// RunRequest.Update refuse it before anything is written, as invalid_argument
	// (HTTP 400), and the error names the key. Those keys are the Runner's: the
	// Agent changes only by RunRequest.Agent, a Tool is switched only by
	// SetToolActive, and the cache key only by Fork. Match it with errors.Is.
	ErrReservedKey = notRunError(gotato.ErrorOf(gotato.CodeInvalidArgument, "host: a metadata key under \"gotato.\" belongs to the Runner"))

	// ErrSessionExists is returned when an explicit Session ID is already taken.
	// It has its own code, already_exists, so a client can tell "the ID is
	// taken" from the other invalid_state answers without reading the message.
	ErrSessionExists = errorOf(CodeAlreadyExists, "host: session already exists")

	// ErrDraining refuses a new Run, and taking a Session over from another
	// Instance, once the Runner drains.
	ErrDraining = errorOf(CodeDraining, "host: draining")

	// ErrNotPersisted reports that a write of a Run failed, so the Record does not
	// hold the Run. The Run outcome is still returned, settled as gotato.RunStatusFailed
	// with store_failure; callers must treat the Session state as lost. A Run whose
	// only failed write is the release of the Session keeps its status and
	// returns no error: its Events are all stored.
	ErrNotPersisted = notPersistedSentinel()

	// ErrCannotResume is returned by StreamSession when the position a client
	// holds no longer means what it did: the Record's epoch is not the one the
	// client saw, because the Record was deleted and created again under the same
	// ID, or the number is past the Record's last Event. The client starts over
	// from the first Event.
	ErrCannotResume = errorOf(CodeCannotResume, "host: the stream cannot resume from that position")

	// ErrNoRun is returned by AbortSession when no Run is going on the Session,
	// here or on the Instance that writes it: a Stop finds nothing to stop.
	ErrNoRun = gotato.ErrorOf(gotato.CodeInvalidState, "host: session has no active run")
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
	e := gotato.ErrorOf(gotato.CodeStoreFailure, "host: session was not persisted")
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

// storeFailureOf classifies an error of a store call that made no Run write:
// a load, a create, a heartbeat, a delete. A store's raw error becomes
// store_failure, retryable, and marked not run when nothing of a Run had
// happened (notRun), so a caller can send the request elsewhere. A failure
// that is typed already keeps its code (not_found and conflict are the
// store's answers); a store_failure from a write that landed no Run step gets
// the not-run mark. An error of the caller's own context is returned as it is.
func storeFailureOf(ctx context.Context, err error, notRun bool) error {
	if err == nil || ctx.Err() != nil {
		return err
	}
	var typed *gotato.RuntimeError
	if errors.As(err, &typed) {
		if typed.Code != gotato.CodeStoreFailure || typed.NotRun || !notRun {
			return err
		}
		marked := *typed
		marked.NotRun = true
		return &marked
	}
	failure := gotato.ErrorOf(gotato.CodeStoreFailure, "host: the store could not be reached")
	failure.Operation, failure.Cause, failure.NotRun = "host.Store", err, notRun
	return failure
}

// refuseReserved is ErrReservedKey, naming the first key of metadata (in key
// order) that lies under ReservedPrefix, or nil when none does. Every path that
// takes a caller's metadata calls it before it writes anything.
func refuseReserved(metadata map[string]string) error {
	for _, key := range slices.Sorted(maps.Keys(metadata)) {
		if strings.HasPrefix(key, ReservedPrefix) {
			return fmt.Errorf("%w: %q", ErrReservedKey, key)
		}
	}
	return nil
}
