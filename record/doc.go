// Package record is Gotato's Record: what a Session did, as a log of Entries,
// and the Context the Model reads from it. It does not know where a Record is
// saved: that is package store, which imports this one.
//
// To write a Store adapter, read Entry, Loaded, Info and Header here
// first; the rest of the contract is in package store.
//
// A Record holds the Session's identity, the committed Messages, per-Run
// records, usage, a bounded window of its stored Events, compaction history,
// and free-form application metadata. Once it is stored, it is the Session's
// Event log: every Entry is a stored Event, numbered by its position. Facts,
// which rebuild the Session, and log Entries, which record what happened, are
// both Events (see Entry). A Record implements gotato.Transcript and
// gotato.Journal, so a live gotato.Session commits to it directly, takes its
// ID, and keeps its Events in it:
//
//	r := record.New()
//	s, _ := gotato.NewSession(ctx, gotato.WithModel(model), gotato.WithTranscript(r))
//	s.Prompt(ctx, gotato.UserMessage("hi"))
//
// A compaction can close a stretch of history into a gotato.Segment: the
// Record writes it once, and a Checkpoint lists the Segments the Context
// starts with by number, so the Messages that render a Segment are stored once
// and never change. Tasks, projects, roles, and agent hierarchy are
// application semantics and belong in Metadata.
//
// # The Context
//
// A Session is what happened; a Context is the projection of it that one
// Turn sends to the Model. Within a Session the history is append-only and the
// Model always sees all of it, laid out so provider prompt caches hit:
//
//	system  = instruction                   stable across Runs
//	tools   = visible ToolSpecs, sorted      stable across Turns
//	history = every committed Message        append-only prefix
//
// Nothing that changes every Turn is added: such content is never cached,
// so it costs its full price on every Turn. A fact the model needs now, such
// as the current time, comes from a tool the model calls.
//
// History shrinks only through compaction, an explicit and recorded step that
// closes everything since the last Checkpoint into a Segment (by default built
// by code: the user's words and each Run's final reply word for word, a line per
// tool call, and a Recap if a RecapWriter is set) and keeps a tail as it was. The
// Messages it closes stay in the store. A Compactor is the frame of that step:
// it runs its parts in order (a Trigger, a Cutter, a Builder, a RecapWriter and
// a Picker, each a default a user can replace) over a SegmentStore, and alone
// holds the guarantees, so a part that fails, panics or breaks a rule drops
// the compaction and never the Run, and no part can rewrite a Segment or skip
// its Checkpoint. Compaction can be invoked directly (Compact) or triggered by
// AutoCompact, a gotato.TurnPreparer, before any model call: past a ceiling in
// tokens, which the model's window gives by default, and once when the provider
// refuses a request as too long. Segments have a budget too, half the ceiling or
// defaultSegmentsBudgetTokens with none: when they pass it, a merge merges the
// oldest into the one merged Segment that opens the Context.
//
// ReadBack (gotato.WithReadBack) is the other half: it offers the tool that
// reads back what compaction and the spill moved out of the Context. read_segment
// lets the model read any Segment back, a merged one's originals included;
// read_result returns one Tool Result or Message by its ref (package ref), also
// one that was spilled into the Record, where the Context holds a preview and
// the model reads the rest page by page. Originals says where they are
// (OriginalsOf), and ReadBack owns the one page size the spill, the preview
// and both tools use.
//
//	live, _ := gotato.NewSession(ctx,
//	    gotato.WithModel(m),
//	    gotato.WithInstruction(agentsMD),
//	    gotato.WithTranscript(s),
//	    gotato.WithExtension(record.AutoCompact(s, record.Compactor{CompactPolicy: record.CompactPolicy{CeilingTokens: 60000}})),
//	    gotato.WithReadBack(record.ReadBack{Originals: record.OriginalsOf(rec, st)}),
//	)
package record
