package gotato_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

type panicModel struct{ onStream bool }

func (m panicModel) Stream(context.Context, gotato.ModelRequest) (gotato.ModelStream, error) {
	if m.onStream {
		panic("boom-stream")
	}
	return panicStream{}, nil
}

type panicStream struct{}

func (panicStream) Recv(context.Context) (gotato.ModelEvent, error) { panic("boom-recv") }
func (panicStream) Close() error                                    { panic("boom-close") }

// panicJournal is a Transcript and Journal that panics in Write or Append.
type panicJournal struct {
	*holdJournal
	onWrite, onAppend bool
}

func (j *panicJournal) Write(ctx context.Context, events []gotato.Event) ([]gotato.Event, error) {
	if j.onWrite {
		panic("boom-write")
	}
	return j.holdJournal.Write(ctx, events)
}

func (j *panicJournal) Append(m gotato.Message) error {
	if j.onAppend {
		panic("boom-append")
	}
	return j.holdJournal.Append(m)
}

// A panic in the Model, its stream, the Journal or the Transcript fails the Run
// instead of the process, as a panic in an extension or a Tool does: the
// Message is fixed, and the panic is on the Cause.
func TestPanicOfAModelJournalOrTranscriptFailsTheRun(t *testing.T) {
	cases := []struct {
		name    string
		model   gotato.Model
		journal *panicJournal
		code    gotato.ErrorCode
		message string
		panics  string
	}{
		{"Stream", panicModel{onStream: true}, nil, gotato.CodeModelFailure, "Model panic", "boom-stream"},
		{"Recv", panicModel{}, nil, gotato.CodeModelFailure, "Model panic", "boom-recv"},
		{"Write", testkit.EchoModel{}, &panicJournal{holdJournal: newHoldJournal(nil), onWrite: true}, gotato.CodeStoreFailure, "Journal panic", "boom-write"},
		{"Append", testkit.EchoModel{}, &panicJournal{holdJournal: newHoldJournal(nil), onAppend: true}, gotato.CodeInternalInvariant, "Transcript panic", "boom-append"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			options := []gotato.SessionOption{gotato.WithModel(c.model)}
			if c.journal != nil {
				options = append(options, gotato.WithTranscript(c.journal))
			}
			s, err := gotato.NewSession(context.Background(), options...)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close(context.Background()) }()
			result, err := s.Prompt(context.Background(), gotato.UserMessage("hi"))
			var re *gotato.RuntimeError
			if !errors.As(err, &re) || re.Code != c.code || re.Message != c.message {
				t.Fatalf("err = %v, want %s %q", err, c.code, c.message)
			}
			if result.Status != gotato.RunStatusFailed {
				t.Fatalf("status = %s", result.Status)
			}
			if re.Cause == nil || !strings.Contains(re.Cause.Error(), c.panics) {
				t.Fatalf("the panic is not on the Cause: %v", re.Cause)
			}
		})
	}
}
