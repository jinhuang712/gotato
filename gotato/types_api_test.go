package gotato_test

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

const (
	footprintSessions = 100
	footprintRounds   = 25
	footprintPayload  = 4096
)

// footprintModel requests one tool call per turn for footprintRounds turns,
// then ends with text. It keeps no copy of the requests it receives, so the
// measurement covers Gotato and the transcript only.
type footprintModel struct{ calls atomic.Int64 }

func (m *footprintModel) Stream(context.Context, gotato.ModelRequest) (gotato.ModelStream, error) {
	n := m.calls.Add(1)
	if n > footprintRounds {
		return &footprintStream{events: testkit.Text("done")}, nil
	}
	call := gotato.ToolCall{ID: gotato.ToolCallID(fmt.Sprintf("call-%d", n)), ToolID: "read", Arguments: []byte(`{}`)}
	return &footprintStream{events: testkit.ToolCalls(call)}, nil
}

type footprintStream struct {
	events []gotato.ModelEvent
	next   int
}

func (s *footprintStream) Recv(context.Context) (gotato.ModelEvent, error) {
	if s.next >= len(s.events) {
		return gotato.ModelEvent{}, io.EOF
	}
	event := s.events[s.next]
	s.next++
	return event, nil
}

func (s *footprintStream) Close() error { return nil }

// readTool returns a distinct 4 KB result on every call, so transcripts share
// no backing memory.
func readTool(agent int) gotato.Tool {
	tool := testkit.NewFakeTool("read", "")
	var calls atomic.Int64
	tool.Handler = func(context.Context, gotato.ToolUse) (gotato.ToolResult, error) {
		body := []byte(strings.Repeat("x", footprintPayload))
		copy(body, fmt.Sprintf("agent-%d-call-%d ", agent, calls.Add(1)))
		return gotato.ToolResult{
			Status:  gotato.ToolResultStatusOK,
			Content: []gotato.ContentPart{{Kind: gotato.ContentKindText, Text: string(body)}},
		}, nil
	}
	return tool
}

func heapInUse() uint64 {
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.HeapInuse
}

// BenchmarkSessionFootprint measures heap per live Session: idle, and after a
// 25-round tool loop with 100 KB of unique tool output per Session, with 100
// Sessions running concurrently.
//
//	go test -run '^$' -bench SessionFootprint -benchtime 1x .
func BenchmarkSessionFootprint(b *testing.B) {
	ctx := context.Background()
	for range b.N {
		base := heapInUse()

		agents := make([]*gotato.Session, footprintSessions)
		for i := range agents {
			agent, err := gotato.NewSession(
				context.Background(),
				gotato.WithModel(&footprintModel{}),
				gotato.WithInstruction("You are a worker."),
				gotato.WithTranscript(session.New()),
				gotato.WithTools(readTool(i)),
			)
			if err != nil {
				b.Fatal(err)
			}
			agents[i] = agent
		}
		idle := heapInUse()

		var wg sync.WaitGroup
		for _, agent := range agents {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := agent.Prompt(ctx, gotato.UserMessage("do the task")); err != nil {
					b.Error(err)
				}
			}()
		}
		wg.Wait()
		active := heapInUse()

		for _, agent := range agents {
			if err := agent.Close(ctx); err != nil {
				b.Fatal(err)
			}
		}
		runtime.KeepAlive(agents)

		b.ReportMetric(float64(idle-base)/footprintSessions, "idle-heap-B/session")
		b.ReportMetric(float64(active-base)/footprintSessions, "active-heap-B/session")
		b.ReportMetric(float64(footprintRounds*footprintPayload), "transcript-tool-B/agent")
	}
}

// When the caller's ctx ends while a Run is in flight, Prompt returns the
// Run's settled result, not an empty one: the Run ends with the ctx, so the
// status says why, and the Session is idle again.
func TestPromptReturnsTheSettledResultWhenItsCtxEnds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ctx    func() (context.Context, context.CancelFunc)
		status gotato.RunStatus
		code   gotato.ErrorCode
	}{
		{"deadline", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 50*time.Millisecond)
		}, gotato.RunStatusDeadlineExceeded, gotato.CodeDeadlineExceeded},
		{"cancel", func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(50*time.Millisecond, cancel)
			return ctx, cancel
		}, gotato.RunStatusCanceled, gotato.CodeCanceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The tool blocks until its ctx ends, as a well-behaved tool does.
			slow, err := gotato.NewFuncTool("slow", "waits for its ctx", func(ctx context.Context, _ struct{}) (string, error) {
				<-ctx.Done()
				return "", ctx.Err()
			})
			if err != nil {
				t.Fatal(err)
			}
			model := testkit.NewFuncModel(func(context.Context, gotato.ModelRequest) (testkit.Script, error) {
				return testkit.ToolCalls(gotato.ToolCall{ID: gotato.ToolCallID(fmt.Sprint(time.Now().UnixNano())), ToolID: "slow", Arguments: []byte(`{}`)}), nil
			})
			session, err := gotato.NewSession(context.Background(), gotato.WithModel(model), gotato.WithTools(slow))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close(context.Background()) }()
			ctx, cancel := tc.ctx()
			defer cancel()
			result, err := session.Prompt(ctx, gotato.UserMessage("go"))
			if !gotato.IsCode(err, tc.code) {
				t.Fatalf("Prompt error = %v, want code %q", err, tc.code)
			}
			if result.Status != tc.status || result.RunID == "" {
				t.Fatalf("result = %+v, want status %q and a RunID", result, tc.status)
			}
			if status := session.Status(); status != gotato.SessionStatusIdle {
				t.Fatalf("Session status = %q after Prompt returned, want idle", status)
			}
		})
	}
}

// truncated is a Turn that spent the whole output limit reasoning and never
// acted: what a reasoning model returns when it plans at length.
func truncated() testkit.Script {
	return testkit.Script{
		{Kind: gotato.ModelEventKindReasoningDelta, Text: "Let me think through every function first..."},
		{Kind: gotato.ModelEventKindDone, StopReason: gotato.StopReasonMaxTokens},
	}
}

// Observed in Mow (I29): a DeepSeek Worker reasoned through all of its work
// in one Turn, hit the output limit, and returned no Tool Call; the Loop took
// the cut-off Message as the final answer and the Run completed with nothing
// done. With MaxTruncatedContinuations set, such a Turn is continued instead.
func TestTruncatedTurnContinuesWithinTheBound(t *testing.T) {
	for _, tc := range []struct {
		name    string
		bound   uint32
		scripts []testkit.Script
		calls   int
		final   string
	}{
		{name: "default settles on the cut-off turn", bound: 0, scripts: []testkit.Script{truncated(), testkit.Text("done")}, calls: 1, final: ""},
		{name: "continued once", bound: 2, scripts: []testkit.Script{truncated(), testkit.Text("done")}, calls: 2, final: "done"},
		{name: "bound reached", bound: 2, scripts: []testkit.Script{truncated(), truncated(), truncated(), testkit.Text("done")}, calls: 3, final: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := testkit.NewFakeModel(tc.scripts...)
			limits := gotato.DefaultLimits()
			limits.MaxTruncatedContinuations = tc.bound
			agent, err := gotato.NewSession(context.Background(), gotato.WithModel(model), gotato.WithLimits(limits))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = agent.Close(context.Background()) }() // test teardown; the Run already settled
			result, err := agent.Prompt(context.Background(), gotato.UserMessage("do the work"))
			if err != nil {
				t.Fatal(err)
			}
			if model.Calls() != tc.calls {
				t.Fatalf("model calls = %d, want %d", model.Calls(), tc.calls)
			}
			if result.Status != gotato.RunStatusCompleted || result.FinalMessage == nil || gotato.TextOf(*result.FinalMessage) != tc.final {
				t.Fatalf("result = %+v", result)
			}
			if tc.calls < 2 {
				return
			}
			// The continuation is a notice the Model sees after the cut-off
			// Turn, so the next request never ends on an assistant, and it is
			// not the user's words (G-F29).
			request, _ := model.LastRequest()
			last := request.Messages[len(request.Messages)-1]
			want := `<notice kind="continue">` + gotato.TruncatedContinuationText + `</notice>`
			if last.Role != gotato.RoleNotice || gotato.TextOf(last) != want {
				t.Fatalf("last message before the continued turn = %+v", last)
			}
		})
	}
}
