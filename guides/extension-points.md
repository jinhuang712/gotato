# Extension points

By the end of this page you know which seam of Gotato to reach for when the default is not what you want, and which seams you will most likely never need. Each one is a Go interface or a function type: you write a small value, and pass it in an option or a field.

## TL;DR

**Common** (most services use one or two of these)

| I want to | Seam |
|---|---|
| call my own model or provider, and say what it is (its window, cache retention, reasoning rule) | `gotato.Model` (`Stream` and `Config`) |
| give the agent a capability | `gotato.Tool` (`NewFuncTool`) |
| save Sessions in my own database | `session.Records` and `session.Liveness` |
| change what the model sees | `gotato.ContextBuilder` |
| block a tool call, rewrite its result, or cap the Turns | Extension stages `PreToolUse`, `PostToolUse`, `TurnStopper` |
| bound the memory of one pod | `service.Bounds` |
| decide whether a Run may start, such as a limit for each tenant | `service.Admitter` |
| register the Agents a service runs, and the jobs that run again | `profile.Registry`: `RegisterAgentProfileInline`, `RegisterTaskSpecInline` |

**Sometimes**

| I want to | Seam |
|---|---|
| expose a group of tools, such as an MCP server | `gotato.ToolSet` |
| keep Agent and job definitions in my own database | `session.Definitions`, with `RegisterAgentProfileWithStore`, `RegisterTaskSpecWithStore` and a `profile.Catalog` |
| supply tools that change between Turns | `gotato.ToolSource`, `profile.AgentProfile.ToolSources` |
| write Recaps with a model | `modelctx.RecapWriter` (`ModelRecap`) |
| choose when a Session compacts, or where the kept tail starts | `modelctx.Trigger`, `modelctx.Cutter`, in a `modelctx.Compactor` |
| prune or reshape the Messages after the builder | `gotato.ContextTransformer`, `gotato.MessageConverter` |
| speak a wire API Gotato does not ship | `gateway.WireAPI` |
| set how often a model call is sent again, or turn it off | `gateway.Retry`, `gateway.WithRetry` |
| change what a failed model call means | `gateway.Classifier`, `gateway.WithClassifier` |

**Rare** (a specific need, or a store adapter's concern)

| I want to | Seam |
|---|---|
| put tagged blocks in the system prompt | `gotato.ContextBlock` |
| replace the search of `search_tools` | `gotato.ToolSearcher` |
| write the Recap of merged Segments myself | `modelctx.MergeRecapWriter` |
| change how a Segment reads, or how many Segments merge | `modelctx.Builder`, `modelctx.Picker` |
| compact a Transcript that is not a `session.Record` | `modelctx.SegmentStore` |
| read spilled results and Segments from my own store, or change the page size | `gotato.ReadBack`, `modelctx.Originals`; in a service, `profile.AgentProfile.ReadBack` |
| rewrite the Transcript before a model call | `gotato.TurnPreparer` |
| keep a Session's Events in my own log | `gotato.Journal`, `gotato.EventSource` |

## The chooser

Each row says what you want, the seam that does it, and where it lives. "Tier" tells how often a service needs it: **common**, **sometimes**, or **rare**.

| I want to ... | Use this seam | Where it is | Tier |
|---|---|---|---|
| call a model of my own, or wrap one (retries, a header, a counter) | `gotato.Model` | `gotato.WithModel`; `profile.AgentProfile.Model` | common |
| give the agent a function to call | `gotato.Tool`, built by `gotato.NewFuncTool` | `gotato.WithTools`; `profile.AgentProfile.Tools` | common |
| keep Sessions in Postgres, Redis or another database | `session.Records` and `session.Liveness`, and optionally `session.Lister` | `service.Config.Store` (`session.Store{Records:, Liveness:}`); prove it with `session/storetest` | common |
| change what the model sees on each Turn (window, selection, a layout of my own) | `gotato.ContextBuilder` | `gotato.WithContextBuilder`; `profile.AgentProfile.ContextBuilder` | common |
| refuse a tool call, or change its result | `gotato.PreToolUse`, `gotato.PostToolUse` | `gotato.WithExtension` | common |
| stop a Run after N Turns or N calls | `gotato.TurnStopper` | `gotato.WithExtension` | common |
| limit how many Sessions one pod keeps in memory, and what a busy Session does | `service.Bounds` | `service.Config.Bounds` | common |
| decide whether a Run may start: a bound on the Runs in flight, a limit for each tenant or caller | `service.Admitter` (`Admit(ctx, Admission, Load) (release, error)`) | `service.Config.Admitter`; the default is `service.Limits{MaxActiveRuns}` | common |
| bring all the tools of an MCP server, or any named group | `gotato.ToolSet` | `gotato.WithActiveToolSet`, `WithToolSet`, `WithFoldedToolSet`, or `profile.AgentProfile.ToolSets` with a `profile.ToolSet{Set, Mode}`; `adapter/mcp` builds one | sometimes |
| supply tools that change between Turns (by user, by feature flag) | `gotato.ToolSource` | `gotato.WithToolSource`; `profile.AgentProfile.ToolSources` | sometimes |
| have the Agent's own model write the Recap of a Segment | `modelctx.RecapWriter` (`ModelRecap`, `TruncateRecap`) | `modelctx.Compactor.Recap`, in `profile.AgentProfile.Compact` | sometimes |
| drop or rewrite Messages after the builder ran | `gotato.ContextTransformer` | `gotato.WithExtension` | sometimes |
| change the shape of Messages for one provider | `gotato.MessageConverter` | `gotato.WithExtension` | sometimes |
| add tagged blocks to the system prompt | `gotato.ContextBlock` | `ModelContext.System`, set by a `ContextBuilder` | rare |
| search tools my own way (embeddings, a service) | `gotato.ToolSearcher` | `gotato.WithToolSearcher` | rare |
| decide when a Session compacts | `modelctx.Trigger` | `Compactor.Trigger`; the default is `CeilingTrigger` | sometimes |
| decide where the kept tail starts | `modelctx.Cutter` | `Compactor.Cut`; the default is `TailBudget` | sometimes |
| change how a closed stretch reads as a Segment | `modelctx.Builder` | `Compactor.Builder`; the default is `CodeSegments` | rare |
| decide how many of the oldest Segments merge | `modelctx.Picker` | `Compactor.Merge`; the default is `OldestToHalf` | rare |
| write the Recap of merged Segments myself | `modelctx.MergeRecapWriter` | the same `Compactor.Recap`; a writer with `WriteMergeRecap` is used for merges | rare |
| compact a Transcript of my own | `modelctx.SegmentStore` | `modelctx.Compact(ctx, store, compactor, cfg)`, `modelctx.AutoCompact(store, compactor)` | rare |
| read spilled results and Segments back from my own store, or change the page size | `gotato.ReadBack`, with `modelctx.Originals` | `gotato.WithReadBack`; the default is `modelctx.ReadBack{Originals: modelctx.RecordOriginals(record, log)}` | rare |
| report the window, the cache time or the reasoning rule of my model | `Config() gotato.ModelConfig` on the `gotato.Model` (a wrapper that embeds the Model forwards it) | read by the Runner and compaction from `Model.Config().Facts` | common |
| speak a wire API Gotato does not ship | `gateway.WireAPI` (`Encode(Call) (*Request, error)` and a `Decoder`) | the first argument of `gateway.New` | sometimes |
| authenticate with more than a static key (a signed request, a token that expires) | an `http.RoundTripper` | `gateway.Connection.HTTPClient` | sometimes |
| change how often a model call is sent again, or turn it off | `gateway.Retry` | `gateway.WithRetry`; `gateway.Retry{}` is off | sometimes |
| change what a failed model call means (retryable, too long) | `gateway.Classifier` | `gateway.WithClassifier`; wrap `gateway.DefaultClassifier` to add to it | sometimes |
| rewrite the Transcript before a model call, such as to compact | `gotato.TurnPreparer` | `gotato.WithExtension` | rare |
| write a Session's Events to a store of my own | `gotato.Journal` (declared in `Transcript.Capabilities()` with `EventSource`, `Spill` and `LoadedToolSource`) | a `Transcript` given to `gotato.WithTranscript`; `session.Records` is the usual way | rare |
| let a subscriber catch up from my own log | `gotato.EventSource` | `Events` in the `Capabilities` of a `Transcript` given to `gotato.WithTranscript` | rare |

## Common seams

### Model

A `gotato.Model` has two methods: `Stream` starts a call and returns its events, and `Config` returns a `gotato.ModelConfig`, what the model is. `gateway` implements it for three wire APIs; you implement it for anything else. A Model that states nothing returns the zero `ModelConfig`: an unknown window, no cache retention and no default sampling. A wrapper that embeds the model it wraps forwards `Config` for free; one that holds it in a field returns `Inner.Config()`, or the window and cache facts are lost.

### Tool

A tool is a `gotato.Tool`. `NewFuncTool` builds one from a Go function, and its input type becomes the schema. Give a tool its Effect so the Agent's Effect cap can hide it. [Getting started](getting-started.md) shows the first one.

### Store

A Store is where Records live. The memory and file stores ship with `session`. A `session.Store` is two seams side by side: `Records` (`Create`, `Append`, `Load`, `Log`, `Delete`) and `Liveness` (`Beat`, `Instance`), so they may live in two databases. A `Records` may also be a `session.Lister`, which `Runner.Sessions` and `GET /v1/sessions` need and the runtime does not; without one they answer `not_supported`. For a database of your own, implement them and run `storetest.RunRecords`, `RunLiveness` and, if you list, `RunLister` on it; the suite names the rules the Runner relies on. Definitions are a separate contract, below.

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

Through the Runner, pass extensions in `profile.AgentProfile.Extensions`; the Runner installs each with `gotato.WithExtension`, after its own.

### Needs a person

Gotato has no built-in approval and no waiting state in the Loop ([decision 005](../plan/decisions/005-no-approval-system.md)). Two ways to ask a person before a step:

**Ask inside `PreToolUse`.** `Before` runs for every call of a Turn, one after another, before any executor of that Turn starts. It gets a `ToolUse`: `RunID`, `Turn`, `CallID`, `ToolID`, `Arguments`, `SourceIndex` and `Effect`. It has no Session id.

- A wait inside `Before` ends with the Run's context, or at `RunTimeout`. `ToolCallDeadline` does not bound it, because no tool call has started yet.
- A `Before` that returns an error ends the Run with `CodeExtensionFailure`. To fail closed without ending the Run (a timeout, a "no"), return `PreToolDecision{Block: true, Reason: "..."}`: the model sees a blocked result and goes on.
- A blocked call has a `tool_ended` Event and no `tool_started`. This is on purpose: `tool_started` says a tool began, and recovery relies on that.

**Hand the step to a person from a tool.** A tool that opens a ticket, as decision 005 says, and returns at once. Use this when the wait is longer than a Run should live.

### Admission

Two things decide whether a Run starts on a pod, in this order ([decision 048](../plan/decisions/048-the-runner-and-the-session-are-built-from-parts-and-host-plumbing-is-internal.md)).

`service.Bounds` guards the pod itself: `MaxActiveSessions` (the Sessions it keeps, active and warm, with the eviction of the longest-idle warm copy before a Run is refused as `overloaded`), `MaxWarmSessions`, and a `Queue` policy for a Session that is busy (`reject` or `wait`). A draining pod refuses first. These are not a policy: they run whatever the `Admitter` says.

`service.Admitter` is the policy of the service, and the one place a Runner asks it. `Admit(ctx, service.Admission, service.Load) (release func(), err error)` is given the Run (`Admission{SessionID, Agent, Task}`: the AgentProfile and, for a Run of a TaskSpec, the TaskSpec's name), what the pod holds (`Load{ActiveRuns, ActiveSessions, WarmSessions}`), and the caller's `ctx`, which carries who asks. The default is `service.Limits{MaxActiveRuns: n}`: refuse past n Runs in flight, and no limit at zero. A limit for each tenant is a few lines:

```go
type perTenant struct {
	mu    sync.Mutex
	limit map[string]int
	held  map[string]int
}

func (p *perTenant) Admit(ctx context.Context, _ service.Admission, _ service.Load) (func(), error) {
	tenant, _ := ctx.Value(tenantKey{}).(string)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.held[tenant] >= p.limit[tenant] {
		return nil, gotato.ErrorOf(gotato.CodeLimitExceeded, "tenant is at its limit of runs")
	}
	p.held[tenant]++
	return func() { p.mu.Lock(); p.held[tenant]--; p.mu.Unlock() }, nil // the Runner calls it once when the Run ends
}
```

A refusal is the error `Admit` returns, marked not run, so the load balancer can send the Run to another pod; an error that is not typed is answered as `limit_exceeded` (HTTP 429). `Admit` runs with the Runner's table of Runs locked, so it must return quickly and must not call the Runner; the `Load` it sees and the Run it admits are one step. A Run that follows another with the messages the first left is part of the Run already admitted and is not asked again.

### Knowing who asked

A Tool cannot learn who asked from the request: `ToolUse` has no field for it, and nothing in `RunRequest` reaches it. `Metadata` applies when a Session is created, `Update` writes Session metadata, and `RequestID` goes only into `run_started`. What reaches `Before` and `Execute` is the caller's `ctx`. Put the asker into it before `Runner.Run`:

```go
type askerKey struct{}

ctx = context.WithValue(ctx, askerKey{}, "ann")
runner.Run(ctx, service.RunRequest{SessionID: id, Prompt: text})

// in a PreToolUse extension, or in a Tool's Execute:
who, _ := ctx.Value(askerKey{}).(string)
```

- **It lasts one Run.** The value is the one of the call that started the Run.
- **Askers of one Session do not mix.** A Session runs one Run at a time, so each Run sees the value of its own caller.
- **A Run that follows another** (it starts from messages the first Run left queued) runs under the first Run's context without its cancel, so it sees the first caller's value, not the sender's of those messages.

`TestAValueInTheCallersCtxReachesBeforeAndExecute` in [service/service_test.go](../service/service_test.go) pins this.

## Sometimes

### ToolSet and ToolSource

A `ToolSet` is a named group with a description and one source; the model loads its tools one by one with `search_tools`. A `ToolSource` is lower level: one method, `Tools(ctx)`, called at the start of each Turn. Reach for a ToolSource when the set of tools depends on who is asking. Through the Runner, set `profile.AgentProfile.ToolSources`:

```go
memory := session.NewMemoryStore()
profiles := profile.NewRegistry(nil)
err := profiles.RegisterAgentProfileInline(profile.AgentProfile{
	Name:        "default",
	Model:       model,
	ToolSources: []gotato.ToolSource{source{}}, // source has Tools(ctx) ([]gotato.Tool, error)
})
runner, err := service.New(service.Config{
	Store:        session.Store{Records: memory, Liveness: memory},
	Profiles:     profiles,
	DefaultAgent: "default",
})
```

A ToolSet is attached with `AgentProfile.ToolSets` (a `profile.ToolSet{Set, Mode}`), not `ToolSources`.

### Agent profiles and task specs

A service registers the Agents it runs in a `profile.Registry`, which it gives to `service.Config.Profiles` ([decision 044](../plan/decisions/044-an-agent-profile-and-a-task-spec-are-registered-inline-or-from-a-store.md)). There are four registrations, two for each kind of definition: inline, as a Go value, or from a store, by name. Each name is registered once, and a later registration replaces the earlier.

| Registration | What it registers |
|---|---|
| `RegisterAgentProfileInline(p)` | a `profile.AgentProfile` as a value, under its `Name` |
| `RegisterTaskSpecInline(t)` | a `profile.TaskSpec`, a job that runs again, as a value |
| `RegisterAgentProfileWithStore(name, defs)` | the name of an AgentProfile whose document is read from a `session.Definitions` at each lookup |
| `RegisterTaskSpecWithStore(name, defs)` | the same for a TaskSpec |

```go
profiles := profile.NewRegistry(nil)
err := profiles.RegisterAgentProfileInline(profile.AgentProfile{Name: "support", Model: model, Tools: tools})
err = profiles.RegisterTaskSpecInline(profile.TaskSpec{Name: "morning-report", Profile: "support", Background: "Report on yesterday's tickets."})
runner, err := service.New(service.Config{Store: store, Profiles: profiles, DefaultAgent: "support"})
out, err := runner.Run(ctx, service.RunRequest{Task: "morning-report", Prompt: "Go."})
```

A TaskSpec only narrows its profile (a subset of the Tools, a lower Effect cap, a shorter timeout, a lower `MaxTokens`); one that widens is refused with `profile.ErrWidens`. A Session looks its AgentProfile up by name each time it is built, so a replaced profile reaches new Sessions and never a Run in flight.

### Definitions and the Catalog

`session.Definitions` is a store of documents by kind and name, which a service implements over its own database so that an AgentProfile or a TaskSpec lives there and not in code. It has two methods, `SaveDefinition` and `LoadDefinition` (`ErrNotFound` when none), keeps bytes, and is not a part of `session.Store`. The stored document is data only: a model name, Tool and ToolSet names, numbers and text. The `profile.Catalog`, built with `profile.NewCatalog(models, tools, toolSets)`, holds what those names can point to, so a stored definition selects what your code offers and never adds to it. A row whose name is not registered is not a definition. `session.NewMemoryDefinitions` and `session.NewFileDefinitions(dir)` ship, and `storetest.RunDefinitions` proves your own ([write a store](write-a-store.md)).

```go
catalog, err := profile.NewCatalog(map[string]gotato.Model{"main": model}, tools, toolSets)
profiles := profile.NewRegistry(catalog)
err = profiles.RegisterAgentProfileWithStore("support", defs) // defs is your session.Definitions
```

### RecapWriter

A Recap is the end of a Segment, written when a Session compacts. With no writer, a Segment has none. `modelctx.ModelRecap` has the Agent's own model write it on the cached prefix; `TruncateRecap` is the deterministic one for tests. Set it on the Agent's `Compactor`:

```go
Compact: modelctx.Compactor{Recap: modelctx.ModelRecap{}}, // a Runner fills in the Agent's model
```

A writer of your own has four methods: `Name`, `WriteRecap`, `WithAgentModel(gotato.Model) RecapWriter` (the Runner calls it to hand over the Agent's model; a writer that asks no model returns itself, and one that wraps a `ModelRecap` passes the model on) and `Validate() error` (a misconfiguration is an error at first use, not a Segment that quietly has none). Embedding the writer you wrap gives you the methods you do not change:

```go
// loggedRecap logs each Recap it is asked for and leaves the writing to the writer it wraps.
type loggedRecap struct{ modelctx.RecapWriter }

func (l loggedRecap) WriteRecap(ctx context.Context, req modelctx.RecapRequest) (gotato.Message, error) {
	log.Printf("recap for segment %d", req.Number)
	return l.RecapWriter.WriteRecap(ctx, req)
}

// The Runner calls this with the Agent's model, so the wrapped writer must get it.
func (l loggedRecap) WithAgentModel(m gotato.Model) modelctx.RecapWriter {
	return loggedRecap{l.RecapWriter.WithAgentModel(m)}
}

// in profile.AgentProfile: Compact: modelctx.Compactor{Recap: loggedRecap{modelctx.ModelRecap{}}},
```

Embedding the interface hides `WriteMergeRecap`, so merges of this writer get the digest built by code; add the method to write them too. The frame still appends your Recap escaped, so it cannot end its tag early, and a writer that fails leaves the Segment without a Recap, not without a compaction.

### Compactor, Trigger and Cutter

Compaction is a frame, `modelctx.Compactor`, around parts. You replace a part and keep the rest; a nil part is its default, so `modelctx.Compactor{}` is how compaction behaved before the parts were named. `CompactPolicy` (embedded) holds only numbers: `CeilingTokens`, `WindowTokens`, `DropReasoning`, `SegmentBudgetTokens` and `Location`. [How the frame works](../specs/compaction.md).

```go
Compact: modelctx.Compactor{
	CompactPolicy: modelctx.CompactPolicy{WindowTokens: 200_000},
	Trigger:       everyTwentyTurns{},
	Cut:           keepLastTwenty{},
},
```

A `Trigger` says when a compaction is due, and why. It is asked once before each model call; `gotato.CompactionWhyPastBudget` means only the oldest Segments are due to merge, any other reason closes a Segment. Wrap `modelctx.CeilingTrigger` to add a rule and keep the default ones (overflow, the ceiling, Segments past budget):

```go
// everyTwentyTurns adds a rule to the default Trigger: also compact on every 20th Turn of a Run.
type everyTwentyTurns struct{ modelctx.CeilingTrigger }

func (t everyTwentyTurns) Due(ctx context.Context, s modelctx.TurnState) (gotato.CompactionWhy, bool) {
	if why, due := t.CeilingTrigger.Due(ctx, s); due {
		return why, true
	}
	if s.Turn%20 == 0 {
		return gotato.CompactionWhyOnDemand, true
	}
	return "", false
}
```

A `Cutter` says where the kept tail starts, as an index into the Messages since the last Checkpoint; the Messages before it close into the Segment, and 0 closes nothing:

```go
// keepLastTwenty keeps the last 20 Messages word for word.
type keepLastTwenty struct{}

func (keepLastTwenty) Cut(stretch []gotato.Message, _ modelctx.CutInfo) int {
	return max(0, len(stretch)-20)
}
```

What the frame still guarantees: a Turn compacts at most once, whatever the Trigger says, and a Run still past the ceiling after its compaction ends `limit_exceeded`; a Trigger that panics, or says due with no reason, drops that Turn's compaction and publishes an `extension_failed` Event. The cut must be inside the stretch (else the compaction fails); the frame moves it forward to a Turn boundary so a tool call keeps its result, and back so a Run's last Turn is kept, and the tail is the Messages as committed, less reasoning and usage. A Cutter cannot drop or move a Segment.

For one call, `modelctx.CompactConfig` changes the numbers, `KeepMessages` (the Cutter becomes `TailBudget{KeepMessages}`) and the Recap writer; it does not need a part of its own.

### Builder and Picker

A `Builder` renders the closed Messages as a Segment. Its settings live with it: `CodeSegments{SegmentSettings{MessageTokens, ToolLines, ArgChars}}`, so most teams change a number and write no code:

```go
Builder: modelctx.CodeSegments{SegmentSettings: modelctx.SegmentSettings{MessageTokens: 500}},
```

A Builder of your own wraps the default or writes the Messages itself:

```go
// countedSegments builds the default Segment and counts it.
type countedSegments struct {
	modelctx.CodeSegments
	built *atomic.Int64
}

func (b countedSegments) Build(closed []gotato.Message, info modelctx.SegmentInfo) (gotato.Segment, error) {
	b.built.Add(1)
	return b.CodeSegments.Build(closed, info)
}
```

What the frame still guarantees: it stamps `Number`, `From`, `To`, `Source`, `Merged` and the Message IDs `segment-<n>-<i>`, appends the Recap escaped, and refuses what would break decisions 034 and 043: a Message that is not a plain user or assistant Message, a tagged Part (one of the four tags) whose text holds a tag or is not of the form `<tag ...>...</tag>`, and a ref that names nothing the Segment closed. The refs it checks are the two shapes `CodeSegments` writes: `[result:<call_id> ...]` ending a tool call line in `<tool_calls>`, and a line `ref msg:<message_id>` in `<segment>` (build them with package `ref`). A Builder that errs or panics drops the compaction.

A `Picker` says how many of the oldest Segments a merge replaces. It is asked only when the Segments pass their budget and there are at least two:

```go
// allButTwo folds every Segment but the newest two, once there are at least two to fold.
type allButTwo struct{}

func (allButTwo) Pick(segments []gotato.Segment, _ int) int {
	if n := len(segments) - 2; n >= 2 {
		return n
	}
	return 0 // merges nothing
}
```

The frame replaces two or more of the first Segments, never a later one. An answer of 1, or of more than there are, fails the merge with the reason `pick_failed` and nothing is merged; 0 merges nothing.

### SegmentStore

A `modelctx.SegmentStore` is what a compaction reads and writes. `*session.Record` is one, so a Session over a `session.Store` compacts with no work. Implement it for a Transcript of your own (see [Transcript and its Capabilities](#transcript-and-its-capabilities)) that keeps the originals a Segment refers to:

```go
type myStore struct{ /* your Transcript's state */ }

func (s *myStore) ID() string                                  { /* the Session's ID */ }
func (s *myStore) Tail() []gotato.Message                      { /* what came since the last Checkpoint, and what it kept */ }
func (s *myStore) Segments() []gotato.Segment                  { /* the Segments the Context starts with, oldest first */ }
func (s *myStore) NextSegment() int                            { /* the number the next Segment gets */ }
func (s *myStore) Position(id gotato.MessageID) (gotato.MessagePosition, bool) { /* where and when a Message was committed */ }
func (s *myStore) LoadedTools() []string                       { /* qualified IDs of the loaded Tools */ }
func (s *myStore) Emit(e gotato.Event)                         { /* publish compaction_started and the rest */ }

// One batch: write the Segment and the Checkpoint whose Context is every Segment held, then segment, then tail.
// The Tools it keeps loaded are compaction.LoadedTools. When it fails, nothing changed.
func (s *myStore) CloseSegment(why gotato.CompactionWhy, seg gotato.Segment, tail []gotato.Message, c *gotato.Compaction) (gotato.Segment, error) { /* ... */ }

// One batch: write the merged Segment and the Checkpoint whose Context is merged, the Segments after the ones
// replaced (replaces are the numbers of the first Segments), then tail.
func (s *myStore) MergeSegments(why gotato.CompactionWhy, merged gotato.Segment, replaces []int, tail []gotato.Message, c *gotato.Compaction) (gotato.Segment, error) { /* ... */ }

// then: modelctx.AutoCompact(store, compactor) is the TurnPreparer; modelctx.Compact(ctx, store, compactor, cfg) compacts once.
```

What the frame still guarantees: only it calls the two writes, one batch per call. `CloseSegment` lists every Segment of the Context itself (a caller cannot drop or move one) and sets the loaded Tools from `Compaction.LoadedTools` in the same step. A panic in your store is a dropped compaction, not a dead Run, and an error you return keeps its own code (a conflict stays a conflict).

### ReadBack and Originals

A `gotato.ReadBack` is a `ToolSource` that also says `PageBytes() int`. It gives a Session its tools for reading back what left the Context, and the one page size that the spill threshold, the pages of a preview and both read tools share. Install it with `gotato.WithReadBack`; a Session spills a large Tool Result only with one installed (and a Transcript that keeps Entries). `modelctx.ReadBack{Originals, PageSize, Location}` is the default and offers `read_result` and, once the Context holds a Segment, `read_segment`. A service installs it over its store, so most teams write nothing; one Agent that needs another sets `profile.AgentProfile.ReadBack`, and nil keeps the default. To read from your own store, supply `modelctx.Originals`, and set the page size if 32 KiB (`modelctx.DefaultPageBytes`) is not right:

```go
type myOriginals struct{ db *sql.DB }

// Ref returns a Message by its ref: result:<call_id> or msg:<message_id>.
func (o myOriginals) Ref(ctx context.Context, r ref.Ref) (gotato.Message, error) {
	// look it up; return modelctx.ErrNoOriginal when nothing has it, modelctx.ErrRemoved when it was pruned
	return gotato.Message{}, modelctx.ErrNoOriginal
}

// Segment returns the Messages Segment n closed, and whether it returned all of them.
func (o myOriginals) Segment(ctx context.Context, n int) ([]gotato.Message, bool, error) {
	return nil, false, modelctx.ErrNoOriginal
}

gotato.WithReadBack(modelctx.ReadBack{Originals: myOriginals{db}, PageSize: 16 << 10})
```

Add `Segments() []gotato.Segment` to your `Originals`, as `modelctx.RecordOriginals` has, and `read_segment` is offered only once the Context holds a Segment; without it, it is always offered. An `ErrNoOriginal` may carry a `Hint() string`, text shown to the model after the words for a ref that names nothing (for example the refs the store holds). Any other error is a store failure and its text is never shown to the model.

What the ReadBack still guarantees: the IDs `read_result` and `read_segment` (`ref.ReadResult`, `ref.ReadSegment`), the paging, the escaping of the four tags, the `read` Effect and the wording are fixed, since written Segments name them; only where the originals are, and how big a page is, are yours. `NewSession` asks `PageBytes` once and refuses a value below 1, or a panic, with `invalid_argument`; a Tool ID it shares with another source fails the Session like any other.

### ContextTransformer and MessageConverter

Both are Extension stages that run after the builder. A transformer selects, adds or prunes Messages; a converter adapts their shape for one provider. They change what the model sees on this Turn only, never the Transcript.

### WireAPI

Replaces the wire API of the gateway: how a request is written and its reply read. Implement `Encode(Call) (*Request, error)`: the `Call` holds the `ModelConfig`, the `ModelRequest`, the key and the cache key, and the `Request` holds the `Body`, the `Header` the wire sets and `NewDecoder`, which returns a fresh `Decoder` for each attempt. `Decoder.Decode(SSEEvent)` turns one event into `ModelEvent`s and says when the reply is done. The wire never sends, retries or classifies, so it reuses the Connection, the Retry and the Classifier as they are. Pass it as the first argument of `gateway.New`; the shipped ones are `ChatCompletions{}`, `Responses{}` and `AnthropicMessages{}`. A wire may add `Validate() error`, which `gateway.New` calls.

### Connection and its HTTP client

`gateway.Connection` holds the complete `Endpoint`, an `APIKey`, `Headers`, a `CacheKeyHeader` and an `HTTPClient`. Any authentication beyond a static key goes in the `RoundTripper` of the client you pass: sign the request or add a token that expires there. With no `HTTPClient` the gateway uses `gateway.NewHTTPClient(10*time.Second, 60*time.Second)`; call it yourself for other timeouts.

### Retry

`gateway.Retry{Max, Backoff, MaxWait}` says how often a refused request or a cut stream is sent again, how long to wait and the longest wait. Pass it with `gateway.WithRetry`; the default is `gateway.DefaultRetry` (2, 200 ms, 60 s), and `gateway.Retry{}` turns retries off. Whether a failure may be repeated is the Classifier's answer.

### Classifier

`gateway.Classifier` is `func(Failure) Verdict`. A `Failure` is what one attempt showed (the status, the message, the code and type, the headers, the error under it), and a `Verdict{Retryable, Overflow, Limit}` says what it means. `gateway.DefaultClassifier` holds the gateway's own tables. To add to it, write a function that calls it and changes the Verdict, and pass that with `gateway.WithClassifier`:

```go
func(f gateway.Failure) gateway.Verdict {
	v := gateway.DefaultClassifier(f)
	if strings.Contains(f.Message, "request_too_big") { // your provider's wording for "too long"
		v.Overflow, v.Retryable = true, false
	}
	return v
}
```

## Rare

### ContextBlock

A tagged piece of the system prompt, rendered as `<tag attr="value">text</tag>`. A `ContextBuilder` fills `ModelContext.System` with them. The tag marks a boundary or where the text came from.

### ToolSearcher

Replaces the built-in BM25 search of `search_tools`. Gotato hands it a query and the specs of the tools that may load; it returns IDs, the first three load and the next five are listed. Install with `gotato.WithToolSearcher`.

### MergeRecapWriter

When the oldest Segments pass their budget they are merged into one (the Picker says how many). A `RecapWriter` that also has `WriteMergeRecap` writes the Recap of the merge (`ModelRecap` does). Any other writer leaves the merged Segment with a digest built by code.

### Model config

`Model.Config()` returns the facts the runtime reads, so compaction knows its limits. Gotato keeps no table of windows, and it guesses nothing from a model's name or address: the team states them.

| Field of `gotato.ModelFacts` | Used for |
|---|---|
| `Window` (`gotato.WindowTokens(n)` or `gotato.WindowUnknown()`) | the Compaction ceiling |
| `CacheRetention` | when a warm Session compacts, before the cache expires |
| `RoundReasoning` | leaving reasoning out of the Messages compaction keeps, and out of earlier Rounds in the request |
| `ThinkingBinding` | asking the provider to drop a thinking block whose prefix changed (Anthropic wire) |

`ModelConfig.Sampling` holds the model's default sampling, which a Session's `gotato.WithSampling` overrides field by field. `gateway.New` takes the same `ModelConfig` and returns it from `Config()`; a wrapper that embeds the Model forwards it, so no fact is hidden.

### TurnPreparer

An Extension stage before every model call, with no tool open. It may rewrite the Transcript and say so. The Runner's own compaction works this way.

### Transcript and its Capabilities

A `Transcript` is what `gotato.WithTranscript` takes, and it says what else it can do in one method, `Capabilities() gotato.Capabilities`:

| Field | Says |
|---|---|
| `ID` | the Session's ID, when not empty |
| `Journal` | writes the Events of a step as numbered Entries; nil means the Session numbers its Events in memory and stores none |
| `Durable` | the Journal's Events are stored, so they survive the process |
| `Spill` | the Journal keeps the Entries it writes, so a large Tool Result may be spilled and read back |
| `Events` | an `EventSource`: a Subscription that falls behind reads Events again from it |
| `Loaded` | a `LoadedToolSource`: the Tools a Run loaded are restored |

`session.Record` and `session.Writer` declare all of them, so you meet this only if you write a Transcript that is not backed by `session.Store`. A wrapper that embeds the concrete type forwards `Capabilities`; any other wrapper writes its own declaration, so one that forgets a part says so in code. `NewSession` refuses a Transcript that declares `Durable` without a `Journal`, `Events` and `Loaded`, so a wrapper that hides the Journal fails at construction, not weeks later as a Session that stores less. A Transcript that stores nothing declares `Capabilities{}`. For your own database, write a Store instead.

## What is not a seam

Gotato has no hook for roles, agent hierarchy, workflow or UI. Those belong to your application, which keeps them in `Session.Metadata` ([GLOSSARY.md](../GLOSSARY.md)).

## Next

- Test a seam with scripted fakes: `testkit` (`FakeModel`, `FakeTool`).
- [Embed Gotato in a service](embed-in-a-service.md) for the Runner and the Store.
- [Which packages do I import?](packages.md) for what each package gives you.
