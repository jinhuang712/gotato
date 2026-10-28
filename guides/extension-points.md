# Extension points

By the end of this page you know which seam of Gotato to reach for when the default is not what you want, and which seams you will most likely never need. Each one is a Go interface or a function type: you write a small value, and pass it in an option or a field.

## TL;DR

**Common** (most services use one or two of these)

| I want to | Seam |
|---|---|
| call my own model or provider | `gotato.Model` |
| give the agent a capability | `gotato.Tool` (`NewFuncTool`) |
| save Sessions in my own database | `session.Store` |
| change what the model sees | `gotato.ContextBuilder` |
| block a tool call, rewrite its result, or cap the Turns | Extension stages `PreToolUse`, `PostToolUse`, `TurnStopper` |
| bound the load on one pod | `service.Admission` |

**Sometimes**

| I want to | Seam |
|---|---|
| expose a group of tools, such as an MCP server | `gotato.ToolSet` |
| supply tools that change between Turns | `gotato.ToolSource`, `service.Agent.ToolSources` |
| write Recaps with a model | `modelctx.RecapWriter` (`ModelRecap`) |
| prune or reshape the Messages after the builder | `gotato.ContextTransformer`, `gotato.MessageConverter` |

**Rare** (a specific need, or a store adapter's concern)

| I want to | Seam |
|---|---|
| put tagged blocks in the system prompt | `gotato.ContextBlock` |
| replace the search of `search_tools` | `gotato.ToolSearcher` |
| write the Recap of merged Segments myself | `modelctx.MergeRecapWriter` |
| let wrappers of a model keep its window and cache hints | `ContextWindower`, `CacheRetainer`, `RoundReasoner`, `Unwrapper` |
| rewrite the Transcript before a model call | `gotato.TurnPreparer` |
| keep a Session's Events in my own log | `gotato.Journal`, `gotato.EventSource` |

## The chooser

Each row says what you want, the seam that does it, and where it lives. "Tier" tells how often a service needs it: **common**, **sometimes**, or **rare**.

| I want to ... | Use this seam | Where it is | Tier |
|---|---|---|---|
| call a model of my own, or wrap one (retries, a header, a counter) | `gotato.Model` | `gotato.WithModel`; `service.Agent.Model` | common |
| give the agent a function to call | `gotato.Tool`, built by `gotato.NewFuncTool` | `gotato.WithTools`; `service.Agent.Tools` | common |
| keep Sessions in Postgres, Redis or another database | `session.Store` | `service.Config.Store`; prove it with `session/storetest` | common |
| change what the model sees on each Turn (window, selection, a layout of my own) | `gotato.ContextBuilder` | `gotato.WithContextBuilder`; `service.Agent.ContextBuilder` | common |
| refuse a tool call, or change its result | `gotato.PreToolUse`, `gotato.PostToolUse` | `gotato.WithExtension` | common |
| stop a Run after N Turns or N calls | `gotato.TurnStopper` | `gotato.WithExtension` | common |
| limit how many Runs and Sessions one pod takes | `service.Admission` | `service.Config.Admission` | common |
| bring all the tools of an MCP server, or any named group | `gotato.ToolSet` | `gotato.WithActiveToolSet`, `WithToolSet`, `WithFoldedToolSet`; `adapter/mcp` builds one | sometimes |
| supply tools that change between Turns (by user, by feature flag) | `gotato.ToolSource` | `gotato.WithToolSource`; `service.Agent.ToolSources` | sometimes |
| have the Agent's own model write the Recap of a Segment | `modelctx.RecapWriter` (`ModelRecap`, `TruncateRecap`) | `modelctx.CompactPolicy.Recap`, in `service.Agent.Compact` | sometimes |
| drop or rewrite Messages after the builder ran | `gotato.ContextTransformer` | `gotato.WithExtension` | sometimes |
| change the shape of Messages for one provider | `gotato.MessageConverter` | `gotato.WithExtension` | sometimes |
| add tagged blocks to the system prompt | `gotato.ContextBlock` | `ModelContext.System`, set by a `ContextBuilder` | rare |
| search tools my own way (embeddings, a service) | `gotato.ToolSearcher` | `gotato.WithToolSearcher` | rare |
| write the Recap of merged Segments myself | `modelctx.MergeRecapWriter` | the same `CompactPolicy.Recap`; a writer with `WriteMergeRecap` is used for merges | rare |
| report the window, the cache time or the reasoning rule of a model I wrap | `modelctx.ContextWindower`, `CacheRetainer`, `RoundReasoner`, and `Unwrapper` on the wrapper | found by `modelctx.WindowOf`, `RetentionOf`, `DropsReasoning` | rare |
| rewrite the Transcript before a model call, such as to compact | `gotato.TurnPreparer` | `gotato.WithExtension` | rare |
| write a Session's Events to a store of my own | `gotato.Journal` (with `EventSource` and `SpillKeeper` beside it) | a `Transcript` given to `gotato.WithTranscript`; `session.Store` is the usual way | rare |
| let a subscriber catch up from my own log | `gotato.EventSource` | `gotato.NewFeed(source)`, `gotato.WithFeed` | rare |

## Common seams

### Model

A `gotato.Model` has one method, `Stream`, that starts a call and returns its events. `gateway` implements it for three wire APIs; you implement it for anything else. A wrapper that forwards to another model should also implement `Unwrap() gotato.Model`, or the window and cache hints of the model it wraps are hidden (see the rare seams below).

### Tool

A tool is a `gotato.Tool`. `NewFuncTool` builds one from a Go function, and its input type becomes the schema. Give a tool its Effect so the Agent's Effect cap can hide it. [Getting started](getting-started.md) shows the first one.

### Store

A Store is where Records live. The memory and file stores ship with `session`. For a database of your own, implement `session.Store` and run `storetest.Run` on it; the suite names the rules the Runner relies on.

### ContextBuilder

A builder turns the Transcript into the Context of one Turn. The default, `gotato.FullHistoryContext()`, sends everything, which keeps the request prefix identical from Turn to Turn so the provider's prompt cache hits. Write your own only when you can say what the model should see instead. A function is enough:

```go
// lastTen shows the model only the last ten Messages, and a block for the date.
func lastTen() gotato.ContextBuilder {
	return gotato.ContextBuilderFunc(func(_ context.Context, s gotato.ContextSnapshot) (gotato.ModelContext, error) {
		msgs := s.Messages
		if len(msgs) > 10 {
			msgs = msgs[len(msgs)-10:]
		}
		return gotato.ModelContext{
			SystemInstructions: s.SystemInstructions,
			System:             []gotato.ContextBlock{{Tag: "today", Text: "2026-10-28"}},
			Messages:           msgs,
		}, nil
	})
}
```

Cutting history like this breaks the prompt cache. For a long Session, prefer Compaction, which closes a stretch into a Segment the model can read back.

### Extension stages

An Extension is a value with one or more stage methods, installed with `gotato.WithExtension`. One value may implement several stages. The stages, in the order a Turn meets them:

| Stage | Method | Runs |
|---|---|---|
| `TurnPreparer` | `PrepareTurn` | before each model call; the one safe point to rewrite the Transcript |
| `ContextTransformer` | `Transform` | on the builder's Messages |
| `MessageConverter` | `Convert` | after the transformers, to fit one provider |
| `PreToolUse` | `Before` | before a tool runs; can block it |
| `PostToolUse` | `After` | after a tool, in reverse order; can change the result |
| `TurnStopper` | `Stop` | after a Turn; can end the Run |

Two small ones, and how they are installed:

```go
// blocker is an Extension with one stage, PreToolUse. It lets every call through.
type blocker struct{}

func (blocker) Before(_ context.Context, use gotato.ToolUse) (gotato.PreToolDecision, error) {
	return gotato.PreToolDecision{}, nil // return {Block: true, Reason: "..."} to refuse
}

// stopAfter is an Extension with one stage, TurnStopper. It ends the Run after N Turns.
type stopAfter struct{ turns int }

func (s stopAfter) Stop(_ context.Context, t gotato.TurnSnapshot) (gotato.StopDecision, error) {
	return gotato.StopDecision{Stop: int(t.Turn) >= s.turns, Reason: "turn cap"}, nil
}

s, err := gotato.NewSession(ctx,
	gotato.WithModel(model),
	gotato.WithExtension(blocker{}),
	gotato.WithExtension(stopAfter{turns: 8}),
)
```

`WithExtension` fails when a value has a method named like a stage (`Before`, `Stop`, ...) with the wrong signature, so a typo does not pass silently. A failing extension ends the Run, unless it also implements `Advisory() bool` and returns true: then the failure is recorded as an `extension_failed` Event.

Through the Runner, pass extensions in `service.Agent.Options`, as `gotato.WithExtension(...)`.

### Admission

`service.Admission` bounds the work of one pod: `MaxActiveRuns`, `MaxActiveSessions` and `MaxWarmSessions`, and a `Queue` policy for a Session that is busy (`reject` or `wait`). A refused Run is marked not run, so the load balancer can send it to another pod.

## Sometimes

### ToolSet and ToolSource

A `ToolSet` is a named group with a description and one source; the model loads its tools one by one with `search_tools`. A `ToolSource` is lower level: one method, `Tools(ctx)`, called at the start of each Turn. Reach for a ToolSource when the set of tools depends on who is asking. Through the Runner, set `service.Agent.ToolSources`:

```go
runner, err := service.New(service.Config{
	Store: session.NewMemoryStore(),
	Agents: []service.Agent{{
		Name:        "default",
		Model:       model,
		ToolSources: []gotato.ToolSource{source{}}, // source has Tools(ctx) ([]gotato.Tool, error)
	}},
})
```

A ToolSet is attached with `Agent.Options`, not `ToolSources`.

### RecapWriter

A Recap is the end of a Segment, written when a Session compacts. With no writer, a Segment has none. `modelctx.ModelRecap` has the Agent's own model write it on the cached prefix; `TruncateRecap` is the deterministic one for tests. Set it in the Agent's compaction policy:

```go
Compact: modelctx.CompactPolicy{Recap: modelctx.ModelRecap{}}, // a Runner fills in the Agent's model
```

### ContextTransformer and MessageConverter

Both are Extension stages that run after the builder. A transformer selects, adds or prunes Messages; a converter adapts their shape for one provider. They change what the model sees on this Turn only, never the Transcript.

## Rare

### ContextBlock

A tagged piece of the system prompt, rendered as `<tag attr="value">text</tag>`. A `ContextBuilder` fills `ModelContext.System` with them. The tag marks a boundary or where the text came from.

### ToolSearcher

Replaces the built-in BM25 search of `search_tools`. Gotato hands it a query and the specs of the tools that may load; it returns IDs, the first three load and the next five are listed. Install with `gotato.WithToolSearcher`.

### MergeRecapWriter

When the oldest Segments pass their budget they are merged into one. A `RecapWriter` that also has `WriteMergeRecap` writes the Recap of the merge (`ModelRecap` does). Any other writer leaves the merged Segment with a digest built by code.

### Model capability interfaces

Optional methods a model can have, so compaction knows its limits. Gotato keeps no table of windows.

| Interface | Method | Used for |
|---|---|---|
| `modelctx.ContextWindower` | `ContextWindowTokens() int` | the Compaction ceiling |
| `modelctx.CacheRetainer` | `CacheRetention() time.Duration` | when a warm Session compacts, before the cache expires |
| `modelctx.RoundReasoner` | `RoundReasoningOnly() bool` | leaving reasoning out of the Messages compaction keeps |
| `modelctx.Unwrapper` | `Unwrap() gotato.Model` | a wrapper's way to let the others be found |

`gateway.Client` implements the first three. A wrapper of your own that does not implement `Unwrap` hides them, and the window reads as unknown.

### TurnPreparer

An Extension stage before every model call, with no tool open. It may rewrite the Transcript and say so. The Runner's own compaction works this way.

### Journal, EventSource and SpillKeeper

These are optional capabilities of a `Transcript`, which `gotato.WithTranscript` takes. A `Journal` writes the Events of a step as numbered Entries; an `EventSource` lets a Subscription that falls behind read them again; a `SpillKeeper` says large Tool Results may be spilled. `session.Record` and `session.Writer` implement all of them, so you meet them only if you write a Transcript that is not backed by `session.Store`. For your own database, write a Store instead.

## What is not a seam

Gotato has no hook for roles, agent hierarchy, workflow or UI. Those belong to your application, which keeps them in `Session.Metadata` ([GLOSSARY.md](../GLOSSARY.md)).

## Next

- Test a seam with scripted fakes: `testkit` (`FakeModel`, `FakeTool`).
- [Embed Gotato in a service](embed-in-a-service.md) for the Runner and the Store.
- [Which packages do I import?](packages.md) for what each package gives you.
