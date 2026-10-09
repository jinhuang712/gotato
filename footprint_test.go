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

	"github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

const (
	footprintAgents  = 100
	footprintRounds  = 25
	footprintPayload = 4096
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
			Status:  gotato.ToolResultOK,
			Content: []gotato.ContentPart{{Kind: gotato.ContentText, Text: string(body)}},
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

// BenchmarkAgentFootprint measures heap per agent: idle, and after a 25-round
// tool loop with 100 KB of unique tool output per agent, with 100 agents
// running concurrently.
//
//	go test -run '^$' -bench AgentFootprint -benchtime 1x .
func BenchmarkAgentFootprint(b *testing.B) {
	ctx := context.Background()
	for range b.N {
		base := heapInUse()

		agents := make([]gotato.RuntimeAgent, footprintAgents)
		for i := range agents {
			agent, err := gotato.NewAgent(
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

		b.ReportMetric(float64(idle-base)/footprintAgents, "idle-heap-B/agent")
		b.ReportMetric(float64(active-base)/footprintAgents, "active-heap-B/agent")
		b.ReportMetric(float64(footprintRounds*footprintPayload), "transcript-tool-B/agent")
	}
}
