package gotato

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"
)

// ErrorCode says what kind of failure a RuntimeError is. The values are the
// Code constants, and the same strings appear in JSON, so a caller can match on
// them.
type ErrorCode string

// The error codes. Each value is the string that appears in JSON.
const (
	CodeInvalidArgument ErrorCode = "invalid_argument"
	CodeInvalidState    ErrorCode = "invalid_state"
	// CodeBusy means the Session has a Run in flight, here or on another
	// Instance. The request did not run; it may succeed once that Run ends.
	CodeBusy           ErrorCode = "busy"
	CodeSessionClosed  ErrorCode = "session_closed"
	CodeSessionClosing ErrorCode = "session_closing"
	CodeModelFailure   ErrorCode = "model_failure"
	// CodeContextOverflow means the Model refused a request as longer than its
	// context window. A Model reports it by returning an error with a
	// `ContextOverflow() bool` method that answers true, as the gateway's
	// Error does; any other Model failure is CodeModelFailure. A request
	// refused before any output is compacted and sent again once. A reply
	// that overflows after some text ends the Run with this code and keeps the
	// text as a Cut-off reply: the compaction before every model call leaves
	// room for a reply, so the next message compacts first and the Session
	// goes on.
	CodeContextOverflow       ErrorCode = "context_overflow"
	CodeModelProtocolFailure  ErrorCode = "model_protocol_failure"
	CodeToolResolutionFailure ErrorCode = "tool_resolution_failure"
	CodeToolArgumentFailure   ErrorCode = "tool_argument_failure"
	CodeToolExecutionFailure  ErrorCode = "tool_execution_failure"
	CodeExtensionFailure      ErrorCode = "extension_failure"
	CodeLimitExceeded         ErrorCode = "limit_exceeded"
	CodeCanceled              ErrorCode = "canceled"
	CodeDeadlineExceeded      ErrorCode = "deadline_exceeded"
	CodeInternalInvariant     ErrorCode = "internal_invariant"
	CodeNotSupported          ErrorCode = "not_supported"
	// CodeConflict means another writer wrote to the Session's Record first:
	// a stale copy can never overwrite a newer one.
	CodeConflict ErrorCode = "conflict"
	// CodeStoreFailure means the store could not write a Session's Record.
	CodeStoreFailure ErrorCode = "store_failure"
	// CodeOverloaded means an Instance is past its hard limit and refused to
	// activate a new Session. The request did not run, so sending it to
	// another Instance cannot run it twice.
	CodeOverloaded ErrorCode = "overloaded"
	// CodeDraining means the Instance is draining and takes no new Run. The
	// request did not run, so another Instance can serve it.
	CodeDraining ErrorCode = "draining"
	// CodeNotFound means the Session (or the Record it names) does not exist.
	// The request did not run, and sending it again changes nothing.
	// session.ErrNotFound carries it.
	CodeNotFound ErrorCode = "not_found"
	// CodeCannotResume means a stream cannot go on from the position a client
	// holds: the Record is not the one it saw, or the number is past its last
	// Event. The client starts over from the first Event.
	CodeCannotResume ErrorCode = "cannot_resume"
	// CodeNotWriter means a message for a Run reached an Instance that is not
	// the Run's writer. The message did not run, and sent again to the
	// writer, whose advertised address is in Details["writer"] when it has
	// one, it may succeed.
	CodeNotWriter ErrorCode = "not_writer"
	// CodeAlreadyExists means a Session with the ID the caller chose exists
	// already, so a create or a fork with that ID did nothing. The ID is taken:
	// choose another, or use the Session that has it.
	CodeAlreadyExists ErrorCode = "already_exists"
)

// ErrorCodes lists every ErrorCode, so a layer that maps codes (a transport, a
// CLI) can check that it maps each one. A new code is added here too; a test
// fails when it is not.
func ErrorCodes() []ErrorCode {
	return []ErrorCode{
		CodeInvalidArgument, CodeInvalidState, CodeBusy, CodeSessionClosed, CodeSessionClosing,
		CodeModelFailure, CodeContextOverflow, CodeModelProtocolFailure,
		CodeToolResolutionFailure, CodeToolArgumentFailure, CodeToolExecutionFailure,
		CodeExtensionFailure, CodeLimitExceeded, CodeCanceled, CodeDeadlineExceeded,
		CodeInternalInvariant, CodeNotSupported, CodeConflict, CodeStoreFailure,
		CodeOverloaded, CodeDraining, CodeNotFound, CodeCannotResume, CodeNotWriter,
		CodeAlreadyExists,
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

// Is reports whether target is a code-only *RuntimeError (one with no Message,
// as in &RuntimeError{Code: CodeBusy}) with the same Code. A target with a
// Message is a sentinel built with ErrorOf, and errors.Is matches it by
// identity alone, through the chain, so two sentinels that share a code are
// told apart. To match any error of a code, use IsCode.
func (e *RuntimeError) Is(target error) bool {
	if e == nil {
		return false
	}
	var other *RuntimeError
	if !errors.As(target, &other) {
		return false
	}
	if other == nil || other.Message != "" {
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
	case CodeDraining, CodeOverloaded, CodeBusy, CodeNotWriter:
		e.NotRun, e.Retryable = true, true
	case CodeNotFound, CodeSessionClosing, CodeSessionClosed:
		e.NotRun = true
	case CodeStoreFailure:
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
		return CodeDeadlineExceeded
	}
	return CodeCanceled
}

// IsCode reports whether err is, or wraps, a RuntimeError with the given code.
func IsCode(err error, code ErrorCode) bool {
	var re *RuntimeError
	return errors.As(err, &re) && re != nil && re.Code == code
}

var (
	// ErrEventsLost ends a Subscription that needs Events its source no longer
	// holds.
	ErrEventsLost = ErrorOf(CodeCannotResume, "gotato: the Events this subscriber missed are no longer available")
)

// contextFailure returns the RuntimeError of an ended ctx, and nil while it
// is live.
func contextFailure(ctx context.Context, operation string) *RuntimeError {
	if err := ctx.Err(); err != nil {
		return runtimeError(codeForContext(err), operation, err.Error(), err)
	}
	return nil
}

// classifyModelError turns an error from a Model into a RuntimeError: a
// cancellation, a context overflow, or a Model failure that may be retryable.
//
// A context error counts as the caller's only when a context of the call
// really ended: runCtx is the Run's, modelCtx the call's, which also ends at
// the ModelCallDeadline (limit, when positive) or at an Interrupt. A Model
// that returns a context error of its own (an HTTP client timeout) while
// neither ended has failed, and a timeout is retryable.
func classifyModelError(err error, runCtx, modelCtx context.Context, limit time.Duration) *RuntimeError {
	if err == nil {
		return runtimeError(CodeModelFailure, "Model", "unknown Model failure", nil)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// Only the text of a context that really ended is used: the Model's
		// own error may carry what a client must not read, and stays on the
		// Cause.
		if cause := runCtx.Err(); cause != nil {
			return runtimeError(codeForContext(cause), "Model", cause.Error(), err)
		}
		if cause := modelCtx.Err(); cause != nil {
			failure := runtimeError(codeForContext(cause), "Model", cause.Error(), err)
			if limit > 0 && errors.Is(cause, context.DeadlineExceeded) {
				failure.Message = "model call exceeded ModelCallDeadline"
				failure.Details = map[string]string{"limit": limit.String()}
			}
			return failure
		}
		failure := runtimeError(CodeModelFailure, "Model", PublicText(err, "model call failed"), err)
		failure.Retryable = errors.Is(err, context.DeadlineExceeded)
		return failure
	}
	// A Model that finds its reply broken (a stream that ends with a finish
	// reason nobody knows), or a request it cannot send as set, says so with
	// a code of its own, and the Run keeps it.
	var coded *RuntimeError
	if errors.As(err, &coded) && coded != nil && (coded.Code == CodeModelProtocolFailure || coded.Code == CodeNotSupported) {
		return coded
	}
	var overflow interface{ ContextOverflow() bool }
	if errors.As(err, &overflow) && overflow.ContextOverflow() {
		return runtimeError(CodeContextOverflow, "Model", PublicText(err, "the request is longer than the model accepts"), err)
	}
	failure := runtimeError(CodeModelFailure, "Model", PublicText(err, "model call failed"), err)
	// A Model that knows the failure is transient (a rate limit, an overload,
	// a broken stream) says so with `Retryable() bool`, as the gateway's
	// Error does. The Model was called, so the request is not "not run".
	var transient interface{ Retryable() bool }
	if errors.As(err, &transient) && transient.Retryable() {
		failure.Retryable = true
	}
	return failure
}

// asError returns failure as an error that is nil when failure is nil. Code
// that holds a *RuntimeError and hands it on as an error goes through it, so
// a nil failure never becomes a non-nil error (the typed-nil trap).
func asError(failure *RuntimeError) error {
	if failure == nil {
		return nil
	}
	return failure
}

// asRuntimeError returns err as a *RuntimeError, wrapping any other error as
// an internal invariant failure.
func asRuntimeError(err error) *RuntimeError {
	if err == nil {
		return nil
	}
	var runtimeErr *RuntimeError
	if errors.As(err, &runtimeErr) {
		return runtimeErr
	}
	return runtimeError(CodeInternalInvariant, "Core", PublicText(err, "internal error"), err)
}

// panicError is a panic of code the host supplied (a Model, a Journal, a
// Transcript), as an error. Its Public text is fixed, so a RuntimeError built
// from it has a Message a client may read; what it panicked with, and the
// stack, are in Error() on the Cause, for logs.
type panicError struct {
	what  string
	value any
	stack []byte
}

func (e *panicError) Error() string  { return fmt.Sprintf("%s panic: %v\n%s", e.what, e.value, e.stack) }
func (e *panicError) Public() string { return e.what + " panic" }

// recoverPanic is deferred by a call into host code: a panic becomes *err, so
// one faulty Model or store cannot take down the process, as a faulty
// extension or Tool cannot.
// [LIVENESS] a panic in host code would end the Session goroutine and the process instead of failing one call. Test: TestPanicOfAModelJournalOrTranscriptFailsTheRun
func recoverPanic(what string, err *error) {
	if recovered := recover(); recovered != nil {
		*err = &panicError{what: what, value: recovered, stack: debug.Stack()}
	}
}

// callHost runs one call into host code; a panic in it is the error it
// returns, a *panicError whose Public text is "<what> panic".
func callHost(what string, call func() error) (err error) {
	defer recoverPanic(what, &err)
	return call()
}
