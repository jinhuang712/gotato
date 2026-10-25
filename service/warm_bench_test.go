package service_test

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

func benchHeap() uint64 {
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.HeapInuse
}

// warmedRunner returns a Runner over a new store in which sessions Sessions of
// messages Messages each (about 200 bytes, distinct in every Session) have each
// run once. With ttl negative a Run keeps nothing in memory.
func warmedRunner(b *testing.B, sessions, messages int, ttl time.Duration) *service.Runner {
	b.Helper()
	ctx := context.Background()
	runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{
		{Name: "echo", Model: testkit.EchoModel{}, Warm: service.WarmPolicy{TTL: ttl, NoIdleCompaction: true}},
	}})
	if err != nil {
		b.Fatal(err)
	}
	for i := range sessions {
		history := make([]gotato.Message, messages)
		for m := range history {
			text := fmt.Sprintf("session %d message %d ", i, m) + strings.Repeat("x", 180)
			if m%2 == 0 {
				history[m] = gotato.UserMessage(text)
			} else {
				history[m] = gotato.AssistantMessage(text)
			}
		}
		created, err := runner.CreateSession(ctx, "echo", nil, session.WithMessages(history...))
		if err != nil {
			b.Fatal(err)
		}
		if _, err := runner.Run(ctx, service.RunRequest{SessionID: created.ID(), Prompt: "go"}); err != nil {
			b.Fatal(err)
		}
	}
	return runner
}

// BenchmarkWarmSessionFootprint measures G-B08 for the warm Session: the heap
// a warm copy holds, per Session, with a history of 10, 100 and 1,000
// Messages. It builds 100 Sessions that have each run once, twice: once with
// each Session kept warm, and once with none kept. What the Runner and the
// store hold is the same in both, so the difference is the warm copies.
//
//	go test -run '^$' -bench WarmSessionFootprint -benchtime 1x ./service
func BenchmarkWarmSessionFootprint(b *testing.B) {
	const sessions = 100
	for _, messages := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("messages=%d", messages), func(b *testing.B) {
			for range b.N {
				coldHeap := func() uint64 {
					base := benchHeap()
					cold := warmedRunner(b, sessions, messages, -1)
					heap := benchHeap() - base
					runtime.KeepAlive(cold)
					return heap
				}()
				base := benchHeap()
				warm := warmedRunner(b, sessions, messages, 0)
				if warm.WarmSessions() != sessions {
					b.Fatalf("warm Sessions = %d, want %d", warm.WarmSessions(), sessions)
				}
				warmHeap := benchHeap() - base
				runtime.KeepAlive(warm)
				b.ReportMetric(float64(warmHeap-coldHeap)/sessions, "warm-heap-B/session")
				b.ReportMetric(float64(coldHeap)/sessions, "store-and-runner-B/session")
			}
		})
	}
}
