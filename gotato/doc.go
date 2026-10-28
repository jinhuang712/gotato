// Package gotato is the Gotato Core: the live Session, its Loop and the
// contracts it runs against. It imports only the standard library.
//
// NewSession builds a Session from an Agent's parts, given as options. Prompt
// runs one Run: the Session commits Messages to a Transcript, asks a
// ContextBuilder for the ModelContext of each Turn, streams the Model,
// executes the requested Tools, and repeats until the Model stops calling
// Tools or a limit, a TurnStopper, or cancellation ends the Run. Extensions
// hook into the stages of a Turn, and every step is an Event: a value of a Go
// type of its own, such as RunStarted or TextDelta, that a caller reads from a
// Subscription (Session.Subscribe, SubscribeRun). A stored Event
// is written before it is sent, and the Session never waits for a subscriber.
//
//	s, err := gotato.NewSession(context.Background(), gotato.WithModel(model), gotato.WithTools(tools...))
//	if err != nil {
//	    return err
//	}
//	defer s.Close(ctx)
//	result, err := s.Prompt(ctx, gotato.UserMessage("hello"))
//
// Steer and FollowUp add the user's words to a Run in flight. What is not the
// user's words goes in as a notice, a Message of its own kind (RoleNotice):
// Session.Notice queues one, it is committed right before the next model call,
// and it starts no Round and no Run. The model reads it as
// <notice kind="…">…</notice>.
//
// Session Records and stores, context strategies, Tool registries, providers,
// and the service layer live in sibling packages that build on these
// contracts.
package gotato
