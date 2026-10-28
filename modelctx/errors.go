package modelctx

import (
	"context"
	"errors"

	"github.com/jinhuang712/gotato/gotato"
)

var (
	// ErrNoRecapContext is what a ModelRecap returns when the compaction gave it
	// no Context to build its request from.
	ErrNoRecapContext = gotato.ErrorOf(gotato.CodeInvalidArgument, "modelctx: ModelRecap needs the Context the next call would send (modelctx.WithRecapContext)")

	// ErrNoRecapModel is what a ModelRecap returns, and Compact and the
	// AutoCompactor refuse with, when it names no Model: outside a Runner,
	// which fills it with the Agent's, a ModelRecap must be given one.
	ErrNoRecapModel = gotato.ErrorOf(gotato.CodeInvalidArgument, "modelctx: ModelRecap has no Model; set ModelRecap.Model, or use it in an Agent run by a Runner")

	// ErrBadRecap is what ValidateRecap and ModelRecap return for a reply that is
	// not a Recap.
	ErrBadRecap = gotato.ErrorOf(gotato.CodeModelProtocolFailure, "modelctx: the reply is not a valid Recap")
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
