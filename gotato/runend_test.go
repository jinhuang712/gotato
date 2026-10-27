package gotato_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/testkit"
)

// maxTurns is the replacement for a Turn cap: a TurnStopper that ends the Run
// after N Turns. The core counts no Turns; a team that wants a cap writes this.
type maxTurns uint32

func (m maxTurns) Stop(_ context.Context, turn gotato.TurnSnapshot) (gotato.StopDecision, error) {
	if uint32(turn.Turn) >= uint32(m) {
		return gotato.StopDecision{Stop: true, Reason: "turn cap"}, nil
	}
	return gotato.StopDecision{}, nil
}

// callsUntil answers every request with a call to the "lookup" tool until the
// request holds answerAfter results, then with a final reply. Zero never
// answers.
func callsUntil(answerAfter int) *testkit.FuncModel {
	return testkit.NewFuncModel(func(_ context.Context, request gotato.ModelRequest) (testkit.Script, error) {
		n := len(testkit.ToolResults(request))
		if answerAfter > 0 && n >= answerAfter {
			return testkit.Text("done"), nil
		}
		return testkit.ToolCalls(gotato.ToolCall{ID: gotato.ToolCallID(fmt.Sprintf("call-%d", n)), ToolID: "lookup", Arguments: []byte(`{}`)}), nil
	})
}

// A team that wants a cap on Turns writes a TurnStopper. The Run ends after
// the third Turn, settles as completed and says why.
func ExampleTurnStopper() {
	session, err := gotato.NewSession(
		gotato.WithModel(callsUntil(0)),
		gotato.WithTool(testkit.NewFakeTool("lookup", "found")),
		gotato.WithExtension(maxTurns(3)),
	)
	if err != nil {
		panic(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	result, err := session.Prompt(context.Background(), gotato.UserMessage("go"))
	fmt.Println(result.Status, result.Metrics.Turns, err)
	// Output: completed 3 <nil>
}

// The core counts no Turns or Tool calls: a Run of 101 Turns, over the old
// defaults of 32 Turns and 128 Tool calls, settles by the model's reply.
func TestARunEndsByTheModelsReplyWhateverItsLength(t *testing.T) {
	session, err := gotato.NewSession(gotato.WithModel(callsUntil(150)), gotato.WithTool(testkit.NewFakeTool("lookup", "found")))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	result, err := session.Prompt(context.Background(), gotato.UserMessage("go"))
	if err != nil || result.Status != gotato.RunCompleted || result.Metrics.Turns != 151 {
		t.Fatalf("result = %+v, err = %v; want completed after 151 Turns", result, err)
	}
}

// A Run with no TurnStopper settles by its ctx: a deadline on it ends the Run
// with deadline_exceeded, and a cancel with cancelled. The core sets none itself.
func TestARunEndsByItsCtx(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		code gotato.ErrorCode
	}{
		{"deadline", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 50*time.Millisecond)
		}, gotato.ErrDeadlineExceeded},
		{"cancel", func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(50*time.Millisecond, cancel)
			return ctx, cancel
		}, gotato.ErrCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := testkit.NewFuncModel(func(context.Context, gotato.ModelRequest) (testkit.Script, error) {
				time.Sleep(10 * time.Millisecond)
				return testkit.ToolCalls(gotato.ToolCall{ID: gotato.ToolCallID(fmt.Sprint(time.Now().UnixNano())), ToolID: "lookup", Arguments: []byte(`{}`)}), nil
			})
			session, err := gotato.NewSession(gotato.WithModel(model), gotato.WithTool(testkit.NewFakeTool("lookup", "found")))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close(context.Background()) }()
			ctx, cancel := tc.ctx()
			defer cancel()
			_, err = session.Prompt(ctx, gotato.UserMessage("go"))
			if !gotato.IsCode(err, tc.code) {
				t.Fatalf("Prompt error = %v, want code %q", err, tc.code)
			}
		})
	}
}

// The core does not cap a Tool Result: an 8 MiB result, over the 4 MiB the old
// cap allowed, reaches the next request whole and the Run completes, when the
// Message limit leaves room for it. Until large results are spilled (G-F35),
// it goes into the Context as it is.
func TestALargeToolResultNoLongerFailsTheRun(t *testing.T) {
	big := strings.Repeat("x", 8<<20)
	limits := gotato.DefaultLimits()
	limits.MaxMessageBytes = 32 << 20
	model := callsUntil(1)
	session, err := gotato.NewSession(gotato.WithModel(model), gotato.WithTool(testkit.NewFakeTool("lookup", big)), gotato.WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	result, err := session.Prompt(context.Background(), gotato.UserMessage("go"))
	if err != nil || result.Status != gotato.RunCompleted {
		t.Fatalf("result = %+v, err = %v; want completed", result, err)
	}
	requests := model.Requests()
	got := testkit.ToolResults(requests[len(requests)-1])
	if len(got) != 1 || len(got[0]) != len(big) {
		t.Fatalf("the last request holds %d results; want one of the whole 8 MiB", len(got))
	}
}
