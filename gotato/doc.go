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
// The files stand in three layers, from the bottom up. A file may use what
// its own layer or a lower one declares, never a higher one; TestFileLayers
// (layers_test.go) holds every file to its layer.
//
//  1. Values and contracts: what a Session, its Events and its Tools are
//     made of. types.go (Message, RunID, NoticeInfo, SpilledResult),
//     model.go (Model), tool.go (Tool, ToolSpec, ToolResult), toolschema.go
//     (a Tool's JSON schema), events.go and eventkinds.go (every Event type
//     and its kind), eventheader.go and eventcodec.go (the Header and the
//     JSON form), errors.go (RuntimeError and its codes), limits.go, retry.go,
//     segment.go (Segment and compaction values), tags.go, clone.go and
//     validate.go.
//  2. Mechanism that needs no Session: parts a caller can read without it.
//     transcript.go (Transcript), journal.go (storing Events before they are
//     sent), feed.go and subscription.go (Feed, EventSource, Subscription),
//     context.go (ContextBuilder, ModelContext), extensions.go (the stages of
//     a Turn that an Extension hooks), turnprep.go (TurnPreparer), toolset.go
//     (ToolSet), toolfunc.go (a Tool from a Go function), bm25.go (ranking for
//     Tool search), coalesce.go and cutoff.go (shaping a reply).
//  3. The Session and its Loop, and everything that runs on them. session.go
//     (Session, NewSession, Prompt), sessionoptions.go (every SessionOption,
//     with sessionConfig), sessionlifecycle.go (Abort, Close, the command
//     loop), loop.go (the Turn loop), reply.go (reading the Model's reply),
//     request.go (the Model's request), commit.go and eventwrite.go (what a
//     Turn writes), queue.go (Steer, FollowUp), notice.go (Notice), inspect.go
//     (InspectContext), spill.go (a long result kept out of the Context), and
//     for Tools toolregistry.go (the Session's catalog), toolsource.go and
//     toolrefresh.go (Tools that change between Turns), toolsearch.go (loading
//     Tools on demand) and toolbatch.go (parallel calls).
//
// A SessionOption is declared in sessionoptions.go even when it configures a
// part of layer 2, because it names sessionConfig, which belongs to layer 3.
// A file of layer 3 may use any file below it, and the files inside one layer
// may use each other. knownBackEdges in layers_test.go lists the uses that
// point up; it is empty, and it may only shrink.
//
// The names, in the order a reader needs them:
//
//   - Must know: Agent, Session, Run, Model, Tool, Message, Event, Instance.
//   - May need: Runner, Store, Record, Transcript, Context, Turn, Steer,
//     FollowUp, Abort, Continue, Effect, ToolSet, Compaction, Segment, Recap,
//     Limits, Notice, Subscription.
//   - For extenders: Extension and its stages, ContextBuilder, ModelContext,
//     ContextBlock, Journal, EventSource, Feed, ToolSource, ToolSearcher,
//     MergedSegment, CacheAnchor.
//
// These stay one package because they refer to each other: a Session emits
// Events and runs Tools, its Events carry Tool and Message values, and its
// Tools return Messages. Split apart, they would import each other in a cycle,
// which Go does not allow.
package gotato
