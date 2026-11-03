# Extension points

> [Wiki](../README.md) › [Reference](../README.md#reference) › Extension points

By the end of this page you know which seam of Gotato to reach for when the default is not what you want, and which seams you will most likely never need. Each one is a Go interface or a function type: you write a small value, and pass it in an option or a field.

## TL;DR

**Common** (most services use one or two of these)

| I want to | Seam |
|---|---|
| call my own model or provider, and say what it is (its window, cache retention, reasoning rule) | `gotato.Model` (`Stream` and `Config`) |
| give the agent a capability | `gotato.Tool` (`NewFuncTool`) |
| save Sessions in my own database | `store.Records` and `store.Liveness` |
| change what the model sees | `gotato.ContextBuilder` |
| block a tool call, rewrite its result, or cap the Turns | Extension stages `PreToolUse`, `PostToolUse`, `TurnStopper` |
| bound the memory of one pod | `host.Bounds` |
| decide whether a Run may start, such as a limit for each tenant | `host.Admitter` |
| register the Agents a service runs, and the jobs that run again | `profile.Registry`: `RegisterAgentProfileInline`, `RegisterTaskSpecInline` |

**Sometimes**

| I want to | Seam |
|---|---|
| expose a group of tools, such as an MCP server | `gotato.ToolSet` |
| keep Agent and job definitions in my own database | `store.Definitions`, with `RegisterAgentProfileWithStore`, `RegisterTaskSpecWithStore` and a `profile.Catalog` |
| supply tools that change between Turns | `gotato.ToolSource`, `profile.AgentProfile.ToolSources` |
| write Recaps with a model | `record.RecapWriter` (`ModelRecap`) |
| choose when a Session compacts, or where the kept tail starts | `record.Trigger`, `record.Cutter`, in a `record.Compactor` |
| prune or reshape the Messages after the builder | `gotato.ContextTransformer`, `gotato.MessageConverter` |
| speak a wire API Gotato does not ship | `model.WireAPI` |
| set how often a model call is sent again, or turn it off | `model.Retry`, `model.WithRetry` |
| change what a failed model call means | `model.Classifier`, `model.WithClassifier` |

**Rare** (a specific need, or a store adapter's concern)

| I want to | Seam |
|---|---|
| put tagged blocks in the system prompt | `gotato.ContextBlock` |
| replace the search of `search_tools` | `gotato.ToolSearcher` |
| write the Recap of merged Segments myself | `record.MergeRecapWriter` |
| change how a Segment reads, or how many Segments merge | `record.Builder`, `record.Picker` |
| compact a Transcript that is not a `record.Record` | `record.SegmentStore` |
| read spilled results and Segments from my own store, or change the page size | `gotato.ReadBack`, `record.Originals`; in a service, `profile.AgentProfile.ReadBack` |
| rewrite the Transcript before a model call | `gotato.TurnPreparer` |
| keep a Session's Events in my own log | `gotato.Journal`, `gotato.EventSource` |

## The chooser

Each row says what you want, the seam that does it, and where it lives. "Tier" tells how often a service needs it: **common**, **sometimes**, or **rare**.

| I want to ... | Use this seam | Where it is | Tier |
|---|---|---|---|
| call a model of my own, or wrap one (retries, a header, a counter) | `gotato.Model` | `gotato.WithModel`; `profile.AgentProfile.Model` | common |
| give the agent a function to call | `gotato.Tool`, built by `gotato.NewFuncTool` | `gotato.WithTools`; `profile.AgentProfile.Tools` | common |
| keep Sessions in Postgres, Redis or another database | `store.Records` and `store.Liveness`, and optionally `store.Lister` | `host.Config.Store` (`store.Store{Records:, Liveness:}`); prove it with `store/storetest` | common |
| change what the model sees on each Turn (window, selection, a layout of my own) | `gotato.ContextBuilder` | `gotato.WithContextBuilder`; `profile.AgentProfile.ContextBuilder` | common |
| refuse a tool call, or change its result | `gotato.PreToolUse`, `gotato.PostToolUse` | `gotato.WithExtension` | common |
| stop a Run after N Turns or N calls | `gotato.TurnStopper` | `gotato.WithExtension` | common |
| limit how many Sessions one pod keeps in memory, and what a busy Session does | `host.Bounds` | `host.Config.Bounds` | common |
| decide whether a Run may start: a bound on the Runs in flight, a limit for each tenant or caller | `host.Admitter` (`Admit(ctx, Admission, Load) (release, error)`) | `host.Config.Admitter`; the default is `host.Limits{MaxActiveRuns}` | common |
| bring all the tools of an MCP server, or any named group | `gotato.ToolSet` | `gotato.WithActiveToolSet`, `WithToolSet`, `WithFoldedToolSet`, or `profile.AgentProfile.ToolSets` with a `profile.ToolSet{Set, Mode}`; `adapter/mcp` builds one | sometimes |
| supply tools that change between Turns (by user, by feature flag) | `gotato.ToolSource` | `gotato.WithToolSource`; `profile.AgentProfile.ToolSources` | sometimes |
| have the Agent's own model write the Recap of a Segment | `record.RecapWriter` (`ModelRecap`, `TruncateRecap`) | `record.Compactor.Recap`, in `profile.AgentProfile.Compact` | sometimes |
| drop or rewrite Messages after the builder ran | `gotato.ContextTransformer` | `gotato.WithExtension` | sometimes |
| change the shape of Messages for one provider | `gotato.MessageConverter` | `gotato.WithExtension` | sometimes |
| add tagged blocks to the system prompt | `gotato.ContextBlock` | `ModelContext.System`, set by a `ContextBuilder` | rare |
| search tools my own way (embeddings, a service) | `gotato.ToolSearcher` | `gotato.WithToolSearcher` | rare |
| decide when a Session compacts | `record.Trigger` | `Compactor.Trigger`; the default is `CeilingTrigger` | sometimes |
| decide where the kept tail starts | `record.Cutter` | `Compactor.Cut`; the default is `TailBudget` | sometimes |
| change how a closed stretch reads as a Segment | `record.Builder` | `Compactor.Builder`; the default is `CodeSegments` | rare |
| decide how many of the oldest Segments merge | `record.Picker` | `Compactor.Merge`; the default is `OldestToHalf` | rare |
| write the Recap of merged Segments myself | `record.MergeRecapWriter` | the same `Compactor.Recap`; a writer with `WriteMergeRecap` is used for merges | rare |
| compact a Transcript of my own | `record.SegmentStore` | `record.Compact(ctx, mine, compactor, cfg)`, `record.AutoCompact(mine, compactor)` | rare |
| read spilled results and Segments back from my own store, or change the page size | `gotato.ReadBack`, with `record.Originals` | `gotato.WithReadBack`; the default is `record.ReadBack{Originals: record.OriginalsOf(rec, log)}` | rare |
| report the window, the cache time or the reasoning rule of my model | `Config() gotato.ModelConfig` on the `gotato.Model` (a wrapper that embeds the Model forwards it) | read by the Runner and compaction from `Model.Config().Facts` | common |
| speak a wire API Gotato does not ship | `model.WireAPI` (`Encode(Call) (*Request, error)` and a `Decoder`) | the first argument of `model.New` | sometimes |
| authenticate with more than a static key (a signed request, a token that expires) | an `http.RoundTripper` | `model.Connection.HTTPClient` | sometimes |
| change how often a model call is sent again, or turn it off | `model.Retry` | `model.WithRetry`; `model.Retry{}` is off | sometimes |
| change what a failed model call means (retryable, too long) | `model.Classifier` | `model.WithClassifier`; wrap `model.DefaultClassifier` to add to it | sometimes |
| rewrite the Transcript before a model call, such as to compact | `gotato.TurnPreparer` | `gotato.WithExtension` | rare |
| write a Session's Events to a store of my own | `gotato.Journal` (declared in `Transcript.Capabilities()` with `EventSource`, `Spill` and `LoadedToolSource`) | a `Transcript` given to `gotato.WithTranscript`; `store.Records` is the usual way | rare |
| let a subscriber catch up from my own log | `gotato.EventSource` | `Events` in the `Capabilities` of a `Transcript` given to `gotato.WithTranscript` | rare |

## Where each seam is explained

| Seam | Page |
|---|---|
| `gotato.Model` and its `Config` | [Write a tool and a model](../how-to/write-a-tool-and-model.md), [State what your model is](../how-to/state-what-your-model-is.md) |
| `gotato.Tool` | [Write a tool and a model](../how-to/write-a-tool-and-model.md) |
| `store.Records`, `store.Liveness`, `store.Lister` | [Write a store adapter](../how-to/write-a-store-adapter.md) |
| `gotato.ContextBuilder`, `ContextTransformer`, `MessageConverter`, `ContextBlock`, `TurnPreparer` | [Change what the model sees](../how-to/change-what-the-model-sees.md) |
| `PreToolUse`, `PostToolUse`, `TurnStopper` | [Add checks and stops to the Loop](../how-to/add-checks-and-stops.md) |
| a person before a step | [Ask a person before a step](../how-to/ask-a-person-before-a-step.md) |
| `host.Bounds`, `host.Admitter` | [Limit Runs per tenant](../how-to/limit-runs-per-tenant.md) |
| the caller's identity in `ctx` | [Know who asked](../how-to/know-who-asked.md) |
| `gotato.ToolSet`, `ToolSource`, `ToolSearcher` | [Add tools that change between Turns](../how-to/add-tools-that-change-between-turns.md) |
| `profile.Registry`, `store.Definitions`, `profile.Catalog` | [Register Agents and jobs](../how-to/register-agents-and-jobs.md) |
| `record.RecapWriter`, `MergeRecapWriter`, `Trigger`, `Cutter`, `Builder`, `Picker` | [Tune compaction](../how-to/tune-compaction.md) |
| `record.SegmentStore`, `Transcript`, `Journal`, `EventSource` | [Compact your own Transcript](../how-to/compact-your-own-transcript.md) |
| `gotato.ReadBack`, `record.Originals` | [Read back from your own store](../how-to/read-back-from-your-own-store.md) |
| `model.WireAPI`, `Connection`, `Retry`, `Classifier` | [Customize the model bridge](../how-to/customize-the-model-bridge.md) |

## The common seams in brief

### Model

A `gotato.Model` has two methods: `Stream` starts a call and returns its events, and `Config` returns a `gotato.ModelConfig`, what the model is. The `model` package implements it for three wire APIs; you implement it for anything else. A Model that states nothing returns the zero `ModelConfig`: an unknown window, no cache retention and no default sampling. A wrapper that embeds the model it wraps forwards `Config` for free; one that holds it in a field returns `Inner.Config()`, or the window and cache facts are lost.

### Tool

A tool is a `gotato.Tool`. `NewFuncTool` builds one from a Go function, and its input type becomes the schema. Give a tool its Effect so the Agent's Effect cap can hide it. [Getting started](../tutorials/getting-started.md) shows the first one.

### Store

A Store is where Records live. The memory and file stores ship with `store`. A `store.Store` is two seams side by side: `Records` (`Create`, `Append`, `Load`, `Log`, `Delete`) and `Liveness` (`Beat`, `Instance`), so they may live in two databases. A `Records` may also be a `store.Lister`, which `Runner.Sessions` and `GET /v1/sessions` need and the runtime does not; without one they answer `not_supported`. For a database of your own, implement them and run `storetest.RunRecords`, `RunLiveness` and, if you list, `RunLister` (package `store/storetest`) on it; the suite names the rules the Runner relies on. Definitions are a separate contract, below.

## What is not a seam

Gotato has no hook for roles, agent hierarchy, workflow or UI. Those belong to your application, which keeps them in `Session.Metadata` ([GLOSSARY.md](../../GLOSSARY.md)).

## See also

- [Which packages do I import?](packages.md)
- [Run Sessions on many pods](../how-to/run-sessions-on-many-pods.md)
