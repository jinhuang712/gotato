// Package gotato is the Gotato Core: the Agent loop and the contracts it runs
// against. It imports only the standard library.
//
// NewAgent builds an Agent from options. Prompt runs one Run: the Agent
// commits Messages to a Transcript, asks a ContextBuilder for the ModelContext
// of each Turn, streams the Model, executes the requested Tools, and repeats
// until the Model stops calling Tools or a limit, a TurnStopper, or cancellation
// ends the Run. Extensions hook into the stages of a Turn, and every step is
// published as a runtime Event.
//
//	agent, err := gotato.NewAgent(gotato.WithModel(model), gotato.WithTools(tools...))
//	if err != nil {
//	    return err
//	}
//	defer agent.Close(ctx)
//	result, err := agent.Prompt(ctx, gotato.UserMessage("hello"))
//
// Sessions, context strategies, Tool registries, providers, and the service
// layer live in sibling packages that build on these contracts.
package gotato
