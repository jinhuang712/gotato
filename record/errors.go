package record

import (
	"context"
	"errors"

	"github.com/jinhuang712/gotato/gotato"
)

var (
	// ErrNotFound is what a read of a Record's log returns for an unknown or
	// deleted Record ID (EntryLog), and what a store returns for the same, and
	// for an Append to a Record that was deleted and created again.
	ErrNotFound = gotato.ErrorOf(gotato.CodeNotFound, "record: not found")

	// ErrNoRecapContext is what a ModelRecap returns when the compaction gave it
	// no Context to build its request from.
	ErrNoRecapContext = gotato.ErrorOf(gotato.CodeInvalidArgument, "record: ModelRecap needs the Context the next call would send (modelctx.WithRecapContext)")

	// ErrNoRecapModel is what a ModelRecap returns, and Compact and the
	// AutoCompactor refuse with, when it names no Model: outside a Runner,
	// which fills it with the Agent's, a ModelRecap must be given one.
	ErrNoRecapModel = gotato.ErrorOf(gotato.CodeInvalidArgument, "record: ModelRecap has no Model; set ModelRecap.Model, or use it in an Agent run by a Runner")

	// ErrBadPart is what a compaction fails with when a part of its Compactor
	// fails, panics or returns what the frame refuses: a cut outside the
	// stretch, a Segment whose tagged text holds a tag or whose ref names
	// nothing it closed, a merge of fewer than two Segments. The compaction is
	// dropped and the Record is as it was; the Run goes on.
	ErrBadPart = gotato.ErrorOf(gotato.CodeExtensionFailure, "record: a compaction part failed or broke the rules of the frame")

	// ErrBadRecap is what ValidateRecap and ModelRecap return for a reply that is
	// not a Recap.
	ErrBadRecap = gotato.ErrorOf(gotato.CodeModelProtocolFailure, "record: the reply is not a valid Recap")
)

// causeOf names the code of a failure for the Event that reports it.
func causeOf(err error) gotato.ErrorCode {
	var runtime *gotato.RuntimeError
	switch {
	case errors.As(err, &runtime) && runtime != nil:
		return runtime.Code
	case errors.Is(err, context.DeadlineExceeded):
		return gotato.CodeDeadlineExceeded
	case errors.Is(err, context.Canceled):
		return gotato.CodeCanceled
	}
	return gotato.CodeStoreFailure
}
