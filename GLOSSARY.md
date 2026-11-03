# Glossary

One name for each thing in Gotato, with its meaning, the words we do not use for it, and the name the code uses. The code and the documents use the same names; where a column says "In code" the name is a Go name you can search for. Names from before the layered refactor are in the last table, for a reader of the changelog.

## Where to start

The terms come in three tiers. Read the first tier and you can use Gotato; read the others when you need them.

| Tier | You meet it when | Terms |
|---|---|---|
| **1. Everyday** | you build an agent and run it | Agent, Session, Run, Turn, Message, Model, Tool, Effect, Event, Subscription, Steer, FollowUp, Interrupt, Abort |
| **2. Running a service** | you keep Sessions in a store and run many Instances | Host, Runner, AgentProfile, TaskSpec, Registry, Name catalog, Store, Definitions, Record, Instance, Writer, Claim, Drain, Admission, Admitter, Not writer, Not run, Retryable, Warm Session, TTL |
| **3. Inside and extending** | you tune long Sessions, write an adapter, or add your own seam | Layer, Seam, Part, Adapter, Context, Context builder, Context block, Compaction, Segment, Recap, Checkpoint, Entry, Fact, Log entry, Journal, Feed, Event source, Extension, ToolSet, Tool catalog, Tool search, Model config, Model facts, Model bridge, Epoch, Record epoch, Liveness row |

[guides/extension-points.md](guides/extension-points.md) says which seam does what.

## Core

| Term | Meaning | Not | In code |
|---|---|---|---|
| **Agent** | A named, reusable definition: model, instruction (with any reference text it carries, such as a schema or a runbook), tools, the highest Effect its tools may have, Sampling, limits and its compaction ceiling. It holds no conversation, no state and no status, and it owns its instruction and ceiling: a Session has neither of its own, so another instruction is another Agent. One Agent runs many Sessions. How a host keeps its Sessions, such as their TTL, is the host's setting and not the Agent's. A host registers its Agents as AgentProfiles. | agent spec, agent config, template, sub-agent, child agent, worker, supervisor (roles belong to applications) | `profile.AgentProfile` in a host (see **AgentProfile**); with the Go API alone, the options passed to `gotato.NewSession`. |
| **Application** | The code built on Gotato that decides what Gotato does not: roles and teams, task graphs, the user interface, who may touch which Session. Gotato has no such layer of its own. | product, client | |
| **Session** | One conversation running one Agent, and the minimal unit a system interacts with. It is a Loop and a Runtime, and its ID is its Record's ID. A live Session exists during a Run: the Runner builds it over the Record when a Run starts and closes it when the Run settles, after taking the messages left in its queues. While a Run goes it has a Status, queues for Steers, FollowUps and notices, its Transcript, its Context and its tools, and it saves itself. Between Runs only the warm Session stays in memory. A Session is bound to no Instance, and any Instance can load it again from the Store. | conversation, thread, actor | `gotato.Session`, the live Session, built for each Run ([specs/session.md](specs/session.md)); `host.Runner` holds the run handle, and the Session's `store.Writer` writes each Step. |
| **Warm Session** | A Session's copy that stays in memory after its Run: its Record and the Writer that wrote it. It is a cache, never a lock: while it idles it holds no Claim, so another Instance can take the Session over and its copy is dropped. Before each Run the Instance compares the Record's head in the Store with the copy's and reloads when it changed. Its own timer compacts it before its prompt cache expires, it leaves memory after its TTL, and it counts toward the Activation limit and is the first thing evicted at the limit. | cached Session, pinned Session, actor | `host.WarmPolicy` (`host.Config.Warm`), `Runner.WarmSessions`, `store.Writer.Stale` and `Resume` ([specs/host.md](specs/host.md)) |
| **TTL** | The time to live of a warm Session: how long it stays in memory while idle, 60 minutes unless the host's setting says another. The Store keeps the Session; only the memory goes. | session timeout, expiry of the Session | `host.WarmPolicy.TTL` |
| **Record** | What the Store keeps of a Session: an append-only log of Entries. Its Facts rebuild the Session, and its Log entries record what happened. Every Entry is also an Event, so the Record is the Session's Event log. Nothing already written is rewritten. Any Instance loads a Session from its Facts, starting at its last Checkpoint. | the Session itself, snapshot | `record.Record`; a Store appends its `record.Entry` batches. |
| **Document** | The snapshot form of a Record: its Messages, Runs and latest Events as one JSON value, for export and tests. It is a copy that cannot write, and not the form a Store keeps. | dump, export | `record.Document`, from `Record.Snapshot` and `Runner.Record`; `record.FromDocument` builds a Record back from it. |
| **Loop** | The cycle inside a Session that calls the model and runs the tools it asks for, until the Run ends. Also called the agentic loop. It is a concept, not a type. | | |
| **Harness** | The industry's word for the code around a model: its loop, its tools and its context handling. In Gotato, a Session's Loop and Runtime. | framework | |
| **Runtime** | The part of a Session around its Loop: its Status, its queues, its state machine, its saving and its TTL. A Session is a Loop and a Runtime. | | In part: `gotato.Session` holds the Status, its state machine and the queues; saving is `store.Writer` through `gotato.Journal`; the warm copy and its TTL are the Runner's. |
| **Transcript** | A Session's stored history: every committed Message, in order, and a declaration of what else it can do (its Capabilities). | history, log | `gotato.Transcript`, implemented by `record.Record` and `store.Writer` |
| **Capabilities** | What a Transcript declares it can do: its ID, its Journal, whether its Events are durable, whether a large Tool Result is spilled, an EventSource and the loaded Tools it restores. A Transcript that stores must declare every part a store needs, or `NewSession` refuses it. | optional interfaces, type assertions | `gotato.Capabilities`, from `Transcript.Capabilities()` |
| **Reserved key** | A Session metadata key that starts with `gotato.`. The Runner owns it: it names the Agent, keeps a TaskSpec's snapshot, a tool switch, the model last used and the cache key. A caller that sets one is refused. | setting, config key | `host.ReservedPrefix`, `host.ErrReservedKey` |
| **Context** | What the model sees on one Turn, built from the Transcript. | prompt, model view | `gotato.ModelContext`, built by a `ContextBuilder` |
| **Context builder** | The code that builds the Context from the Transcript on every Turn. The default shows the whole committed history, which keeps the request prefix identical from Turn to Turn so the provider's prompt cache hits. Extensions that transform or convert Messages run on its output. | prompt template, strategy | `gotato.ContextBuilder`, `gotato.FullHistoryContext` (the default), `gotato.WithContextBuilder`, `profile.AgentProfile.ContextBuilder` |
| **Context block** | One tagged piece of the system prompt, rendered as `<tag attr="value">text</tag>`; the tag marks a boundary or where the text came from. | prompt section | `gotato.ContextBlock`, in `ModelContext.System` |
| **Status** | The state a Session is in, such as idle, running or failed. The Session's state machine moves it. | state | `Session.Status` (created, idle, busy, closing, closed); `gotato.RunResult.Status` for one Run: `completed`, `failed`, `canceled` or `deadline_exceeded`. |

## Execution

| Term | Meaning | Not | In code |
|---|---|---|---|
| **Run** | One request to a Session, from the input to the settled result. It may take many Turns. | job, task | `gotato.RunResult`, `host.RunRequest` and `host.RunResponse` |
| **Turn** | One model call plus the tool calls it asked for. | step, iteration | `turn_started`, `turn_ended` Events |
| **Round** | A user Message and every reply and Tool Result after it, up to the next user Message. A Steer starts a new Round; a Continue without a new prompt stays in its Round. A model whose Model facts set `RoundReasoning` is sent only the current Round's reasoning. | turn (a provider's word for it, and a Turn is something else) | no type; the model bridge finds it from the last user Message |
| **Step** | One thing a Run does that is written as it happens: the prompt, a model call starting, part of a streaming reply, a reply, Tools starting, a Tool Result, a continuation. A Turn has several. A crash loses at most the Step in flight. | | no type: the Events a Step stages are written together by `gotato.Journal.Write` |
| **Message** | One entry in the Transcript: user input, an assistant reply, a tool result or a notice. It carries the Run that committed it, if any. | | `gotato.Message`, with `RunID` |
| **Notice** | A Message that tells the model a runtime fact the user did not say, from Gotato, the service or an extension: the last reply was cut at the output limit, the user has been away for three days. It waits in the same queue as a Steer, goes in right before the next model call, and never starts a Round or a Run. A compacted stretch of history is a Segment, not a notice. | system reminder, user message | `gotato.RoleNotice`; `Session.Notice`, `Runner.Notice` |
| **Cut-off reply** | An assistant reply whose stream broke before the model finished. The Transcript keeps the text received so far as a log, and the reply is never sent to a model again. When the stream is asked for again, the new reply is a reply of its own. | partial message | a `gotato.Message` with `StopReasonCutOff` |
| **Limits** | Settings that guard a real failure: the size of a message and of what a load reads, parallel tools, and the deadline of a model call and of a tool call. The core counts no Turns or tool calls, and a Run's own deadline is its `ctx`; a cap on Turns or calls is a team's `TurnStopper`. | quotas, caps on a Run | `gotato.CoreLimits`, `gotato.WithLimits` |
| **Continue** | Resume a Loop without new input. | | `Session.Continue` |
| **Steer** | Insert a Message into a running Loop, to change its course. | | `Session.Steer` |
| **Interrupt** | A Steer that does not wait for the Turn to end: it cuts the model call in flight short, keeps the text already sent as a Cut-off reply, and starts the next Turn of the same Run with its Message. A Tool in flight finishes first. It shares the Steer queue and its limit. | cancel, abort (an Abort stops the whole Run) | `Session.Interrupt`, `host.MessageKindInterrupt` |
| **FollowUp** | Queue a Message that the Run picks up when it would otherwise settle, so the same Run goes on. | | `Session.FollowUp` |
| **Abort** | Stop a Run. The Run still settles with a result. | kill, cancel | `Session.Abort`, `Runner.AbortSession`; an `abort_requested` Event, and the Run's `run_ended` follows |

## Models

| Term | Meaning | Not | In code |
|---|---|---|---|
| **Model** | The interface Gotato calls for a reply, as a stream of events. It also describes itself with a Model config. | LLM | `gotato.Model` (`Stream` and `Config`) |
| **Model config** | One model, described once: its name, its Model facts and its default Sampling. A Model that states nothing returns the zero value. Gotato keeps no table of models. | model metadata, model profile, model table | `gotato.ModelConfig`, `Model.Config()` |
| **Model facts** | What a model is, as the team that runs it states it: its context window (a number of tokens, or `unknown` written out), how long its provider keeps the prompt cache, whether the provider takes back only the current Round's reasoning, and whether it checks a thinking block's prefix. Nothing overrides them and nothing is guessed from an address or a name. | capability, provider rule | `gotato.ModelFacts` (`Window`, `CacheRetention`, `RoundReasoning`, `ThinkingBinding`), `gotato.Window` |
| **Sampling** | A model's sampling and reasoning hints: temperature, output limit, reasoning effort and summary. The Model's defaults sit in its Model config; an Agent's own, and then a TaskSpec's, override them field by field. | model options | `gotato.Sampling`, `gotato.WithSampling` |
| **Provider** | A model service: an endpoint, a key, a Wire API and its models. In code it is values, not a type. | vendor | `model.New(wire, connection, config)` |
| **Model bridge** | The package that calls a model over HTTP: one `model.Client` joins a Wire API, a Connection, a Retry and a Classifier into a `gotato.Model`. Gotato provides and assumes no gateway; a team's own proxy is only an endpoint the Connection points at. | gateway, SDK | package `model`: `model.Client`, from `model.New` |
| **Wire API** | A request and response format that providers speak, such as OpenAI Chat Completions, OpenAI Responses or Anthropic Messages. Gotato ships each Wire API once, and any provider that speaks it works. A team that needs another writes one type that encodes a request and decodes its reply. | API format, protocol | `model.WireAPI`; `ChatCompletions`, `Responses`, `AnthropicMessages` |
| **Connection** | Where a model call is sent and as whom: the complete endpoint URL, a key, headers, a cache key header and an HTTP client. Authentication beyond a static key goes in the HTTP client's `RoundTripper`. | transport, credentials | `model.Connection`, `model.NewHTTPClient` |
| **Retry** | How often the model bridge sends a call again, and how long it waits, for a refused request or a cut stream. What is worth repeating is the Classifier's to say. | backoff policy | `model.Retry`, `model.DefaultRetry`, `model.WithRetry` |
| **Classifier** | The function that says what a failed model call means: the bridge reads a Failure and gets a Verdict (retryable, overflow, the limit named). A team wraps the default to add its own wording. | error table | `model.Classifier`, `model.DefaultClassifier`, `model.WithClassifier`, `model.Failure`, `model.Verdict` |

## Tools

| Term | Meaning | Not | In code |
|---|---|---|---|
| **Tool** | One capability a Session can call, with a name, a schema and an Effect. | function, action | `gotato.Tool` |
| **Effect** | How much a Tool can change: read, write local, write external or destructive. A Tool that declares none counts as destructive. | side effect, risk level | `gotato.ToolEffect` |
| **Effect cap** | The highest Effect an Agent's Tools may have. The model never sees a Tool above it, and the search never finds one. Only the Agent sets it, and there is none by default. | permission, policy, approval | `gotato.WithMaxEffect`, `profile.AgentProfile.MaxEffect` |
| **ToolSet** | A named group of Tools with a description and one source, such as an MCP server. It is a grouping, not the unit of loading: the model loads its Tools one by one. A team installs it active (always visible), listed in the Tool catalog with all its tool names (the default), or folded. | toolkit | `gotato.ToolSet`; `gotato.WithActiveToolSet`, `WithToolSet`, `WithFoldedToolSet`; `profile.ToolSet{Set, Mode}` |
| **Folded ToolSet** | A ToolSet whose entry in the Tool catalog shows only its description and a count of its Tools. The model expands it with the `toolset` parameter of `search_tools`. | hidden ToolSet | `gotato.WithFoldedToolSet`, `profile.ToolSetFolded` |
| **Tool catalog** | What the model reads to find Tools it has not loaded: each ToolSet's description and, by its install mode, all its tool names or only a tool count. It is the description of `search_tools`, so it stays the same while Tools load. Not to be confused with the Name catalog. | tool list, registry | the description of `search_tools` (`gotato.SearchToolName`) |
| **Tool search** | The way the model loads Tools: `search_tools` loads Tools by name, runs a BM25 search that loads the top 3 and lists 5 more candidates, or expands a folded ToolSet. A loaded Tool stays until the next Compaction, which keeps the Tools called in the closed Segment or the kept tail. A team plugs in its own search. | discovery, retrieval | `search_tools`, `gotato.ToolSearcher`, `gotato.WithToolSearcher`; the facts are `tool_loaded` and `tool_unloaded`. |
| **ToolSource** | Anything that supplies Tools at the start of each Turn. | | `gotato.ToolSource` |
| **Spilled result** | A Tool Result over one page (about 8K tokens), stored in full in the Record as an Entry of its own. The Message in the Transcript holds a preview and a Ref, and the model reads the rest page by page with `read_result`. A Session spills only with a ReadBack installed. | truncated result, attachment | `ToolResult.Spilled`, the `tool_result_spilled` Entry, `read_result` |
| **ReadBack** | What makes the originals Gotato moved out of the Context readable again: a ToolSource that offers `read_result` and `read_segment`, and the one owner of the Page size. | | `gotato.ReadBack`, `gotato.WithReadBack`; the default is `record.ReadBack{Originals, PageSize, Location}`; an AgentProfile may replace it with `profile.AgentProfile.ReadBack`. |
| **Page size** | The most one page of a read holds, in bytes: 32 KiB, about 8K tokens, by default. The spill threshold, the pages a preview counts and the pages of both read tools all use this one number. | page limit | `gotato.ReadBack.PageBytes`, `record.DefaultPageBytes` |
| **Originals** | Where a ReadBack reads the originals from: a Message by its Ref, and the Messages a Segment closed. | | `record.Originals` (`Ref`, `Segment`), `record.OriginalsOf`; `ErrNoOriginal`, `ErrRemoved` |
| **Ref** | The name of one original a Segment or a spilled result points to: `result:<call_id>` for a Tool Result, `msg:<message_id>` for a user or assistant Message. The form is a contract, since a Segment carries it forever. | id, link | the package `ref`: `ref.Result`, `ref.Message`, `ref.Parse`, `ref.Safe` |

## Hosting

| Term | Meaning | Not | In code |
|---|---|---|---|
| **Host** | Your service seen from Gotato's side: the code that builds a Runner, registers AgentProfiles and mounts an adapter. Also the package `host`, which holds the Runner. Settings of how Sessions are kept, such as the TTL, are the host's. | server | package `host`, `host.Config` |
| **Runner** | The part of a Go service that hosts Sessions: it starts them from AgentProfiles, routes messages to them, loads them from the Store and applies limits. | server | `host.Runner`, from `host.New` |
| **AgentProfile** | The Agent as a host registers it: a kind of Agent, like a process. It holds the name, instruction, model, Tools and ToolSets, Effect cap, Sampling, extensions, Context builder, limits, compaction parts, a Run timeout and, optionally, the ReadBack that replaces the default. It holds Go values, so it is registered inline, or stored as a document that names parts of the Name catalog. A Session looks its AgentProfile up by name each time it is built: a replaced profile reaches new and rebuilt Sessions and never a Run in flight. | agent spec, agent config | `profile.AgentProfile`, `profile.AgentProfileDoc` |
| **TaskSpec** | A job that runs again, like a thread of an AgentProfile's process: a name, its AgentProfile, a Background and bounds that only narrow it (a subset of the Tools and ToolSets, an Effect cap no higher, a timeout no longer, an output limit no higher). A Run or a create of a TaskSpec opens a new Session; the Background is added once, after the instruction, and the Record keeps the TaskSpec as it was, so every Instance builds the Session the same after the registered spec changes. Where its result goes, and when it runs, are the service's. | job, template, role | `profile.TaskSpec`, `TaskSpec.Narrow`, `TaskSpec.Apply`; `host.RunRequest.Task` and `.TaskSpec`, `host.CreateSessionRequest.Task` and `.TaskSpec`; HTTP `task`; the metadata key `gotato.task_spec` |
| **Name catalog** | What a stored definition may name: the Models, Tools and ToolSets the service's code offers, each by name. A stored AgentProfile or TaskSpec selects from it and never adds to it, and a name it lacks is refused. Say Name catalog in prose, because a bare catalog is the Tool catalog. | registry, plugin list | `profile.Catalog`, from `profile.NewCatalog(models, tools, toolSets)` |
| **Registry** | Where a host registers its AgentProfiles and TaskSpecs and asks for them by name. There are four registrations, each name once and a later one replacing it: inline as a value, or from a store by name, for each of the two kinds. A name that is not registered is not a definition, however many rows a store holds. | service locator | `profile.Registry`, from `profile.NewRegistry(catalog)`; `host.Config.Profiles`, `Config.DefaultAgent` |
| **Definitions** | An optional store of documents by kind and name, where a service keeps its AgentProfile and TaskSpec definitions. It saves and reads bytes and knows nothing of profiles. It is a contract of its own, not a part of the Store. | profile table, config store | `store.Definitions`, `store.NewMemoryDefinitions`, `store.NewFileDefinitions`; the suite is `storetest.RunDefinitions` |

## State and history

| Term | Meaning | Not | In code |
|---|---|---|---|
| **Store** | Where Sessions are saved, as Records: memory, a file, or your own database. The Store is the truth, and a Runner's memory is only a cache. A Session built with `gotato.NewSession` alone keeps its Record in memory. | repository, persistence | `store.Store{Records, Liveness}`, two seams, and an optional `store.Lister`; `store.MemoryStore`, `store.FileStore`; the conformance suite `store/storetest` |
| **Entry** | One line of a Record, at a position no other Entry ever takes: a Fact or a Log entry. Every Entry is a stored Event, and its position is that Event's number. | row | `record.Entry` (`Kind`, `Seq`) |
| **Fact** | An Entry a rebuild reads: a Message, Tool calls that started, a metadata change, a Claim or a Checkpoint. The Context is built from the Facts on every Turn and is never stored. | | `record.LayerFact` |
| **Log entry** | An Entry that records what happened, for people and telemetry: a Run's start and end, usage, a model call, a retry, a release, part of a streaming reply. A rebuild does not read it, except the reply parts of a Run that never ended. | | `record.LayerLog` |
| **Compaction** | Closing what came since the last Checkpoint into a Segment, and appending a Checkpoint that lists the Segments it uses and holds the kept tail. The Messages it covers stay in the Record but are no longer loaded. | summarization, pruning | `record.Compact`, `record.AutoCompact`; the frame is a `record.Compactor` |
| **Checkpoint** | A Fact that holds the state of a Session's Facts at one point, so it stands in for everything before it. Every Record starts with one, and Compaction appends one with every Segment, the kept tail and the loaded Tools. A load starts at the last one. | snapshot | the `session_created` and `compaction_ended` Entries (`Entry.IsCheckpoint`), `Record.Checkpoint` |
| **Segment** | One stretch of a Session's history that a Compaction closed: the user's messages and each Run's final reply word for word, one line for each tool call inside its Run, and a Recap at its end. It has a number and is never rewritten, except that the oldest are merged, into the one **merged Segment** that opens the Context, named by their range, `<segment n="1-3">`. The model can read any Segment back in full with `read_segment`, a merged one's originals by their own numbers. | epoch (the Record epoch and a Claim's Epoch are other things), summary | `gotato.Segment`, stored as a `segment_closed` Entry; a merged Segment has `Merged` set |
| **Recap** | The end of a Segment, which the Agent's own model writes on the cached prefix: what happened, key facts, decisions, what is still open, and where it stands. A Recap that breaks the rules is dropped and its Segment is kept without one. It is off until a team turns it on. A merged Segment's Recap is written from the Recaps it merges, or by code as a digest when no model writes it. | summary | `gotato.Segment.Recap`; `record.RecapWriter`, `ModelRecap`, `TruncateRecap` |
| **Compactor** | The frame of a compaction: it runs the Parts in order and alone writes and holds the guarantees (one batch, no Segment dropped or moved, a Segment written once, readable Refs, once per Turn). A Part that fails, panics or breaks a rule drops that compaction and never the Run. A nil Part is its default. | pipeline, strategy | `record.Compactor{CompactPolicy, Trigger, Cut, Builder, Recap, Merge}`; `profile.AgentProfile.Compact` |
| **Trigger** | The Part that says when a compaction is due, and why. | scheduler | `record.Trigger`; the default is `CeilingTrigger` |
| **Cutter** | The Part that says where the kept tail starts in the Messages since the last Checkpoint. | | `record.Cutter`; the default is `TailBudget` |
| **Builder** | The Part that renders the closed Messages as a Segment. | | `record.Builder`; the default is `CodeSegments` |
| **Picker** | The Part that says how many of the oldest Segments a merge replaces. | | `record.Picker`; the default is `OldestToHalf` |
| **SegmentStore** | What a compaction reads and writes: the Context's Segments, tail and loaded Tools, and two batches, one that closes a Segment and one that merges. | | `record.SegmentStore`; `*record.Record` is one. |
| **Fork** | Starting a new Session from another Session's Record, as a branch at one point of its log. It writes the Store and activates nothing. | clone | `record.Fork`, `Runner.Fork` |

## Watching and extending

| Term | Meaning | Not | In code |
|---|---|---|---|
| **Event** | Something that happened to a Session or an Instance, as a value of its own type, named for what happened, such as `run_started`. A stored Event is an Entry, numbered by its position in the Record. A delta, such as `text_delta`, is only sent. An Instance Event happens where no Claim is held, and goes only to that Instance's Subscriptions. | log line | `gotato.Event`, one Go type for each kind (`gotato.RunStarted`), each with a `gotato.Header`; a Runner's Instance Events are types of `host`, such as `host.RunRejected`. |
| **Subscription** | A queue of Events, in order: a Run's, a Session's from any number, or an Instance's. A Session never waits for one; a subscriber that falls behind catches up from the Record. | observer, listener | `gotato.Subscription`, from `Session.Subscribe`, `Runner.StreamRun`, `StreamSession` and `SubscribeInstance` |
| **Feed** | What fans a stream of Events out to Subscriptions. Publishing never blocks. | broker, bus | `gotato.Feed` (`Subscribe`, `Close`); a host's own plumbing for it is internal |
| **Event source** | Where a Subscription that fell behind reads stored Events again, by number. The Record is one. | | `gotato.EventSource`, implemented by `store.Writer` |
| **Journal** | What a Transcript declares when it keeps the Session's stored Events: the Session hands it the Events of each Step to write, in order, and it returns them numbered. Without one, the Session numbers its Events in memory and stores none. | event writer | `gotato.Journal`, implemented by `record.Record` and `store.Writer` |
| **Extension** | Code that wraps the Loop at a defined point. One value may take part in several of the six stages: `TurnPreparer` (before a model call, the one safe point to rewrite the Transcript), `ContextTransformer`, `MessageConverter`, `PreToolUse`, `PostToolUse` and `TurnStopper`. | hook, middleware, plugin | `gotato.WithExtension` and the stage interfaces named here |

## Design

| Term | Meaning | Not | In code |
|---|---|---|---|
| **Guideline** | One of the ten hard commitments in DESIGNS.md that the whole design must meet, each with the question it asks of a new requirement. A design breaks one guideline or none. | principle (the word decisions written before 2026-11-03 use) | no type |
| **Layer** | A package that does one job and imports only the layers below it: `host`, then `profile`, then `store`, then `record`, then `gotato`, then `ref`. The model bridge, `testkit` and the Adapters sit beside the layers and import down. | tier | `gotato/layering_test.go` enforces the order. |
| **Seam** | A small interface where Gotato ships a default and a user can plug in their own, without forking Gotato: the Store, a Model, a Wire API, a Part, a ToolSource. | hook, plugin | no type |
| **Part** | One replaceable piece behind a Seam, with a default, inside a frame that keeps the guarantees whatever the Part returns. Compaction has Trigger, Cutter, Builder, RecapWriter and Picker. | module, stage (a stage is an Extension's place in the Loop) | |
| **Adapter** | A module of its own that connects Gotato to something outside it: the HTTP API over a Runner, an MCP server's tools as a ToolSet. No core or runtime package imports one. | plugin, driver | `adapter/http`, `adapter/mcp` |

## Cluster and positioning

| Term | Meaning | Not | In code |
|---|---|---|---|
| **Instance** | One running copy of your service. On Kubernetes it is a pod; the glossary and the code say Instance. | replica, node | `host.Config.Instance` names it |
| **Not writer** | What an error says when a message reaches an Instance that is not the Writer of the Session's Run going on: the code `not_writer`, which is also Not run and Retryable. It carries the Writer's advertised address, so the caller, or the HTTP endpoints when a team opts in, sends the message there. | redirect, forward | `host.CodeNotWriter`, `host.WriterOf` |
| **Not run** | What an error says when nothing with side effects happened, so the same request can go to another Instance without running twice. A refusal before a Run starts is not run; a store failure may have run, and is not marked. | | `gotato.NotRun(err)`, HTTP `Gotato-Not-Run` |
| **Retryable** | What an error says when the same request, sent again later, may succeed. | | `gotato.Retryable(err)`, HTTP `Gotato-Retryable` |
| **Admission** | The decision whether a Run may start on an Instance. In order: a draining Instance refuses, the memory bound refuses (Activation limit), and then the service's own Admitter decides. The Run it is asked about is an `Admission` (the Session, the AgentProfile and, for a TaskSpec's Run, the TaskSpec's name), and the Instance's counts are its `Load`. | rate limit, quota | `host.Admission`, `host.Load`; `host.Bounds` holds the memory bound and the `host.QueuePolicy` for a busy Session |
| **Admitter** | The policy of a service for whether a Run may start: it is given the Run, the Instance's `Load` and the caller's `ctx`, which carries who asks, and returns a `release` the Runner calls once when the Run ends, or a refusal that is not run. The default is `Limits{MaxActiveRuns}`. | quota, rate limiter | `host.Admitter`, `host.Limits`, `host.Config.Admitter` |
| **Drain** | Finishing work before an Instance shuts down: it takes no new Sessions, lets every Run finish, saves, releases its Claims, and only then disconnects. | | `Runner.Drain`, then `Runner.Close` |
| **Writer** | The one Instance allowed to append to a Session's Record at a time. It holds the Claim while a Run goes, not while a warm Session idles, and it is wherever the Load balancer sends the Session's requests. | owner, leader, master | `host.Runner` as its Instance, writing through a `store.Writer` |
| **Claim** | A Writer's hold on a Record: a Fact naming the Instance and an Epoch, written at the position after the head that Instance read. While the Record is busy, it is valid as long as that Instance's Liveness row is alive; a Record that is not busy is claimable at once. | lease, lock | `record.Claim` |
| **Epoch** | A number in a Claim. It grows when a different Instance claims the Record, and on the first Claim; the same Instance claiming again keeps it. An Instance that lost its Claim always writes to a taken position, and the Store refuses it. | version, generation | `record.Claim.Epoch` |
| **Record epoch** | An identity a Record gets when it is created. It changes only if the Record is deleted and created again under the same ID, so a number a client holds means the same Event only within one epoch. A stream that resumes is refused when the client's epoch is not the Record's. It is not the Epoch of a Claim. | version, generation | `record.Record.RecordEpoch`, `record.Header.Epoch`; `record_epoch` in `session_created` and in a Run's result |
| **Activation limit** | The hard limit on the Sessions an Instance has active; with the warm Session, the Sessions it holds in memory, idle ones included. At the limit, the Instance first evicts the longest-idle warm Session, and only when every live Session is running a Run does it refuse a Run that would activate another, as not run. | quota, rate limit | `host.Bounds.MaxActiveSessions`, `host.ErrOverloaded` |
| **Liveness row** | One row per running Instance in the Store, renewed on a heartbeat and timed by the Store's clock. While a Record is busy, its Claim is valid as long as the holder's row is alive: when the row expires, every busy Claim of that Instance ends at once. It can also carry the Instance's advertised address. | heartbeat (the act of renewing it) | `store.Liveness` (`Beat`, `Instance`); `host.Heartbeat` holds its settings. |
| **Advertised address** | An address an Instance puts in its Liveness row, an optional setting: where a caller can reach that Instance's own endpoint. It is what Not writer names. Gotato reads it and never connects to it. | service discovery, membership | `host.Config.Address`, `store.InstanceRow.Address` |
| **Load balancer** | The router in front of your Instances. It sends every request for one Session to one Instance by hashing its `session-id`. It belongs to your infrastructure, not to Gotato. | gateway | |
| **Cloud agent** | An Agent whose Sessions live in your own cluster and are called like any other service. | | |
| **Agent-as-a-Service** | Gotato's mission: any service can easily become an agent, and the agent becomes the cloud service itself. Callers start and talk to Sessions like any other API, inside your own infrastructure. | | |

## Names you may meet in older text

The changelog names packages as they were when each change was made. Read them through this table.

| Old name | Today |
|---|---|
| `session.Record`, `Entry`, `Header`, `Claim`, `Fork`, `Document` | `record.<same name>` |
| `session.RecordInfo`, `session.RecordOption` | `record.Info`, `record.Option` |
| `modelctx` (the Context, `Compact`, `Compactor`, `ReadBack`, the Parts) | `record` |
| `session.Store`, `Records`, `Liveness`, `Lister`, `Definitions`, `MemoryStore`, `FileStore`, `Writer` | `store.<same name>` |
| `session/storetest` | `store/storetest` |
| `gateway` (the Model bridge) | `model` |
| `service` (`Runner`, `Config`, `RunRequest`, `Admitter`) | `host` |
| `service/httpapi` | `adapter/http`, a module of its own |
| `service.Agent` | `profile.AgentProfile` |
| `service.Liveness` (a setting) | `host.Heartbeat` |
| the sampling struct once called `gotato.ModelConfig` | `gotato.Sampling`; `gotato.ModelConfig` now describes the model |
| `WithModelConfig`, `ModelRequest.Config` | `WithSampling`, `ModelRequest.Sampling` |
| model profile (the model's description) | Model config and Model facts |
| `gotato.instruction`, `gotato.compact_ceiling` (metadata keys) | none; another instruction or ceiling is another Agent |
