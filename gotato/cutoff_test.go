package gotato

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// breakingStream plays its events, then fails with err, or, when err is nil,
// waits for its context to end after signalling reached.
type breakingStream struct {
	mu      sync.Mutex
	events  []ModelEvent
	err     error
	reached chan struct{}
}

func (s *breakingStream) Recv(ctx context.Context) (ModelEvent, error) {
	s.mu.Lock()
	if len(s.events) > 0 {
		event := s.events[0]
		s.events = s.events[1:]
		s.mu.Unlock()
		return event, nil
	}
	s.mu.Unlock()
	if s.err != nil {
		return ModelEvent{}, s.err
	}
	if s.reached != nil {
		close(s.reached)
		s.reached = nil
	}
	<-ctx.Done()
	return ModelEvent{}, ctx.Err()
}

func (s *breakingStream) Close() error { return nil }

// breakingModel answers its first call with a stream that breaks, and every
// later call with "again", recording each request.
type breakingModel struct {
	mu       sync.Mutex
	first    *breakingStream
	requests []ModelRequest
}

func (m *breakingModel) Stream(_ context.Context, request ModelRequest) (ModelStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, request)
	if len(m.requests) == 1 {
		return m.first, nil
	}
	return &testStream{events: []ModelEvent{{Kind: ModelEventKindTextDelta, Text: "again"}, {Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}}}, nil
}

func (m *breakingModel) request(i int) ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests[i]
}

func TestCutOffReplyIsCommittedWhenTheStreamBreaks(t *testing.T) {
	streamed := []ModelEvent{
		{Kind: ModelEventKindTextDelta, Text: "hel"},
		{Kind: ModelEventKindReasoningDelta, Text: "thinking"},
		{Kind: ModelEventKindTextDelta, Text: "lo"},
		{Kind: ModelEventKindToolCall, ToolCall: &ToolCall{ID: "call-1", ToolID: "demo.echo", Arguments: []byte(`{}`)}},
	}
	for _, tc := range []struct {
		name   string
		stream func() *breakingStream
		limits *CoreLimits
		cancel bool
		status RunStatus
		code   ErrorCode
	}{
		{name: "provider error", stream: func() *breakingStream {
			return &breakingStream{events: append([]ModelEvent(nil), streamed...), err: errors.New("connection reset")}
		}, status: RunStatusFailed, code: CodeModelFailure},
		{name: "canceled", stream: func() *breakingStream {
			return &breakingStream{events: append([]ModelEvent(nil), streamed...), reached: make(chan struct{})}
		}, cancel: true, status: RunStatusCanceled, code: CodeCanceled},
		{name: "deadline", stream: func() *breakingStream {
			return &breakingStream{events: append([]ModelEvent(nil), streamed...), reached: make(chan struct{})}
		}, limits: &CoreLimits{ModelCallDeadline: 20 * time.Millisecond}, status: RunStatusDeadlineExceeded, code: CodeDeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := tc.stream()
			reached := stream.reached
			transcript := &recordingTranscript{}
			options := []SessionOption{WithModel(&breakingModel{first: stream}), WithTranscript(transcript)}
			if tc.limits != nil {
				limits := DefaultLimits()
				limits.ModelCallDeadline = tc.limits.ModelCallDeadline
				options = append(options, WithLimits(limits))
			}
			session, err := NewSession(context.Background(), options...)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close(context.Background()) }()
			if tc.cancel {
				// Abort cancels the Run and still returns its settled result,
				// as service.Runner does for a cancel request.
				go func() {
					<-reached
					session.Abort()
				}()
			}
			result, err := session.Prompt(context.Background(), UserMessage("hi"))
			if err == nil || result.Status != tc.status || result.Error == nil || result.Error.Code != tc.code {
				t.Fatalf("result = %+v, err = %v; want %s with %s", result, err, tc.status, tc.code)
			}
			messages := transcript.Messages()
			if len(messages) != 2 {
				t.Fatalf("transcript = %+v, want the prompt and the cut-off reply", messages)
			}
			reply := messages[1]
			if reply.Role != RoleAssistant || reply.StopReason != StopReasonCutOff || TextOf(reply) != "hello" {
				t.Fatalf("cut-off reply = %+v", reply)
			}
			if len(reply.Parts) != 1 || len(reply.ToolCalls) != 0 || ReasoningOf(reply) != "" {
				t.Fatalf("cut-off reply keeps only its text: %+v", reply)
			}
		})
	}
}

func TestStreamThatBreaksBeforeAnyTextCommitsNothing(t *testing.T) {
	stream := &breakingStream{events: []ModelEvent{{Kind: ModelEventKindReasoningDelta, Text: "thinking"}}, err: errors.New("connection reset")}
	transcript := &recordingTranscript{}
	session, err := NewSession(context.Background(), WithModel(&breakingModel{first: stream}), WithTranscript(transcript))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	if _, err := session.Prompt(context.Background(), UserMessage("hi")); err == nil {
		t.Fatal("Prompt succeeded on a broken stream")
	}
	if messages := transcript.Messages(); len(messages) != 1 || messages[0].Role != RoleUser {
		t.Fatalf("transcript = %+v, want only the prompt", messages)
	}
}

func TestCutOffReplyIsNotSentToTheModel(t *testing.T) {
	model := &breakingModel{first: &breakingStream{events: []ModelEvent{{Kind: ModelEventKindTextDelta, Text: "half an answ"}}, err: errors.New("connection reset")}}
	transcript := &recordingTranscript{}
	session, err := NewSession(context.Background(), WithModel(model), WithTranscript(transcript))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	if _, err := session.Prompt(context.Background(), UserMessage("first")); err == nil {
		t.Fatal("Prompt succeeded on a broken stream")
	}
	if _, err := session.Prompt(context.Background(), UserMessage("second")); err != nil {
		t.Fatal(err)
	}
	request := model.request(1)
	if len(request.Messages) != 2 || TextOf(request.Messages[0]) != "first" || TextOf(request.Messages[1]) != "second" {
		t.Fatalf("second request = %+v, want both prompts and no cut-off reply", request.Messages)
	}
	if got := len(transcript.Messages()); got != 4 {
		t.Fatalf("transcript has %d Messages, want 4 with the cut-off reply kept", got)
	}
}

func TestContinueAfterACutOffReplyAsksAgain(t *testing.T) {
	model := &breakingModel{first: &breakingStream{events: []ModelEvent{{Kind: ModelEventKindTextDelta, Text: "half"}}, err: errors.New("connection reset")}}
	transcript := &recordingTranscript{}
	session, err := NewSession(context.Background(), WithModel(model), WithTranscript(transcript))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	if _, err := session.Prompt(context.Background(), UserMessage("question")); err == nil {
		t.Fatal("Prompt succeeded on a broken stream")
	}
	result, err := session.Continue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalMessage == nil || TextOf(*result.FinalMessage) != "again" {
		t.Fatalf("Continue = %+v", result)
	}
	if request := model.request(1); len(request.Messages) != 1 || TextOf(request.Messages[0]) != "question" {
		t.Fatalf("Continue sent %+v, want the question alone", request.Messages)
	}
	messages := transcript.Messages()
	if len(messages) != 3 || messages[1].StopReason != StopReasonCutOff || TextOf(messages[2]) != "again" {
		t.Fatalf("transcript = %+v", messages)
	}
}

func TestForModelLeavesOutCutOffReplies(t *testing.T) {
	cut := Message{Role: RoleAssistant, Parts: []ContentPart{{Kind: ContentKindText, Text: "half"}}, StopReason: StopReasonCutOff}
	out := ForModel([]Message{UserMessage("q"), cut, UserMessage("again")})
	if len(out) != 2 || TextOf(out[0]) != "q" || TextOf(out[1]) != "again" {
		t.Fatalf("ForModel = %+v", out)
	}
}

func TestReplyModelIsRecordedAndNeverSent(t *testing.T) {
	model := &testModel{scripts: [][]ModelEvent{
		{{Kind: ModelEventKindTextDelta, Text: "first"}, {Kind: ModelEventKindDone, StopReason: StopReasonEndTurn, Model: "fallback-model"}},
		{{Kind: ModelEventKindTextDelta, Text: "second"}, {Kind: ModelEventKindDone, StopReason: StopReasonEndTurn}},
	}}
	transcript := &recordingTranscript{}
	session, err := NewSession(context.Background(), WithModel(model), WithTranscript(transcript))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	for _, prompt := range []string{"one", "two"} {
		if _, err := session.Prompt(context.Background(), UserMessage(prompt)); err != nil {
			t.Fatal(err)
		}
	}
	messages := transcript.Messages()
	if len(messages) != 4 || messages[1].Model != "fallback-model" || messages[3].Model != "" {
		t.Fatalf("transcript = %+v, want the first reply's model only", messages)
	}
	for _, sent := range model.lastRequest().Messages {
		if sent.Model != "" {
			t.Fatalf("request carries a reply's model: %+v", sent)
		}
	}
}
