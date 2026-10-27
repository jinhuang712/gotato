package reftasks

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/jinhuang712/gotato/testkit"

	"github.com/jinhuang712/gotato/gotato"
)

// The long_session task (GOALS.md G-G10 and G-B14): one Session played for
// 2,000 Turns, each with a 20 KB Tool Result, against a fake Model with a
// 128K-token window.
const (
	// LongSessionName is the task's name in reports.
	LongSessionName = "long_session"
	// LongSessionTurns is how many Turns the task plays.
	LongSessionTurns = 2000
	// LongSessionTurnsPerRun is how many Turns one Run takes: a Tool call at
	// each of them but the last, which answers.
	LongSessionTurnsPerRun = 20
	// LongSessionResultBytes is the size of each Tool Result.
	LongSessionResultBytes = 20 << 10
	// LongSessionWindow is the fake Model's window, in tokens.
	LongSessionWindow = 128_000
	// LogsReadID is the Tool the task offers.
	LogsReadID = "logs.read"
)

// LongSessionTask is the long_session task at its full size: 2,000 Turns of
// 20 KB Tool Results in 100 Runs on one Session, against a Model whose window
// is 128K tokens.
func LongSessionTask() Task {
	return LongSession(LongSessionTurns, LongSessionTurnsPerRun, LongSessionResultBytes, LongSessionWindow, false)
}

// LongSessionRecapTask is LongSessionTask with the model's Recap turned on
// (G-B16): the task long_session_recap.
func LongSessionRecapTask() Task {
	return LongSession(LongSessionTurns, LongSessionTurnsPerRun, LongSessionResultBytes, LongSessionWindow, true)
}

// LongSession is the long_session task at the size given: turns Turns in Runs
// of perRun Turns each, every Tool Result resultBytes long, against a fake
// Model whose window is window tokens. With recap, the Agent turns the model's
// Recap on (G-B16), and the fake Model writes one when asked.
//
// The Model plays from the request alone: it calls logs.read at every Turn of
// a Run but the last, which answers, and it reads where it is in the Run from
// the ID of the last Tool call, which a compaction always keeps. It reports the
// size of each request as its input tokens (bytes ÷ 4, see RequestTokens), as a
// provider does, so a Session sees the usage it would see.
func LongSession(turns, perRun, resultBytes, window int, recap bool) Task {
	runs := max(turns/perRun, 1)
	name := LongSessionName
	if recap {
		name += "_recap"
	}
	prompts := make([]string, runs)
	for i := range prompts {
		prompts[i] = fmt.Sprintf("Run %d: read the next %d logs of batch B-%04d, one after the other, and tell me when you are done.", i+1, perRun-1, i+1)
	}
	return Task{
		Name:        name,
		Instruction: "You read the logs of a batch, one part at a time, and say when you are done.",
		Prompts:     prompts,
		Tools:       []gotato.Tool{logsRead(resultBytes)},
		Model:       longSessionModel{parts: perRun - 1},
		Window:      window,
		Recap:       recap,
		Check: func(answers []string) error {
			if len(answers) != runs {
				return fmt.Errorf("got %d answers, want %d", len(answers), runs)
			}
			for i, answer := range answers {
				if want := fmt.Sprintf("Run %d done", i+1); !strings.Contains(answer, want) {
					return fmt.Errorf("answer %d = %q, want it to say %q", i+1, answer, want)
				}
			}
			return nil
		},
	}
}

// logsRead is the Tool of the long_session task: it returns resultBytes of log
// lines for a part of a Run, the same every time. Its Effect is read.
func logsRead(resultBytes int) gotato.Tool {
	tool, err := gotato.NewFuncTool(LogsReadID, "Reads one part of a batch's log.",
		func(_ context.Context, in struct {
			Run  int `json:"run" description:"the run's number"`
			Part int `json:"part" description:"the part of the log"`
		}) (string, error) {
			var b strings.Builder
			for line := 0; b.Len() < resultBytes; line++ {
				fmt.Fprintf(&b, "2026-10-25T10:%02d:%02dZ INFO batch-worker run=%d part=%d line=%04d charge settled, no retry needed, queue depth unchanged\n",
					in.Part%60, line%60, in.Run, in.Part, line)
			}
			return b.String()[:resultBytes], nil
		})
	if err != nil {
		panic(err)
	}
	return gotato.ToolWithEffect(tool, gotato.ToolEffectRead)
}

// RequestTokens estimates the tokens of a request the way the long_session
// task's fake Model and the workload driver both count them: the bytes of its
// instruction, Messages and Tools ÷ 4.
func RequestTokens(request gotato.ModelRequest) int {
	messages, _ := json.Marshal(gotato.ForModel(request.Messages))
	tools, _ := json.Marshal(request.Tools)
	return (len(request.SystemInstructions) + len(messages) + len(tools) + 3) / 4
}

// longRecap is what the fake Model answers when it is asked for a Recap.
const longRecap = "## What happened\n- the user asked for the logs of one batch after another; each was read in full\n## Key facts\n- the batches are numbered B-0001 and up, in the order of the runs"

var (
	longPrompt = regexp.MustCompile(`^Run (\d+):`)
	longCallID = regexp.MustCompile(`^call-(\d+)-(\d+)$`)
)

type longSessionModel struct{ parts int }

// Stream implements gotato.Model.
func (m longSessionModel) Stream(_ context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	script := m.play(request)
	// The reply reports what a provider reports: the request's tokens in, the
	// reply's out.
	out := 0
	for _, event := range script {
		out += (len(event.Text) + 3) / 4
		if event.ToolCall != nil {
			out += (len(event.ToolCall.Arguments) + 3) / 4
		}
	}
	script[len(script)-1].Usage = gotato.Usage{InputTokens: uint64(RequestTokens(request)), OutputTokens: uint64(out)}
	return &stream{events: script}, nil
}

func (m longSessionModel) play(request gotato.ModelRequest) testkit.Script {
	if len(request.Messages) == 0 {
		return testkit.Text("Nothing to do.")
	}
	last := request.Messages[len(request.Messages)-1]
	text := promptText(last)
	switch {
	case last.Role == gotato.RoleUser && (strings.HasPrefix(text, "Write a recap") || strings.HasPrefix(text, "Write one recap")):
		return testkit.Text(longRecap)
	case last.Role == gotato.RoleToolResult && last.ToolResult != nil:
		match := longCallID.FindStringSubmatch(string(last.ToolResult.CallID))
		if match == nil {
			return testkit.Text("I lost my place.")
		}
		var run, part int
		_, _ = fmt.Sscan(match[1], &run)
		_, _ = fmt.Sscan(match[2], &part)
		if part >= m.parts {
			return testkit.Text(fmt.Sprintf("Run %d done after %d reads.", run, part))
		}
		return m.read(run, part+1)
	default:
		match := longPrompt.FindStringSubmatch(text)
		if match == nil {
			return testkit.Text("Which run?")
		}
		var run int
		_, _ = fmt.Sscan(match[1], &run)
		return m.read(run, 1)
	}
}

func (m longSessionModel) read(run, part int) testkit.Script {
	return testkit.ToolCalls(gotato.ToolCall{
		ID:        gotato.ToolCallID(fmt.Sprintf("call-%d-%d", run, part)),
		ToolID:    LogsReadID,
		Arguments: fmt.Appendf(nil, `{"run":%d,"part":%d}`, run, part),
	})
}
