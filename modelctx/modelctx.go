// Package modelctx builds what the Model sees now.
//
// A Session is what happened; a ModelContext is the projection of it that one
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
// closes everything since the last Checkpoint into a Segment built by code
// (the user's words and each Run's final reply word for word, a line per tool
// call, and a Recap if a RecapWriter is set) and keeps a tail as it was. The
// Messages it closes stay in the store. Compaction can be invoked directly
// (Compact) or triggered by AutoCompact, a gotato.TurnPreparer, before any model
// call: past a ceiling in tokens, which the model's window gives by default,
// and once when the provider refuses a request as too long. Segments have a
// budget too, half the ceiling or defaultSegmentsBudgetTokens with none: when they
// pass it, a merge merges the oldest into the one merged Segment that
// opens the Context, and ReadSegmentTool (SegmentTools, as a ToolSource) lets
// the model read any Segment back, a merged one's originals included.
// ReadResultTool (ResultTools) is its sibling for a Tool Result that was
// spilled into the Record: the Context holds a preview and a ref, and the model
// reads the rest page by page, with the paging of read_segment.
//
//	live, _ := gotato.NewSession(ctx,
//	    gotato.WithModel(m),
//	    gotato.WithInstruction(agentsMD),
//	    gotato.WithTranscript(s),
//	    gotato.WithExtension(modelctx.AutoCompact(s, modelctx.CompactPolicy{CeilingTokens: 60000})),
//	)
package modelctx
