package gotato

import (
	"context"
)

// ContextInspection is the request a Session's next Turn would send, built
// without calling the Model.
type ContextInspection struct {
	// SourceMessages is the number of committed Messages it was built from.
	SourceMessages int `json:"source_messages"`
	// Context is the built Context, after the ContextTransformer and
	// MessageConverter Extensions.
	Context ModelContext `json:"context"`
	// Request is what the Model would receive, model options included.
	Request ModelRequest `json:"request"`
	// PrefixHash is the hash a context_built Event reports for this request.
	PrefixHash string `json:"prefix_hash"`
}

// InspectContext builds the request the Session's next Turn would send for its
// Transcript as committed now, without calling the Model. It runs the code a
// Turn runs: the Tool sources are read again, the ContextBuilder builds the
// Context, the ContextTransformer and MessageConverter Extensions shape it,
// and the request carries the visible Tools and the model options. It runs no
// TurnPreparer and emits no Event. It fails with CodeBusy while a Run is in
// flight.
//
// The snapshot it builds from has no RunID and is Turn 1, as at the start of
// a Run. A ContextTransformer whose output depends on the Run or the Turn, or
// a Tool the Model loaded during a Run, makes a later Turn differ.
func (a *Session) InspectContext(ctx context.Context) (ContextInspection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ContextInspection{}, runtimeError(codeForContext(err), "InspectContext", "context ended", err)
	}
	if err := a.admissionError("InspectContext"); err != nil {
		return ContextInspection{}, err
	}
	// Holding the admission token keeps a Run from starting while the
	// Context is built from the Transcript and the Tool registry.
	select {
	case a.admission <- struct{}{}:
	default:
		return ContextInspection{}, runtimeError(CodeBusy, "InspectContext", "Session is processing a Run", nil)
	}
	defer func() { <-a.admission }()
	sourceErr, filterErr := a.refreshTools(ctx)
	if filterErr != nil {
		return ContextInspection{}, asRuntimeError(filterErr)
	}
	if sourceErr != nil {
		return ContextInspection{}, asRuntimeError(sourceErr)
	}
	snapshot := ContextSnapshot{
		SessionID:          a.id,
		Turn:               1,
		SystemInstructions: a.instruction,
		Messages:           cloneMessages(a.transcript.Messages()),
	}
	built, request, prefix, err := a.assembleTurn(ctx, snapshot)
	if err != nil {
		return ContextInspection{}, err
	}
	return ContextInspection{
		SourceMessages: len(snapshot.Messages),
		Context:        built,
		Request:        request,
		PrefixHash:     prefix,
	}, nil
}
