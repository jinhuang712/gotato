package modelctx

import "github.com/jinhuang712/gotato/gotato"

var (
	// ErrNoRecapContext is what a ModelRecap returns when the compaction gave it
	// no Context to build its request from.
	ErrNoRecapContext = gotato.ErrorOf(gotato.CodeInvalidArgument, "modelctx: ModelRecap needs the Context the next call would send (CompactConfig.Context)")

	// ErrBadRecap is what ValidateRecap and ModelRecap return for a reply that is
	// not a Recap.
	ErrBadRecap = gotato.ErrorOf(gotato.CodeModelProtocolFailure, "modelctx: the reply is not a valid Recap")
)
