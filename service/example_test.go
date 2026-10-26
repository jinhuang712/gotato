package service_test

import (
	"context"
	"fmt"

	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// A Runner keeps each Session in a Store, so any instance that shares the
// Store can run its next message.
func ExampleRunner_Run() {
	runner, err := service.New(service.Config{
		Store:  session.NewMemoryStore(), // session.NewFileStore(dir), or your own session.Store
		Agents: []service.Agent{{Name: "default", Model: testkit.EchoModel{}}},
	})
	if err != nil {
		panic(err)
	}
	ctx := context.Background()
	defer func() { _ = runner.Close(ctx) }()

	first, err := runner.Run(ctx, service.RunRequest{Prompt: "hello"})
	if err != nil {
		panic(err)
	}
	// The same Session goes on, here or on another instance.
	second, err := runner.Run(ctx, service.RunRequest{SessionID: first.SessionID, Prompt: "again"})
	if err != nil {
		panic(err)
	}
	fmt.Println(first.FinalText)
	fmt.Println(second.FinalText)
	// Output:
	// echo: hello
	// echo: again
}
