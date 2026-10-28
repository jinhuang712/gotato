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
//
// # Where things are
//
// The package has four subjects. Each is a group of files, so a reader can
// start from the name of a file:
//
//   - Session and Loop: the live Session and the Run it executes. session.go
//     (Session, NewSession, Prompt), sessionoptions.go (its options),
//     sessionlifecycle.go (Abort, Close, the command loop), loop.go (the Turn
//     loop), reply.go (reading the Model's reply), eventwrite.go and commit.go
//     (what a Turn writes), model.go and types.go (Model, Message), queue.go
//     (Steer, FollowUp), limits.go, validate.go, retry.go, notice.go, tags.go,
//     clone.go, coalesce.go and cutoff.go.
//   - Tools: what the Model may call. tool.go (Tool, ToolSpec, ToolResult),
//     toolfunc.go and toolschema.go (a Tool from a Go function), toolbatch.go
//     (parallel calls), toolset.go (ToolSet), toolregistry.go (the Session's
//     catalog), toolsource.go and toolrefresh.go (Tools that change between
//     Turns), toolsearch.go and bm25.go (loading Tools on demand) and spill.go
//     (a long result kept out of the Context).
//   - Events and Feed: what a Session says about itself. events.go and
//     eventkinds.go (every Event type and its kind), eventheader.go and
//     eventcodec.go (the Header and the JSON form), feed.go and subscription.go
//     (Feed, EventSource, Subscription), journal.go (storing Events before they
//     are sent) and errors.go (RuntimeError and its codes).
//   - Context contracts: what the Model is shown and how a store keeps it.
//     transcript.go (Transcript), context.go (ContextBuilder, ModelContext),
//     request.go (building the Model's request), turnprep.go (TurnPreparer),
//     inspect.go (InspectContext), segment.go (Segment and compaction values)
//     and extensions.go (the stages of a Turn that an Extension hooks).
//
// The names, in the order a reader needs them:
//
//   - Must know: Agent, Session, Run, Model, Tool, Message, Event, Instance.
//   - May need: Runner, Store, Record, Transcript, Context, Turn, Steer,
//     FollowUp, Abort, Continue, Effect, ToolSet, Compaction, Segment, Recap,
//     Limits, Notice, Subscription.
//   - For extenders: Extension and its stages, ContextBuilder, ModelContext,
//     Block, Journal, EventSource, Feed, ToolSource, ToolSearcher,
//     MergedSegment, CacheAnchor.
//
// These stay one package because they refer to each other: a Session emits
// Events and runs Tools, its Events carry Tool and Message values, and its
// Tools return Messages. Split apart, they would import each other in a cycle,
// which Go does not allow.
package gotato
