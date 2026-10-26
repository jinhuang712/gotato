package gotato

import (
	"context"
	"errors"
	"fmt"
)

type ErrorCode string

const (
	ErrInvalidArgument ErrorCode = "invalid_argument"
	ErrInvalidState    ErrorCode = "invalid_state"
	// ErrBusy means the Session has a Run in flight, here or on another
	// Instance. The request did not run; it may succeed once that Run ends.
	ErrBusy           ErrorCode = "busy"
	ErrSessionClosed  ErrorCode = "session_closed"
	ErrSessionClosing ErrorCode = "session_closing"
	ErrModelFailure   ErrorCode = "model_failure"
	// ErrContextOverflow means the Model refused a request as longer than its
	// context window. A Model reports it by returning an error with a
	// `ContextOverflow() bool` method that answers true, as the gateway's
	// Error does; any other Model failure is ErrModelFailure. A request
	// refused before any output is compacted and sent again once. A reply
	// that overflows after some text ends the Run with this code and keeps the
	// text as a Cut-off reply: the compaction before every model call leaves
	// room for a reply, so the next message compacts first and the Session
	// goes on.
	ErrContextOverflow       ErrorCode = "context_overflow"
	ErrModelProtocolFailure  ErrorCode = "model_protocol_failure"
	ErrToolResolutionFailure ErrorCode = "tool_resolution_failure"
	ErrToolArgumentFailure   ErrorCode = "tool_argument_validation_failure"
	ErrToolExecutionFailure  ErrorCode = "tool_execution_failure"
	ErrExtensionFailure      ErrorCode = "extension_failure"
	ErrLimitExceeded         ErrorCode = "limit_exceeded"
	ErrCancelled             ErrorCode = "cancelled"
	ErrDeadlineExceeded      ErrorCode = "deadline_exceeded"
	ErrInternalInvariant     ErrorCode = "internal_invariant_failure"
	ErrNotSupported          ErrorCode = "not_supported"
	// ErrConflict means another writer wrote to the Session's Record first:
	// a stale copy can never overwrite a newer one.
	ErrConflict ErrorCode = "conflict"
	// ErrStoreFailure means the store could not write a Session's Record.
	ErrStoreFailure ErrorCode = "store_failure"
	// ErrOverloaded means an Instance is past its hard limit and refused to
	// activate a new Session. The request did not run, so sending it to
	// another Instance cannot run it twice.
	ErrOverloaded ErrorCode = "overloaded"
	// ErrDraining means the Instance is draining and takes no new Run. The
	// request did not run, so another Instance can serve it.
	ErrDraining ErrorCode = "draining"
	// ErrNotFound means the Session (or the Record it names) does not exist.
	// The request did not run, and sending it again changes nothing.
	// session.ErrNotFound matches it.
	ErrNotFound ErrorCode = "not_found"
	// ErrCannotResume means a stream cannot go on from the position a client
	// holds: the Record is not the one it saw, or the number is past its last
	// Event. The client starts over from the first Event.
	ErrCannotResume ErrorCode = "cannot_resume"
	// ErrNotWriter means a message for a Run reached an Instance that is not
	// the Run's writer. The message did not run, and sent again to the
	// writer, whose advertised address is in Details["writer"] when it has
	// one, it may succeed.
	ErrNotWriter ErrorCode = "not_writer"
)

// ErrorCodes lists every ErrorCode, so a layer that maps codes (a transport, a
// CLI) can check that it maps each one. A new code is added here too; a test
// fails when it is not.
func ErrorCodes() []ErrorCode {
	return []ErrorCode{
		ErrInvalidArgument, ErrInvalidState, ErrBusy, ErrSessionClosed, ErrSessionClosing,
		ErrModelFailure, ErrContextOverflow, ErrModelProtocolFailure,
		ErrToolResolutionFailure, ErrToolArgumentFailure, ErrToolExecutionFailure,
		ErrExtensionFailure, ErrLimitExceeded, ErrCancelled, ErrDeadlineExceeded,
		ErrInternalInvariant, ErrNotSupported, ErrConflict, ErrStoreFailure,
		ErrOverloaded, ErrDraining, ErrNotFound, ErrCannotResume, ErrNotWriter,
	}
}

// RuntimeError is the one error a Gotato layer returns. Besides its Code it
// answers two questions, set where the error is made and never guessed by a
// caller or a transport:
//
//   - NotRun: nothing with side effects happened, so the same request sent to
//     another Instance cannot run twice.
//   - Retryable: the same request sent again later, here or elsewhere, may
//     succeed.
//
// Read them through the NotRun and Retryable functions, which look through
// wrapped errors.
type RuntimeError struct {
	Code      ErrorCode         `json:"code"`
	Operation string            `json:"operation,omitempty"`
	Message   string            `json:"message"`
	Retryable bool              `json:"retryable,omitempty"`
	NotRun    bool              `json:"not_run,omitempty"`
	Details   map[string]string `json:"details,omitempty"`
	Cause     error             `json:"-"`
}

func (e *RuntimeError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Operation == "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s (%s): %s", e.Code, e.Operation, e.Message)
}

func (e *RuntimeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Is reports whether target is a *RuntimeError with the same Code. It lets a
// caller match a sentinel built with ErrorOf through errors.Is without sharing
// the sentinel's pointer.
func (e *RuntimeError) Is(target error) bool {
	if e == nil {
		return false
	}
	var other *RuntimeError
	if !errors.As(target, &other) {
		return false
	}
	if other == nil {
		return false
	}
	return e.Code == other.Code
}

func runtimeError(code ErrorCode, operation, message string, cause error) *RuntimeError {
	e := &RuntimeError{Code: code, Operation: operation, Message: message, Cause: cause}
	e.applyDefaults()
	return e
}

// applyDefaults sets NotRun and Retryable for the codes whose answer is the
// same wherever they are made:
//
//	code             not run  retryable
//	draining         yes      yes (on another Instance)
//	overloaded       yes      yes (on another Instance)
//	busy             yes      yes (after the Run in flight)
//	not_writer       yes      yes (at the writer)
//	not_found        yes      no
//	session_closing  yes      no
//	session_closed   yes      no
//	store_failure    unknown  yes
//
// Any other code is neither: it is set by the place that knows better, such
// as a model failure the provider calls retryable.
func (e *RuntimeError) applyDefaults() {
	switch e.Code {
	case ErrDraining, ErrOverloaded, ErrBusy, ErrNotWriter:
		e.NotRun, e.Retryable = true, true
	case ErrNotFound, ErrSessionClosing, ErrSessionClosed:
		e.NotRun = true
	case ErrStoreFailure:
		e.Retryable = true
	}
}

// NotRun reports whether err says its request did not run: nothing with side
// effects happened, so sending the same request to another Instance cannot
// run it twice. It is false for nil, for an error that is not a *RuntimeError,
// and for one that does not say so.
func NotRun(err error) bool {
	var re *RuntimeError
	return errors.As(err, &re) && re != nil && re.NotRun
}

// Retryable reports whether err says the same request, sent again later, may
// succeed. It is false for nil and for an error that does not say so.
func Retryable(err error) bool {
	var re *RuntimeError
	return errors.As(err, &re) && re != nil && re.Retryable
}

// ErrorOf builds a typed error for a layer outside Core. The service layer,
// protocol adapters, and applications classify their own failures with the
// same codes so a caller reads one error vocabulary across the whole boundary.
func ErrorOf(code ErrorCode, message string) *RuntimeError {
	e := &RuntimeError{Code: code, Message: message}
	e.applyDefaults()
	return e
}

func codeForContext(err error) ErrorCode {
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrDeadlineExceeded
	}
	return ErrCancelled
}

func IsCode(err error, code ErrorCode) bool {
	var re *RuntimeError
	return errors.As(err, &re) && re != nil && re.Code == code
}
