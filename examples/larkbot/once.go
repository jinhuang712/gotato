package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
)

// runOnce sends one prompt through a Runner with an in-memory store and prints
// the reply to stdout. It is the smoke test for a model, without Lark.
func runOnce(model, prompt string) error {
	agent, err := buildAgent(model)
	if err != nil {
		return err
	}
	if c, ok := agent.Model.(io.Closer); ok {
		defer func() { _ = c.Close() }()
	}
	runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{agent}})
	if err != nil {
		return err
	}
	defer func() { _ = runner.Close(context.Background()) }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const id = "once"
	if _, err := runner.CreateSessionExclusive(ctx, agent.Name, id, nil); err != nil {
		return err
	}
	start := time.Now()
	res, err := runner.Send(ctx, service.MessageRequest{SessionID: id, Kind: service.MessageFollowUp, Text: prompt})
	elapsed := time.Since(start).Round(time.Millisecond)
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	if res.Run == nil {
		return fmt.Errorf("no run started")
	}
	fmt.Fprintf(os.Stderr, "status=%s latency=%s\n", res.Run.Result.Status, elapsed)
	fmt.Println(strings.TrimSpace(res.Run.FinalText))
	if res.Run.Result.Status != "completed" {
		return fmt.Errorf("run did not complete (%s)", res.Run.Result.Status)
	}
	return nil
}
