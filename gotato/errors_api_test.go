package gotato_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

// The text of a Model's error stays on the Cause: the Message of the Run's
// error is fixed, because the service layer returns it to HTTP clients.
func TestModelErrorTextStaysOffTheMessage(t *testing.T) {
	s, err := gotato.NewSession(context.Background(), gotato.WithModel(erroringModel{errors.New("dial tcp 10.0.0.7:443: secret-host refused")}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	_, err = s.Prompt(context.Background(), gotato.UserMessage("hi"))
	var re *gotato.RuntimeError
	if !errors.As(err, &re) || re.Code != gotato.CodeModelFailure {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(re.Message, "secret-host") || strings.Contains(re.Error(), "secret-host") {
		t.Fatalf("foreign text in the public error: %q", re.Error())
	}
	if !errors.Is(err, re.Cause) || !strings.Contains(re.Cause.Error(), "secret-host") {
		t.Fatalf("the Model's error is not on the Cause: %v", re.Cause)
	}
}

type erroringModel struct{ err error }

func (m erroringModel) Stream(context.Context, gotato.ModelRequest) (gotato.ModelStream, error) {
	return nil, m.err
}

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

const privateText = "password=private-sentinel"

type fixedSet struct{ tool gotato.Tool }

func (s fixedSet) Spec() gotato.ToolSetSpec                     { return gotato.ToolSetSpec{Name: "catalog"} }
func (s fixedSet) Tools(context.Context) ([]gotato.Tool, error) { return []gotato.Tool{s.tool}, nil }

type rawErrorSearcher struct{}

func (rawErrorSearcher) Search(context.Context, string, []gotato.ToolSpec) ([]string, error) {
	return nil, errors.New("private-backend " + privateText)
}

type panicSearcher struct{}

func (panicSearcher) Search(context.Context, string, []gotato.ToolSpec) ([]string, error) {
	panic(privateText)
}

type publicSearchError struct{}

func (publicSearchError) Error() string  { return "private-backend " + privateText }
func (publicSearchError) Public() string { return "the index is unavailable" }

type publicErrorSearcher struct{}

func (publicErrorSearcher) Search(context.Context, string, []gotato.ToolSpec) ([]string, error) {
	return nil, publicSearchError{}
}

type panicStopper struct{}

func (panicStopper) Stop(context.Context, gotato.TurnSnapshot) (gotato.StopDecision, error) {
	panic(privateText)
}

// searchResultText runs a Session whose Model calls the search Tool and
// returns the text of the failed Tool Result the Model reads.
func searchResultText(t *testing.T, searcher gotato.ToolSearcher) string {
	t.Helper()
	ctx := context.Background()
	tool, err := gotato.NewFuncTool("lookup", "lookup", func(context.Context, struct{}) (string, error) { return "ok", nil })
	if err != nil {
		t.Fatal(err)
	}
	model := testkit.NewFakeModel(testkit.ToolCalls(gotato.ToolCall{ID: "search-call", ToolID: gotato.SearchToolName, Arguments: []byte(`{"queries":["lookup"]}`)}), testkit.Text("done"))
	s, err := gotato.NewSession(ctx, gotato.WithModel(model), gotato.WithToolSet(fixedSet{tool}), gotato.WithToolSearcher(searcher))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(ctx) }()
	if _, err = s.Prompt(ctx, gotato.UserMessage("find tool")); err != nil {
		t.Fatal(err)
	}
	for _, m := range s.Transcript().Messages() {
		if m.ToolResult != nil {
			return m.ToolResult.SafeError
		}
	}
	t.Fatal("no tool result")
	return ""
}

// A ToolSearcher's error and panic are the application's: the text is kept on
// the Cause, and the Model reads a fixed one.
func TestToolSearcherFailureTextStaysPrivate(t *testing.T) {
	for name, searcher := range map[string]gotato.ToolSearcher{"error": rawErrorSearcher{}, "panic": panicSearcher{}} {
		t.Run(name, func(t *testing.T) {
			if text := searchResultText(t, searcher); text == "" || strings.Contains(text, privateText) {
				t.Fatalf("the Model reads %q", text)
			}
		})
	}
}

// A PublicError of a ToolSearcher keeps its own text.
func TestToolSearcherPublicErrorKeepsItsText(t *testing.T) {
	text := searchResultText(t, publicErrorSearcher{})
	if !strings.Contains(text, "the index is unavailable") || strings.Contains(text, privateText) {
		t.Fatalf("the Model reads %q", text)
	}
}

// A Tool that panics does not hand what it panicked with to the Model.
func TestToolPanicTextStaysOutOfModelInput(t *testing.T) {
	tool, err := gotato.NewFuncTool("probe", "a probe", func(context.Context, struct{}) (string, error) { panic(privateText) })
	if err != nil {
		t.Fatal(err)
	}
	model := testkit.NewFakeModel(testkit.ToolCalls(gotato.ToolCall{ID: "p1", ToolID: "probe", Arguments: []byte(`{}`)}), testkit.Text("done"))
	s, err := gotato.NewSession(context.Background(), gotato.WithModel(model), gotato.WithTools(tool))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	if _, err = s.Prompt(context.Background(), gotato.UserMessage("probe")); err != nil {
		t.Fatal(err)
	}
	req, ok := model.LastRequest()
	if !ok {
		t.Fatal("no request")
	}
	results := testkit.ToolResults(req)
	if len(results) == 0 || strings.Contains(strings.Join(results, "\n"), privateText) {
		t.Fatalf("the Model reads %q", results)
	}
}

// An Extension that panics fails the Run with a fixed Message, and keeps what
// it panicked with on the Cause.
func TestExtensionPanicTextStaysOnTheCause(t *testing.T) {
	ctx := context.Background()
	s, err := gotato.NewSession(ctx, gotato.WithModel(testkit.EchoModel{}), gotato.WithExtension(panicStopper{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(ctx) }()
	result, err := s.Prompt(ctx, gotato.UserMessage("hello"))
	var failure *gotato.RuntimeError
	if err == nil || !errors.As(err, &failure) {
		t.Fatalf("err = %v, result = %+v", err, result)
	}
	if strings.Contains(failure.Error(), privateText) || result.Error != nil && strings.Contains(result.Error.Error(), privateText) {
		t.Fatalf("the failure says %q", failure.Error())
	}
	if !strings.Contains(failure.Cause.Error(), privateText) {
		t.Fatalf("the Cause lost the panic: %v", failure.Cause)
	}
}

// hostileTranscript is a Transcript that panics in the one method named by
// panics, and offers the optional ones a Session asks of it.
type hostileTranscript struct {
	*holdJournal
	panics string
}

func (t *hostileTranscript) Len() int {
	if t.panics == "Len" {
		panic("boom-len")
	}
	return t.holdJournal.Len()
}

func (t *hostileTranscript) Messages() []gotato.Message {
	if t.panics == "Messages" {
		panic("boom-messages")
	}
	return t.holdJournal.Messages()
}

func (t *hostileTranscript) Durable() bool {
	if t.panics == "Durable" {
		panic("boom-durable")
	}
	return true
}

func (t *hostileTranscript) KeepsSpilledResults() bool { return false }

func (t *hostileTranscript) LoadedTools() []string {
	if t.panics == "LoadedTools" {
		panic("boom-loaded")
	}
	return nil
}

// A panic in the Transcript's Len, Messages or Durable, or in a host's
// WithRunStart function, fails the Run with a code and keeps the process.
func TestPanicInAHostReadFailsTheRun(t *testing.T) {
	cases := []struct {
		name string
		with func() []gotato.SessionOption
		code gotato.ErrorCode
		text string
	}{
		{"Len", func() []gotato.SessionOption {
			return []gotato.SessionOption{gotato.WithTranscript(&hostileTranscript{holdJournal: newHoldJournal(nil), panics: "Len"})}
		}, gotato.CodeInternalInvariant, "Transcript panic"},
		{"Messages", func() []gotato.SessionOption {
			return []gotato.SessionOption{gotato.WithTranscript(&hostileTranscript{holdJournal: newHoldJournal(nil), panics: "Messages"})}
		}, gotato.CodeInternalInvariant, "Transcript panic"},
		{"Durable", func() []gotato.SessionOption {
			return []gotato.SessionOption{gotato.WithTranscript(&hostileTranscript{holdJournal: newHoldJournal(nil), panics: "Durable"})}
		}, gotato.CodeInternalInvariant, "Transcript panic"},
		{"WithRunStart", func() []gotato.SessionOption {
			return []gotato.SessionOption{gotato.WithRunStart(func(gotato.RunID) { panic("boom-start") })}
		}, gotato.CodeExtensionFailure, "WithRunStart panic"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := gotato.NewSession(context.Background(), append([]gotato.SessionOption{gotato.WithModel(testkit.EchoModel{})}, c.with()...)...)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close(context.Background()) }()
			result, err := s.Prompt(context.Background(), gotato.UserMessage("hi"))
			var re *gotato.RuntimeError
			if !errors.As(err, &re) || re.Code != c.code || re.Message != c.text {
				t.Fatalf("err = %v, want %s %q", err, c.code, c.text)
			}
			if result.Status != gotato.RunStatusFailed {
				t.Fatalf("status = %s", result.Status)
			}
		})
	}
}

// A panic in LoadedTools fails the Session's construction, with a code.
func TestPanicInLoadedToolsFailsNewSession(t *testing.T) {
	_, err := gotato.NewSession(context.Background(), gotato.WithModel(testkit.EchoModel{}),
		gotato.WithTranscript(&hostileTranscript{holdJournal: newHoldJournal(nil), panics: "LoadedTools"}))
	if !gotato.IsCode(err, gotato.CodeInternalInvariant) {
		t.Fatalf("err = %v", err)
	}
}

// panicText is a PublicError whose Public panics.
type panicText struct{}

func (panicText) Error() string  { return "x" }
func (panicText) Public() string { panic("boom-public") }

func TestPublicTextSurvivesAPanickingPublic(t *testing.T) {
	if got := gotato.PublicText(panicText{}, "fallback"); got != "fallback" {
		t.Fatalf("PublicText = %q", got)
	}
}
