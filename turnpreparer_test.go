package gotato

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// callModel answers each call from steps: an error is a refusal before any
// output, events are a reply.
type callModel struct {
	mu       sync.Mutex
	steps    []any
	requests []ModelRequest
}

func (m *callModel) Stream(_ context.Context, request ModelRequest) (ModelStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, request)
	index := len(m.requests) - 1
	if index >= len(m.steps) {
		return nil, errors.New("callModel: no step left")
	}
	switch step := m.steps[index].(type) {
	case error:
		return nil, step
	case []ModelEvent:
		return &testStream{events: step}, nil
	}
	return nil, errors.New("callModel: bad step")
}

func (m *callModel) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

// editableTranscript is a Transcript a TurnPreparer can rewrite.
type editableTranscript struct{ messages []Message }

func (t *editableTranscript) Len() int            { return len(t.messages) }
func (t *editableTranscript) Messages() []Message { return t.messages }
func (t *editableTranscript) Append(message Message) error {
	t.messages = append(t.messages, message)
	return nil
}

// preparerFunc is a TurnPreparer made of a function.
type preparerFunc func(context.Context, PendingTurn) (TurnPrepared, error)

func (f preparerFunc) PrepareTurn(ctx context.Context, pending PendingTurn) (TurnPrepared, error) {
	return f(ctx, pending)
}

func newPreparedSession(t *testing.T, model Model, transcript Transcript, preparer TurnPreparer, extra ...Option) *Session {
	t.Helper()
	options := []Option{WithModel(model), WithTool(&countingTool{}), WithExtension(preparer)}
	if transcript != nil {
		options = append(options, WithTranscript(transcript))
	}
	session, err := NewSession(append(options, extra...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	return session
}

// The Loop calls the preparer before every model call, with the Turn, the
// request about to be sent, and where the Run started.
func TestATurnPreparerRunsBeforeEveryModelCall(t *testing.T) {
	model := &callModel{steps: []any{toolCallScript(), finalScript("done")}}
	transcript := &editableTranscript{messages: []Message{UserMessage("earlier"), AssistantMessage("answered")}}
	var seen []PendingTurn
	preparer := preparerFunc(func(ctx context.Context, p PendingTurn) (TurnPrepared, error) {
		if err := p.Flush(ctx); err != nil {
			t.Errorf("Flush: %v", err)
		}
		seen = append(seen, p)
		return TurnPrepared{}, nil
	})
	session := newPreparedSession(t, model, transcript, preparer)
	result, err := session.Prompt(context.Background(), UserMessage("go"))
	if err != nil || result.Status != RunCompleted {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if len(seen) != 2 || model.calls() != 2 {
		t.Fatalf("preparer ran %d times for %d model calls, want 2 and 2", len(seen), model.calls())
	}
	for i, p := range seen {
		if p.Turn != TurnNumber(i+1) || p.RunID != result.RunID || p.SessionID == "" || p.Overflow || p.RunStart != 2 {
			t.Errorf("call %d: %+v", i, p)
		}
	}
	// The request is the one the Model is sent: the second carries the Tool
	// batch, and no Tool call is open.
	if got := len(seen[0].Request.Messages); got != 3 {
		t.Errorf("first request has %d Messages, want 3", got)
	}
	if got := len(seen[1].Request.Messages); got != 5 || seen[1].Request.Messages[4].Role != RoleToolResult {
		t.Errorf("second request has %d Messages, want 5 ending in the Tool Result", got)
	}
}

// A preparer that rewrote the Transcript says so, and the request is built
// again from it before the call.
func TestATurnPreparerThatRewritesTheTranscriptHasTheRequestBuiltAgain(t *testing.T) {
	model := &callModel{steps: []any{toolCallScript(), finalScript("done")}}
	transcript := &editableTranscript{}
	preparer := preparerFunc(func(_ context.Context, p PendingTurn) (TurnPrepared, error) {
		if p.Turn != 2 {
			return TurnPrepared{}, nil
		}
		// Keep the latest Turn, as a compaction does.
		messages := p.Transcript.(*editableTranscript)
		messages.messages = append([]Message{UserMessage("SEGMENT")}, messages.messages[len(messages.messages)-2:]...)
		return TurnPrepared{Changed: true}, nil
	})
	session := newPreparedSession(t, model, transcript, preparer)
	if _, err := session.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	second := model.requests[1].Messages
	if len(second) != 3 || TextOf(second[0]) != "SEGMENT" || second[2].Role != RoleToolResult {
		t.Fatalf("second request = %+v, want the rewritten Transcript", second)
	}
}

// A refusal as too long, before any output, gives the preparer one chance to
// shorten the request, and the call is sent again.
func TestARefusalAsTooLongIsRetriedOnceAfterAPreparerChangedTheTranscript(t *testing.T) {
	model := &callModel{steps: []any{overflowError{overflow: true}, finalScript("fits")}}
	transcript := &editableTranscript{messages: []Message{UserMessage("old"), AssistantMessage("older")}}
	var calls []bool
	preparer := preparerFunc(func(_ context.Context, p PendingTurn) (TurnPrepared, error) {
		calls = append(calls, p.Overflow)
		if !p.Overflow {
			return TurnPrepared{}, nil
		}
		transcript.messages = transcript.messages[2:]
		return TurnPrepared{Changed: true}, nil
	})
	session := newPreparedSession(t, model, transcript, preparer)
	result, err := session.Prompt(context.Background(), UserMessage("go"))
	if err != nil || result.Status != RunCompleted {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if len(calls) != 2 || calls[0] || !calls[1] {
		t.Fatalf("preparer calls (overflow flag) = %v, want [false true]", calls)
	}
	if got := len(model.requests[0].Messages); got != 3 {
		t.Errorf("the refused request had %d Messages, want 3", got)
	}
	if got := len(model.requests[1].Messages); got != 1 {
		t.Errorf("the second request had %d Messages, want 1", got)
	}
	if result.Metrics.Turns != 1 {
		t.Errorf("the retry made %d Turns, want it inside one", result.Metrics.Turns)
	}
}

// The preparer is given the error that refused the request, and only then, so
// it can read what the refusal names.
func TestAPreparerIsGivenTheErrorThatRefusedTheRequest(t *testing.T) {
	refusal := overflowError{overflow: true}
	model := &callModel{steps: []any{refusal, finalScript("fits")}}
	transcript := &editableTranscript{messages: []Message{UserMessage("old"), AssistantMessage("older")}}
	var refusals []error
	preparer := preparerFunc(func(_ context.Context, p PendingTurn) (TurnPrepared, error) {
		refusals = append(refusals, p.Refusal)
		if p.Overflow {
			transcript.messages = transcript.messages[2:]
		}
		return TurnPrepared{Changed: p.Overflow}, nil
	})
	session := newPreparedSession(t, model, transcript, preparer)
	if _, err := session.Prompt(context.Background(), UserMessage("go")); err != nil {
		t.Fatal(err)
	}
	var got overflowError
	if len(refusals) != 2 || refusals[0] != nil || !errors.As(refusals[1], &got) || got != refusal {
		t.Fatalf("refusals seen = %v, want [nil, the provider's error]", refusals)
	}
}

// A second refusal ends the Run: the Turn is retried once, never again.
func TestASecondRefusalAsTooLongEndsTheRunWithContextOverflow(t *testing.T) {
	model := &callModel{steps: []any{overflowError{overflow: true}, overflowError{overflow: true}, finalScript("never")}}
	var overflows int
	preparer := preparerFunc(func(_ context.Context, p PendingTurn) (TurnPrepared, error) {
		if p.Overflow {
			overflows++
		}
		return TurnPrepared{Changed: p.Overflow}, nil
	})
	session := newPreparedSession(t, model, nil, preparer)
	result, err := session.Prompt(context.Background(), UserMessage("go"))
	if !IsCode(err, ErrContextOverflow) || result.Status != RunFailed {
		t.Fatalf("result = %+v, err = %v; want %s", result, err, ErrContextOverflow)
	}
	if model.calls() != 2 || overflows != 1 {
		t.Fatalf("%d model calls and %d overflow chances, want 2 and 1", model.calls(), overflows)
	}
}

// A preparer that changes nothing leaves the refusal as it was.
func TestARefusalNoPreparerCanMendEndsTheRun(t *testing.T) {
	for name, preparers := range map[string][]TurnPreparer{
		"no preparer":         nil,
		"nothing was changed": {preparerFunc(func(context.Context, PendingTurn) (TurnPrepared, error) { return TurnPrepared{}, nil })},
	} {
		t.Run(name, func(t *testing.T) {
			model := &callModel{steps: []any{overflowError{overflow: true}, finalScript("never")}}
			options := []Option{WithModel(model)}
			for _, preparer := range preparers {
				options = append(options, WithExtension(preparer))
			}
			session, err := NewSession(options...)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close(context.Background()) }()
			result, err := session.Prompt(context.Background(), UserMessage("go"))
			if !IsCode(err, ErrContextOverflow) || result.Status != RunFailed || model.calls() != 1 {
				t.Fatalf("result = %+v, err = %v, %d calls", result, err, model.calls())
			}
		})
	}
}

// A preparer ends a Run with the Gotato error it returns, and any other
// failure is an extension failure.
func TestATurnPreparerEndsTheRunWithItsError(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want ErrorCode
	}{
		"limit":    {ErrorOf(ErrLimitExceeded, "past the ceiling"), ErrLimitExceeded},
		"plain":    {errors.New("boom"), ErrExtensionFailure},
		"canceled": {context.Canceled, ErrExtensionFailure},
	} {
		t.Run(name, func(t *testing.T) {
			model := &callModel{steps: []any{finalScript("never")}}
			session := newPreparedSession(t, model, nil, preparerFunc(func(context.Context, PendingTurn) (TurnPrepared, error) {
				return TurnPrepared{}, tc.err
			}))
			result, err := session.Prompt(context.Background(), UserMessage("go"))
			if !IsCode(err, tc.want) || model.calls() != 0 {
				t.Fatalf("result = %+v, err = %v, %d calls; want %s before any call", result, err, model.calls(), tc.want)
			}
			if tc.err == context.Canceled && result.Status != RunCanceled {
				t.Errorf("status = %s, want %s", result.Status, RunCanceled)
			}
		})
	}
}
