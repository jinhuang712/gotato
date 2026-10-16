// Package gotato is the Gotato Core: the live Session, its Loop and the
// contracts it runs against. It imports only the standard library.
//
// NewSession builds a Session from an Agent's parts, given as options. Prompt
// runs one Run: the Session commits Messages to a Transcript, asks a
// ContextBuilder for the ModelContext of each Turn, streams the Model,
// executes the requested Tools, and repeats until the Model stops calling
// Tools or a limit, a TurnStopper, or cancellation ends the Run. Extensions
// hook into the stages of a Turn, and every step is published as a runtime
// Event.
//
//	s, err := gotato.NewSession(gotato.WithModel(model), gotato.WithTools(tools...))
//	if err != nil {
//	    return err
//	}
//	defer s.Close(ctx)
//	result, err := s.Prompt(ctx, gotato.UserMessage("hello"))
//
// Session Records and stores, context strategies, Tool registries, providers,
// and the service layer live in sibling packages that build on these
// contracts.
package gotato
