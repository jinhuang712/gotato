# Changelog

What changed in each Gotato release, from the user's side. Newest first. A breaking change carries a before/after note, so you can move your code in one pass. Additive changes may be listed, but breaking ones must be.

## Unreleased

Phases 2, 3, 4 and 5 of [ROADMAP.md](ROADMAP.md): the Effect cap, the names, every step of a Run written as it happens, one writer per Session, the drain, the Event design with the Claim Events, the load balancer's part, stream resume on any instance, and the multi-instance harness. The Effect cap is an addition. The names ship as one planned break: the HTTP and gRPC contract version is now `"4"`, and the CLI contract version is now `"2"`. The store contract changes too, and every defect in [TODO.md](TODO.md) is fixed; the breaking changes among them each carry a before and after below. It also fixes how a Run ends when it fails or is cancelled during a tool batch, and what the Runner returns for a cancelled Run.

### Names

- **The code now uses the names in [GLOSSARY.md](GLOSSARY.md).** The Agent is a definition, `service.Agent`. The object you build and talk to is the live Session, `gotato.Session`: one concrete type, with the interfaces the old `gotato.Agent` was split into removed, because nothing implemented them but the core. What the store keeps is the Session's Record, `session.Record`. The Loop stays a concept. No `// Deprecated:` aliases are kept. **Breaking:**

  | Before | After |
  |---|---|
  | `service.AgentSpec`, `service.Config.Specs`, `Runner.Spec(name)` | `service.Agent`, `service.Config.Agents`, `Runner.Agent(name)` |
  | `gotato.NewAgent(…) (RuntimeAgent, error)` | `gotato.NewSession(…) (*gotato.Session, error)` |
  | `gotato.Agent`, `RuntimeAgent`, `ControllableAgent`, `EventAgent`, `LifecycleAgent`, `AgentLifecycle`, `RunCanceler`, `IdleWaiter`, `ToolInspector`, `ContextInspector`, `EventSource`, `LifecycleSource` | removed; every method is on `*gotato.Session` |
  | `gotato.AgentID` | `gotato.SessionID` |
  | `gotato.AgentStatus`, `AgentCreated` … `AgentClosed` | `gotato.SessionStatus`, `SessionCreated` … `SessionClosed` |
  | `ErrAgentClosed`, `ErrAgentClosing` (`agent_closed`, `agent_closing`) | `ErrSessionClosed`, `ErrSessionClosing` (`session_closed`, `session_closing`) |
  | `EventAgentStart`, `EventAgentEnd` (`agent_start`, `agent_end`) | `run_started`, `run_ended` (see Events) |
  | `LifecycleAgentCreated`, `…Closing`, `…Closed` (`agent_created`, …) | removed with `SubscribeLifecycle` (see Events) |
  | `AgentID` (`agent_id`) on `Event`, `LifecycleEvent` and the extension inputs | `SessionID` (`session_id`) |
  | `session.Session` | `session.Record` |
  | `session.Record(s)`, the recorder | removed: a `session.Record` is a `gotato.Journal` (see Events) |
  | `testkit.NewSession` | `testkit.NewRecord` |
  | `BenchmarkAgentFootprint`, `heap-B/agent` | `BenchmarkSessionFootprint`, `heap-B/session` |

  ```go
  // before
  agent, err := gotato.NewAgent(gotato.WithModel(model), gotato.WithTranscript(s), gotato.WithExtension(session.Record(s)))
  runner, err := service.New(service.Config{Store: store, Specs: []service.AgentSpec{{Name: "default", Model: model}}})
  // after
  live, err := gotato.NewSession(gotato.WithModel(model), gotato.WithTranscript(r))
  runner, err := service.New(service.Config{Store: store, Agents: []service.Agent{{Name: "default", Model: model}}})
  ```
- **A live Session's ID is its Session ID, and every Event carries it.** `gotato.Session` takes its ID from its Transcript when the Transcript has an `ID() string` method, as a `session.Record` does, and generates one otherwise. The Runner's own Events carry it too. **Breaking:** the `session_id` payload key of `session_loaded`, `session_saved` and `session_compacted` is gone; read the Header's `SessionID` instead.
- **A Run record no longer has `agent_id`.** It held a counter that meant nothing after a restart, and nothing read it. `Record.RecordRunStart` takes the Run's ID and time only. A Record saved before still loads; the old key is ignored, and its stored Events keep their old kinds and `agent_id`.
- Error messages say "Session is closed" and "Session is already processing a Run" where they said "Agent".

### Runtime

- **The default `MaxMessages` is 10,000, where it was 1,000.** The limit counts the Messages a load reads, and once a Session has Segments those include the Messages that render them, so a Session of short messages would have reached 1,000 long before its Context grew large. The size of the Context is bounded by the ceiling in tokens and by `MaxTranscriptBytes`, which stays at 32 MiB. A Session that set `MaxMessages` itself keeps its value.
- **An Agent can cap the Effect of its tools.** `gotato.WithMaxEffect(effect)` and `service.Agent.MaxEffect` name the highest Effect a tool may have. A tool above the cap is never shown to the model, including the tools of a ToolSet activated later, and a call to it fails as a call to an unknown tool. A tool that declares no Effect counts as destructive, so any lower cap hides it. A Session cannot switch on a hidden tool: `Runner.Tools` does not list it, and `Runner.SetToolActive` reports it as unknown. There is no cap by default, so an Agent that names none behaves as before. An unknown Effect fails `NewSession` and `service.New`.
- **The tool that activates a ToolSet declares `read`.** It changes only which tools the model sees, so every cap keeps it. Its `ToolSpec`, and the `ToolUse` a `PreToolUse` extension gets for it, now carry `EffectRead` instead of no Effect.
- **`context_built` counts are numbers, and the ContextBuilder's metadata has its own key.** The Session merged the builder's `Metadata` into the payload under the same keys, so `FullHistoryContext`'s `"source_messages": "3"` replaced the Session's count, and `selected_messages` and `dropped_messages` always arrived as strings. The payload now has the shape of the `context inspect` report: `strategy`, `messages`, `source_messages`, `selected_messages`, `prefix_hash`, `prefix_messages`, `system_bytes` and `tools` are the Session's own, each count a number, and the builder's `Metadata` sits under `metadata`, as its strings. The same payload reaches HTTP, gRPC (`payload_json`) and `gotato events`. **Breaking:** read `dropped_messages`, and any key your own builder sets, from `metadata`.

  ```text
  before  {"strategy":"full_history","source_messages":"3","selected_messages":"3","dropped_messages":"0","messages":3,…}
  after   {"strategy":"full_history","source_messages":3,"selected_messages":3,"messages":3,…,
           "metadata":{"strategy":"full_history","source_messages":"3","selected_messages":"3","dropped_messages":"0"}}
  ```

- **`ToolUse` no longer has `Executed` or `Result`.** A `ToolUse` reaches a Tool and a `PreToolUse` extension before the call runs, so the two fields were never set: they were always `false` and `nil`. The outcome of a call is the `ToolResult` that a `PostToolUse` extension receives, with its `Executed` and its content. **Breaking:** code that names the fields no longer compiles, and `ToolUse` JSON has no `executed`. Read the `ToolResult` instead.

  ```go
  // before: always false, because the call has not run yet
  func (a audit) Before(ctx context.Context, use gotato.ToolUse) (gotato.PreToolDecision, error) { if use.Executed { … } }
  // after: the outcome arrives in PostToolUse
  func (a audit) After(ctx context.Context, result gotato.ToolResult) (gotato.ToolResult, error) { if result.Executed { … } }
  ```

- **A Run that fails during a tool batch still answers every tool call.** A Run could fail after its assistant message was committed with tool calls but before every result was: `MaxToolCalls` reached in preflight, a tool result over `MaxToolResultBytes`, a failing `PreToolUse` or `PostToolUse` extension, or a failing observer. Those calls stayed without a result, and every provider refuses a request like that, so no later Run of the Session could succeed. The failed Run now commits a closing result for each open call, `failed`, or `cancelled` when the Run was cancelled, with a `SafeError` that starts with the error code, and emits `tool_result_committed` for it. These closing results do not count against the Message and transcript limits, since the Run may be ending on one of them.
- **An aborted Run starts no more tools.** In a sequential batch, the tools after the one running at `Abort` were still started with the cancelled context, so a tool that ignored its context carried out its side effect after the Run was stopped. They are now settled as `cancelled` with `Executed` false, and never started. Each of them, and each tool cancelled while running, now gets its `tool_execution_end` Event.
- **A tool that was running at cancellation is recorded as executed.** Its result was `cancelled` with `Executed` false, even when the tool had already finished, or had started and could have done its work. A tool whose executor started now keeps `Executed` true, and a result that had already arrived keeps its content. `Executed` false now means only that the executor never ran: the call was blocked, rejected, or never started.
- **A cancelled Run in the Runner keeps its result.** `CancelRun`, `CancelSession` and `Drain` cancelled the context the live Session's `Prompt` waited on, so `Prompt` returned before the Run settled, and every cancelled Run came back with an empty `run_id`, no Turns and no usage, over HTTP, gRPC and the CLI too. Once the live Session is built, the Runner now stops a Run only through `Abort`, and the result carries the Run's ID, usage and metrics, as the Session's Run record does.
- **A reply cut off mid-stream is kept.** When a stream breaks after the model sent some text (a provider or network error, a deadline, or an abort), the text received so far is committed as an assistant Message with the new `StopCutOff` (`"cut_off"`), followed by `message_end`, and the Run settles as before. Reasoning and Tool calls that arrived before the cut are not kept. `ForModel` leaves a cut-off reply out, so the model never sees it again, and `Continue` asks the model afresh from the Message before it. Before, the text was dropped. Code that reads a Transcript sees the new stop reason.

- **A Record keeps its changes as a log of Entries.** Once a Record is stored, every change to it is also a `session.Entry`: a fact, which rebuilds the Session (a `Checkpoint`, a Message, a metadata change), or a log Entry, which records what happened (Runs, usage, Events). `session.FromEntries` rebuilds a Record from a Checkpoint and the Entries after it, and `WithMessages` starts a Record with history its first Checkpoint holds. A Record that is never stored keeps no Entries. The stores still save a whole Record; appending comes with the store contract.
- **Compaction appends a Checkpoint.** **Breaking:** `Record.ReplaceMessages` and `Record.RecordCompaction` are replaced by `Record.Checkpoint(messages, compaction)`, which does both and adds one Checkpoint to the log.

  ```go
  // before
  r.ReplaceMessages(kept)
  r.RecordCompaction(c)
  // after
  r.Checkpoint(kept, &c)
  ```
- **A Record keeps no text deltas.** The reply they make up is in its Message. **Breaking:** `GET /v1/sessions/{id}/events`, the gRPC events call and `gotato events` do not list text deltas (`message_update` before the rename, `text_delta` now).
- **`Record.Set` with an unchanged value changes nothing.** It no longer moves `UpdatedAt`.
- **Text that imitates Gotato's own tags is escaped.** `ForModel` writes the `<` that starts or ends `<notice>`, `<segment>`, `<tool_calls>` or `<recap>` (any case) as `&lt;` in user and assistant text, Tool Result content and `SafeError`, so a user, outside data or the model's own words cannot speak as the system. Before: a user message `<recap>x</recap>` reached the model unchanged. After: `&lt;recap>x&lt;/recap>`. New in the root package: `EscapeTags`, the tag names, `MetaTag` (a Part with this metadata is Gotato's own tagged text and is left alone) and `TagsSentence`, which `AssembleRequest` adds to the system part only when a request holds a marked Part. Requests without those are byte for byte as before.

### Sessions and stores

- **A writer of a deleted Record is refused by the Record created again.** `Store.Append` took only a position, so a writer that loaded a Record, which was then deleted and created again under the same ID at the same length, wrote into the new Record, and the new Record's Summary named the old epoch. On a file store shared by two processes, a position could be written twice. The Summary that `Append` already takes now carries the writer's epoch (`Summary.RecordEpoch`, read with `Summary.Epoch`), and a store refuses with `ErrNotFound` when the Record's epoch is another. The signature does not change. `MemoryStore` and `FileStore` do it, the file store's head cache now knows the epoch and is trusted only while its file is the one in the directory, with the same modification time and size, because a file system such as ext4 may give a deleted file's inode number to a new one, and `testkit/storetest` adds a check, so an adapter that compares only the length now fails the suite. The file store keeps one window of a few milliseconds, between a writer's check and its link, in which a Record deleted and created again still takes the old writer's batch ([TODO.md](TODO.md)). **Breaking** for a store adapter only:

  | Before | After |
  |---|---|
  | `Append` checks the position, and `ErrNotFound` only for a Record that is gone | `Append` also compares `summary.Epoch()` with the stored Summary's, before the position, and returns `ErrNotFound` when they differ |

- **A store keeps each Session as an append-only log.** `session.Store` writes `session.Entry` batches, each all or nothing and only at positions that are still free, so an adapter maps a write to an insert on a unique (session, position) key. `Load` returns the facts from the last Checkpoint, and `Log` returns every Entry after a position. A `session.Writer` writes a Record: `session.Create` stores a new one, `session.OpenWriter` opens a stored one, and `Writer.Flush` writes what changed since. `session.Open` reads the facts a Run needs, and `session.View` reads everything, Runs and Events included. **Breaking:** an adapter implements the new calls, and code that saved and read Records moves as follows.

  | Before | After |
  |---|---|
  | `store.Save(ctx, r)` for a new Record | `session.Create(ctx, store, r, instance)` |
  | `store.Get(ctx, id)` | `session.View(ctx, store, id)`, or `session.Open` for the facts only |
  | change a Record, then `store.Save(ctx, r)` | `w, err := session.OpenWriter(ctx, store, id, instance)`, change `w.Record()`, then `w.Flush(ctx)` |
  | an adapter's `Save`, `Get`, `List`, `Delete` | `Create`, `Append`, `Load`, `Log`, `List`, `Delete`, plus `Beat` and `Instance` for Liveness rows |
  | `session.NewMemoryStore()`, `session.NewFileStore(dir)` | the same, with an optional `session.WithStoreClock` |
- **A stale copy can no longer overwrite a newer Record.** A Claim (`session.Claim`: an Instance and an Epoch) is itself an Entry. Opening a Record as another Instance claims it with the next Epoch, at the position after the head it read, so a writer that held it before always writes to a taken position and gets `conflict` (`session.ErrConflict`). A Writer stops at its first failed write, and the Record is opened again from the store, which shows whether the write landed. A Run that a stopped writer left running is ended as `failed`, `interrupted`. Two Instances can still run one Session at the same time: the one that writes second fails.
- **`testkit/storetest` checks an adapter against the store contract.** `storetest.Run(t, backing)` runs every check, each on a new backing store, with several handles on one store, as several Instances use it: positions written once, a stale writer refused, a race of eight writers with one winner, a load from the last Checkpoint, deletes, and Liveness rows that expire. The memory and file stores pass it.
- **The file store keeps a directory per Session,** with one file per write, `<id>/<position>.json`. A write is linked into place, so two processes on one directory can never both write a position, and a crash never leaves a torn write. A session file of the earlier format, `<id>.json`, is read as a Checkpoint and moved into its directory by its first write. **Breaking:** an older `gotato` does not read the new layout.
- **The Runner writes as an Instance.** `service.Config.Instance` names it, and is generated when empty; `Runner.Instance` returns it. `Runner.Session` and `Runner.Sessions` read Sessions. `RunRequest.Update` sets metadata as the Run's first write. `CreateSessionExclusive` and `Fork` with an explicit ID rely on the store's `Create`, so they stay exclusive across Instances. `session_saved` sums every write the Run made since the Session was opened, and adds `writes`.
- **New error codes `conflict` and `store_failure`.** HTTP answers 409 and 503, and gRPC `Aborted` and `Unavailable`. A Run whose result another writer fenced answers 409 (gRPC `Aborted`) instead of 500 (`Internal`).
- **`gotato run --no-save` no longer stores settings.** `--instruction` and `--compact-ceiling` given with `--session` and `--no-save` apply to that run only; without `--no-save` they are the run's first write, under its Claim.
- The session document gains `seq`, and a summary gains `seq` and `running`.

- **Every step of a Run is written as it happens.** A Transcript may implement the new `gotato.Journal`, whose `Step` the live Session calls after the prompt, before each model call, for every second or 4 KiB of a streaming reply, after the reply, before each group of Tools runs, after each Tool Result, and after each Steer, FollowUp or continuation. A failed step fails the Run with its code (`conflict` or `store_failure`), and a failed model-call step stops the Run before the model is called. `session.Writer` is a Transcript and a Journal, and the Runner gives it to the live Session, so a crash loses at most the step in flight. A Run makes more writes: about one per step, against one per Run before.
- **Tool Results are committed after each group of Tools runs,** in call order, instead of after every Tool of the Turn has run. **Breaking:** with several Tools in a Turn, a group's `tool_result_committed` Events now come before the next group's `tool_execution_end`.
- **A Session opened after its writer stopped answers every Tool call.** A call that started and has no result gets a failed result whose text says the Tool's outcome is unknown (`session.StoppedToolResult`), and a call that never started one that says it was not run (`session.NotRunToolResult`). They are worked out each time the Session is loaded, and never written. `session.View` also shows the parts of a reply that was streaming when its writer stopped as a cut-off reply.

- **One Instance runs a Session at a time.** A Runner keeps a Liveness row in the store, renewed every third of `service.Config.Liveness.TTL` (10 seconds by default) and timed by the store's clock. A Claim counts only while its Instance's row is alive. Opening a Session that another Instance holds takes it over when that Instance is dead, or alive and idle; when it is alive and using the Session, draining or not, the Run waits up to `Liveness.Wait` (5 seconds by default), and is then refused with `busy` and a `retry_after_ms` detail, which HTTP sends as 409 with `Retry-After`. Before every write, a Runner checks that its own row was renewed within the TTL less a margin, and otherwise writes nothing, so an Instance that was paused or cut off stops before it calls the model. `Runner.Close` removes the row, so another Instance takes its Sessions over at once, and a closed Runner writes nothing more; `gotato`, `gotato serve` and `gotato-grpc` call it when they stop. A Session's summary gains `busy`, set by the Claim from the moment a writer opens it until it releases it (`Writer.Release`). `session.WithGuard` and `session.WithHolderCheck` are the Writer options that do this.

- **Two instances that open one idle Session at once no longer fail half their Runs.** The instance that held the Session reopened it without writing anything, so the store did not mark it busy, and another instance claimed it at the same moment; the holder's first write then failed with `conflict`, 409 over HTTP, before any model call. The retry meant for a lost race never ran either, because the check for a lapsed row matched every conflict. Now the holder writes a busy mark, the new log Entry `session.EntryBusy`, which takes the next position as a Claim does, so exactly one of the two writes lands, and the instance that loses waits for the other's Run. This adds one write to every Run on the instance that already holds the Session, about 8 ms with the file store on macOS.
- **An instance drains in order.** `Runner.Drain` first marks the Runner's Liveness row draining (`InstanceRow.Draining`). Then `Draining` reports true, so `/readyz` answers 503, and the Runner admits no new Run and takes no Session over from another Instance: both fail with `ErrDraining`. Creating or forking a Session still works, since it activates nothing. Every Run in flight finishes, saves and releases its Session. At the drain's deadline the Runs still going are stopped, saved and released, and each returns with the new `RunResult.Reconnect` (`retry_after_ms`, a wait between half a second and a second and a half) once every Run of the drain is released. Before, the row was never marked, and the Runs cut at the deadline came back as plain cancelled Runs. `gotato serve` says `draining` and `stopped` on stderr.
- **An ended Liveness row is removed.** `Store.Beat` with a ttl of 0 removes the row instead of keeping it expired, and `Store.Instance` reads a removed row as not alive, so every Claim that names it ends. The memory and file stores remove it, so `.instances/` no longer keeps a file for every command that ran. `testkit/storetest` adds "an ended row is removed", and fails a store that keeps ended rows or reads a removed row as alive. **Breaking** for a store adapter:

  | Before | After |
  |---|---|
  | `Beat(ctx, row, 0)` ends the row, and a later `Beat` of that ID fails with `ErrExpired` | `Beat(ctx, row, 0)` removes the row, and its ID is unknown again; the Instance never beats again |

- **A ToolSet the model switches on stays on for the Session.** `toolset_activated` is now a fact of the Record, not a log entry: `session.Record.ActiveToolSets()` lists the names in the order they were switched on. A later Run, also on another Instance, starts with those ToolSets active, so its first request carries their Tools. `session_created` and `compaction_ended` gain `tool_sets`, so a Checkpoint keeps them, and `Fork` and `Document` copy them. A name no installed ToolSet carries, a ToolSet that no longer resolves, and a ToolSet past `MaxActiveToolSets` stay off. A ToolSet installed with `WithActiveToolSet` is the Agent's choice and is never switched off by this. A custom `gotato.Transcript` opts in by implementing `gotato.ActiveToolSetSource`. Before, the next Run began with every ToolSet inactive and the model had to activate it again. A Record written before this change restores nothing, since its activations were log entries. Resolving ToolSets while a Session is built can be cancelled: the new `gotato.NewSessionContext(ctx, options...)` takes a context, `NewSession` calls it with `context.Background()` and cannot be cancelled, and the Runner passes the Run's context. A context that ends during the restore fails the build with `cancelled` or `deadline_exceeded`.
- **The Record keeps Segments, written once.** A compaction can close a stretch of history into a `gotato.Segment`: its number, its times, the range of Entries it covers (`SegmentSource`), the Messages that render it, a line per tool call, a Recap and the ToolSets it used. `Record.CloseSegment` appends it as its own `segment_closed` Event, and its Messages are never written again, because a provider's prompt cache reads their bytes. `Record.CheckpointSegments` then appends a `compaction_ended` Checkpoint that lists the Segment numbers and keeps the tail, and does not copy the Segments' Messages. A load reads the latest Checkpoint, resolves its numbers to the stored Messages, and goes on with the tail. The original Entries stay in the log, unread by the Context. `Record.Segments()`, `Tail()`, `NextSegment()` and `Position(id)` (the Entry number and time of each Message since the last Checkpoint) read it back, `Fork` copies the Segments with the parent's Session ID in their `Source`, and `Document` carries them. `Compaction` gains `segment`, `tokens_before`, `tokens_after`, `ceiling`, `recap_usage` and `tool_sets` (`summarizer` names the Recap writer, or `none`), and `CompactionWhy` gains `before_cache_expires` and `overflow`. Nothing closes a Segment yet; `Compact` still replaces the old part with one summary. **Breaking** for a store adapter: `Store.Load` also returns the `segment_closed` Entries from before the last Checkpoint (`Loaded.Segments`), which an adapter finds by `Entry.Kind` as it finds the Checkpoint; `testkit/storetest` checks it.

  | Before | After |
  |---|---|
  | `Loaded{Facts, Head, Summary}` | `Loaded{Facts, Segments, Head, Summary}`; with no Segments, `Segments` is empty and nothing changes |
  | `compaction_ended.messages` and `session_created.messages` hold every Message the Context starts with | they hold the Messages after the Segments the Checkpoint lists in `segments`; a Checkpoint with no `segments` is as before |

  **`gotato.Message` gains `RunID`**, the Run that committed it, so a Segment can group Messages by Run. The Loop sets it on the prompt, each reply (a cut-off one too), each tool result including the closing results of a failed Run, and each Steer, FollowUp and truncation continuation. A Message that did not come from a Run, such as history a Record starts with, has none, and so does any Message in a Record written before this change; both load with an empty `RunID`. It is stored in the Message's JSON as `run_id` (omitted when empty), so entries, Checkpoint tails, Segments, `Fork` and `Document` keep it, and gRPC carries it inside `messages_json`. It is a record only: `ForModel` drops it, and no wire API encodes it, so a request and its `prefix_hash` are the same as before.

  | Before | After |
  |---|---|
  | `{"id":"m1","role":"user",…}` | `{"id":"m1","run_id":"run-…","role":"user",…}`; no `run_id` when empty |

  The gRPC contract gains the typed `Segment`, `SegmentSource`, `ToolCallLine` and `MessagePosition`, the `segment_closed` Event, the new `Compaction` fields, and `segments`, `tail` and `tool_sets` on `session_created` and `compaction_ended` (`tool_sets` was missing there). All are additions.

- **`modelctx.Compact` refuses a Record that holds Segments.** It would have flattened them into one summary and lost what they keep. It returns the new `modelctx.ErrHasSegments`, an `invalid_state` error; the Runner's `Compact` passes it on, and a Run with `AutoCompact` fails with it past the ceiling. No code closes a Segment yet, so nothing changes for a Session today.

  | Before | After |
  |---|---|
  | `Compact` on a Record with Segments replaced them with one summary | `Compact` returns `ErrHasSegments` and leaves the Record as it was; `POST /v1/sessions/{id}/compact` answers 409, gRPC `FailedPrecondition`, `gotato context compact` exits 1 |

- **A Checkpoint lists its Segments once and in order; a Segment can be closed and listed in one step; a Document carries the largest Segment number.** `Record.CheckpointSegments` refuses a number listed twice, a number listed after a larger one, and a number never closed, with `invalid_argument`, and changes nothing. The new `Record.CloseSegmentAndCheckpoint` closes a Segment and writes the Checkpoint that lists it as neighbouring Entries of one batch; `CloseSegment` alone still stores a Segment that no Checkpoint lists, which a load leaves out of the Context. `session.Document` gains `max_segment`, which `Snapshot` fills and `Load` restores (also over gRPC, where the Document is JSON). All are additions except the refusals, which no code reaches yet, since nothing closes a Segment.

  | Before | After |
  |---|---|
  | `CheckpointSegments(…, []int{1, 1}, …)` and `[]int{2, 1}` put a Segment in the Context twice or out of order | both return `invalid_argument` |
  | `Load(Snapshot())` of a Record with an unlisted Segment 2 gave `NextSegment() == 2` again | `NextSegment() == 3`; a Document with no `max_segment` is read from its Segments, as before |

- **A `Document` carries a Segment's Messages twice, for now.** Once a Segment exists, `messages` holds its rendered Messages and `segments` holds them again, so a Document with Segments is larger. Nothing creates Segments yet, so no Document is larger today; this will show when compaction closes Segments, and we revisit it if the size matters.

### Providers

- **Claude and OpenAI are sent only the current Round's reasoning.** A Round is a user Message and every reply and Tool Result after it. Before, every reply's reasoning went back on every request, so a long Session carried all of it, and Claude's newer models kept and billed all of it. Now a recognized Claude or OpenAI provider gets back only the reasoning of the replies after the last user Message, which is what its tool loop needs. A Steer starts a new Round, and a Continue without a new prompt stays in its Round. On Responses, an earlier Round's function call also goes without its item ID. DeepSeek, on its chat API or its Anthropic one, and every provider the gateway does not recognize still get all of it, with no field or header added. The provider is the new `provider` key (`Config.Provider`: `anthropic`, `openai`, `deepseek` or `other`); without it, the gateway recognizes only `api.anthropic.com`, `api.openai.com` and `api.deepseek.com`, never a model name. `gateway.NewFromEnv` reads `GOTATO_GATEWAY_PROVIDER`, and `Client.RoundReasoningOnly` reports the rule. **Behavior change:** a gateway on Anthropic's or OpenAI's own address sends less reasoning, and once per Round the last Round's replies are cached again without it. `provider: other` keeps the old behavior:

  ```yaml
  # before: every reasoning artifact went back, on every request
  api: anthropic-messages
  model: claude-opus-5-5
  # after: the same file sends only the current Round's reasoning; this line restores the old behavior
  provider: other
  ```
- **A recognized provider keeps the prompt cache as long as it offers.** Claude's `cache_control` markers gain `"ttl": "1h"`, and Responses requests to GPT-5.6 and later gain `prompt_cache_options: {"ttl": "30m"}`, which is also their default. OpenAI models before GPT-5.6 and the chat API keep their own default. The new `cache_retention` key (`Config.CacheRetention`) sets it per model, and the `GOTATO_GATEWAY_CACHE_RETENTION` environment variable (`gateway.CacheRetentionEnv`) sets it for every model and wins over each model's own. **Behavior change:** a cache write on Claude now costs twice the input price instead of 1.25 times; `cache_retention: 5m` keeps the five-minute default and its price.
- **Claude drops a thinking block whose prefix changed, instead of refusing the request.** Fable 5.1, Opus 5.5, Sonnet 5.5 and Haiku 5.5 check that every thinking block comes after the same system prompt, tools and Messages it was written under. For these models only, the gateway sends `thinking: {"type": "adaptive", "block_binding": {"prefix_mismatch_behavior": "drop_block"}}`, with the beta header `thinking-binding-controls-2026-08-01` joined to any configured `anthropic-beta`. `adaptive` is their default, so nothing else changes. `no_drop_block: true` (`Config.NoDropBlock`) turns it off.
- **On Claude, the cache is also marked where the last Round started,** so a request that leaves the last Round's reasoning out still reads the cache up to there.
- **Compaction keeps the kept Messages without their reasoning, for Claude and OpenAI.** The summary changes the prefix that reasoning was written under, so a Continue after a compaction could be refused by Claude with a 400. `modelctx.CompactOptions` and `CompactPolicy` gain `DropReasoning`, and `modelctx.DropsReasoning` asks a Model through the new `modelctx.RoundReasoner`; the Runner sets it for `Runner.Compact` and automatic compaction. DeepSeek and unrecognized providers keep the reasoning.
- **The chat gateway keeps a tool call's `extra_content` and sends it back.** Gemini 3 and later put the thought signature in `tool_calls[].extra_content.google.thought_signature` on their chat API, and refuse a call of the current Round that comes back without it, so a Run's second tool call could fail with a 400. The chat gateway now reads `extra_content` from any delta of a streamed call, keeps it in `ToolCall.Signature`, and sends it back on that call. Another wire API's artifact in `ToolCall.Signature` is never sent as `extra_content`, and `extra_content` is never sent as a Responses item ID. Gemini stays an unrecognized provider, so all its reasoning goes back. Not yet run against Gemini.
- **The chat gateway keeps parallel tool calls apart without an index.** Gemini's chat API streams parallel calls whole, each with its own ID, but without an index, with a null one, or all at index 0. The gateway merged them by index into one call, with the last call's ID, every name and every argument string, which is not JSON, so the Run sent the model a failed tool result instead of running the tools. A delta that names another call ID than the one its index holds now starts a new call, and a delta without an index adds to the last call. Calls come out in the order they first appeared, which for a provider that numbers them is their index order.
- **A Responses tool call keeps the provider's item ID out of its call ID.** The Responses adapter packed the provider's item ID into `ToolCall.ID` as `call_…|fc_…`, so a provider-private ID was stored in the Session and reached `Event.ToolCallID`. `ToolCall` now has `Signature`, an opaque provider artifact that the adapter sets and reads back on replay, like `ContentPart.Signature`. `ToolCall.ID` is the call ID alone. A Session saved before still replays: the adapter reads a packed call ID as it did.
- **The chat gateway sends `{}` for a tool call with empty arguments.** A provider may stream a call to a tool that takes no arguments with `arguments: ""`. The Loop accepts only JSON, so the call failed as invalid arguments and the tool could never run. The chat gateway now sends `{}` in its place, as the Responses and Anthropic Messages gateways already did.
- **A reply records the model that answered it.** Behind a gateway that falls back to another model, the model that answers can differ from the one requested, and nothing said so. `Message.Model` now holds the model the provider reported, read from `model` in each chat chunk, from Responses' `response.created` and `response.completed`, and from Anthropic's `message_start`. A custom `gotato.Model` reports it in the new `ModelEvent.Model`. It is empty when the provider names none, and `ForModel` drops it, so it never reaches a request. Stored Messages and the HTTP and gRPC session documents gain an optional `model`.
- **A request the provider refuses as too long ends the Run with `context_overflow`.** The gateway marks a 400 or 413 whose error body says the request is longer than the context window (`gateway.Error.Overflow`, `ContextOverflow()`), on all three wire APIs and in the error events of the Responses and Messages streams. The Loop reads any Model error with a `ContextOverflow() bool` method, so a custom `gotato.Model` can say the same. The new code is `gotato.ErrContextOverflow`; gRPC maps it to `RESOURCE_EXHAUSTED`. The phrases it looks for are in `gateway/overflow.go`, and have not yet been checked against live providers. **Breaking:** a Run that overflows fails with another cause.

  ```text
  before  run_ended {"status":"failed","cause":"model_failure"}
  after   run_ended {"status":"failed","cause":"context_overflow"}
  ```

  A Claude reply that stops with `model_context_window_exceeded` was read as a finished one. It now fails the Run with `context_overflow` too, keeps the text received as a cut-off reply, and is never continued as an output-limit stop (`MaxTruncatedContinuations` does not apply), since a continuation would ask the model again on a full window. HTTP maps an error with this code to `422` where it is returned as a call's error; a Run that fails with it is still a `200` with the code in `result.error`.
- **A model's context window and cache retention are known to the gateway.** The new `context_window` key (`Config.ContextWindow`, in tokens; 0 is unknown, and gotato keeps no table of windows) is read back by `Client.ContextWindow()`. `Client.CacheRetention()` says how long the provider keeps the prompt cache as the client asks for it: 1 hour or 5 minutes on Claude's Messages API, 30 minutes on Responses when it sends a ttl, and 0 (unknown) elsewhere. `modelctx.WindowOf` and `modelctx.RetentionOf` read both from any `gotato.Model` that has the methods.

### Events

The Event design of [DESIGNS.md → Every step is an Event, and a stored Event is an Entry](DESIGNS.md#every-step-is-an-event-and-a-stored-event-is-an-entry--2026-10-18) is built, against v0.1.2. It is one break. It shares the contract version change that this release already holds (HTTP and gRPC `"4"`, CLI `"2"`); the versions are not raised a second time.

- **Each kind of Event is a Go type, and a stored Event is an Entry of the Record.** `gotato.Event` is an interface with one type for each kind, such as `gotato.RunStarted`, and every type embeds a `gotato.Header`: `Time`, `SessionID`, `Number`, `RunID`, `Turn`. The number is the Entry's position in the Session's Record, so it counts the whole Record, and every instance reads the same number. A stored Event reaches subscribers only after it is written; deltas (`text_delta`, `reasoning_delta`, `tool_progress`) are sent at once, with the number of the stored Event they follow. A Session with no store numbers its Events in memory. **Breaking:**

  | Before | After |
  |---|---|
  | `gotato.Event{Kind, Class, Sequence, Turn, MessageID, ToolCallID, Payload map[string]any, Timestamp}` | `gotato.Event` interface; `event.Kind()`, `event.Head()`; one struct per kind with checked fields |
  | `Sequence`, counted within one Run | `Number`, the position in the Record |
  | `Timestamp`, `event_class`, `EventClass` with `EventProtected` and `EventCoalescable` | `Time`; `Kind().Class()` is `ClassStored`, `ClassDelta` or `ClassInstance` |
  | `EventKind` constants `EventRunStart`, `EventMessageUpdate`, … | `KindRunStarted`, `KindTextDelta`, … |

  ```go
  // before
  for event := range events {
      if event.Kind == gotato.EventRunEnd {
          fmt.Println(event.Payload["status"], event.Sequence)
      }
  }
  // after
  for {
      event, err := sub.Next(ctx)
      if err != nil { break }
      if ended, ok := event.(gotato.RunEnded); ok {
          fmt.Println(ended.Status, ended.Number)
      }
  }
  ```

- **Names say what happened, in the glossary's words.** **Breaking:** the kinds are renamed. New kinds are listed in [FEATURES.md → G-F13](FEATURES.md#g-f13--events-).

  | Before | After |
  |---|---|
  | `run_start`, `run_end` | `run_started`, `run_ended` (with `cause`, `turns`, `tool_calls`, `usage`) |
  | `turn_start`, `turn_end` | `turn_started`, `turn_ended` |
  | `message_start`, `message_end` of a reply | `reply_started`, `reply_ended`; a cut-off reply is `reply_cut_off` |
  | `message_start`, `message_end` of a user Message | `message_committed`, with `source`: `prompt`, `steer`, `follow_up` or `continuation` |
  | `message_update` | `text_delta`; the reasoning is now streamed too, as `reasoning_delta` |
  | `tool_execution_start`, `tool_execution_end` | `tool_started`, `tool_ended`; a blocked call has only a `tool_ended` |
  | `tool_execution_update` | `tool_progress` |
  | `tool_result_committed`, `toolset_activated`, `context_built` | the same kinds, with typed fields |
  | `session_compacted` | `compaction_ended`, after a `compaction_started`, or a `compaction_failed` |

- **Every Entry is an Event, and the `events` log Entry is gone.** The Record no longer keeps a copy of the Events, so facts and log entries are said once. **Breaking** for a store adapter and for code that reads Entries: an Entry's `Kind` is the Event's kind, such as `message_committed`, and its `Data` is the Event as JSON. A Checkpoint is a `session_created` or `compaction_ended` Entry (`Entry.IsCheckpoint`), where it was a `checkpoint` Entry, so a load starts at the last of those; `testkit/storetest` checks it. A Record written before is still read: its Entries keep their old kinds, new Events are appended after them, and `session.StoredEvents` maps them onto Events (a legacy Entry that has no Event, such as a log snapshot, shows as a gap in the numbers).

  | Before | After |
  |---|---|
  | `session.EntryCheckpoint`, `EntryMessage`, `EntryRunStart`, `EntryEvents`, … | `gotato.KindSessionCreated`, `KindMessageCommitted`, `KindRunStarted`, …; there is no `events` Entry |
  | `session.Recorder`, `session.NewRecorder(r)` | none: `gotato.WithTranscript(r)` is enough, because a `session.Record` is a `gotato.Journal` |
  | `Record.RecordEvent`, `RecordRunStart`, `RecordRunEnd` | none: the Run's Events carry them |
  | `Record.Checkpoint(messages, compaction)` | `Record.Checkpoint(why, messages, compaction)`; `Record.Emit(event)` queues another stored Event |
  | `gotato.Journal.Step(ctx, Step)`, `gotato.Step`, `StepKind` | `gotato.Journal.Write(ctx, []Event) ([]Event, error)` |
  | `session.LogSnapshot`, `session.Checkpoint`, `session.ToolsStarted`, `session.ModelCall`, `session.ReplyPart`, `session.MetadataChange`, and the `session.Entry*` constants | removed; `session.EntryKind` is `gotato.EventKind`, and `session.Compaction` is `gotato.Compaction` |
  | `session.Document.Events []gotato.Event` (a struct with a payload) | the same field, now typed Events; the last 1000 stored Events of the Record |

- **Subscriptions replace observers, and a Session never waits for one.** A Run's stream, a Session's stream from after any number, and an Instance's stream. Each subscriber has one goroutine and its own queue; one that falls too far behind is no longer buffered and catches up from the Record by number, so only deltas can be skipped; an Instance's stream says how many Events it skipped with an `events_skipped` Event. **Breaking:**

  | Before | After |
  |---|---|
  | `gotato.EventObserver`, installed with `WithExtension`, awaited before the Loop went on | removed; `s.Subscribe(ctx)`, `s.SubscribeRun(ctx)`, `s.SubscribeFrom(ctx, after)` |
  | `Session.Subscribe(ctx) (EventStream, error)`, `EventStream` | `Session.Subscribe(ctx, opts...) (gotato.Subscription, error)` |
  | `Session.SubscribeLifecycle`, `LifecycleEvent`, `LifecycleStream` | removed; `session_created` and `session_unloaded` |
  | an extension that failed with `Advisory() == true` was skipped silently | the failure is stored as `extension_failed` |
  | `Runner.StreamRun(ctx, request, sink)` called the sink on the Run's goroutine | the same signature, but the Run runs on its own goroutine and the sink is called from the caller's, reading a Subscription, so a slow sink never slows the Run |
  | `session_loaded`, `session_saved` in a Run's stream | Instance Events: `Runner.SubscribeInstance` |
  | `testkit.NewEventRecorder()` with `WithExtension` | `testkit.RecordEvents(session)` right after `NewSession`, or `testkit.RecordSubscription(sub)` |

  ```go
  // before
  recorder := testkit.NewEventRecorder()
  live, _ := gotato.NewSession(gotato.WithModel(model), gotato.WithExtension(recorder))
  // after
  live, _ := gotato.NewSession(gotato.WithModel(model))
  recorder := testkit.RecordEvents(live)
  ```

- **A new Record is busy until it is released.** **Breaking** for code that calls `session.Create` itself: the Record it stores is marked busy, as one that `OpenWriter` opens is, and `Writer.Release` writes the `session_released` that ends it. Before, `Create` left the Record idle, so another Instance could claim a Session whose first Run was still going, and the first Run of a new Session never wrote a release. `session.WithIdle()` is new, for a Record that nothing runs against: `Create` then writes the release in the same write as the first Claim, and `Runner.CreateSession` and `Runner.Fork` use it, so a Session that was only created is idle at once.

  ```go
  // before: idle after Create
  writer, _ := session.Create(ctx, store, record, "instance-1")
  // after: busy until writer.Release(ctx); or, when nothing runs against it:
  writer, _ := session.Create(ctx, store, record, "instance-1", session.WithIdle())
  ```

- **Streams, loads and unsaved Runs keep to the Record.** A Session's stream reads from the Record any stored Event that another Instance wrote, where it skipped it; `compaction_ended` carries the Claim, so a load that starts at a compaction still knows the Writer and its Epoch; a load of a Session whose last Run never ended rebuilds the Cut-off reply from the parts kept, and stores it as a `reply_cut_off`; a `SubscribeFrom` over a Record that is not stored fails with `gotato.ErrEventsLost` where it started in the middle of the log; and a Run with `SkipSave` numbers its Events from 1 on its own stream, since it stores none.
- **Two names moved to make room for the Events.** **Breaking:** the Session's status constants `gotato.SessionCreated`, `SessionIdle`, `SessionBusy`, `SessionClosing` and `SessionClosed` are now `SessionStatusCreated` … `SessionStatusClosed`, because `SessionCreated` is the Event; and the function type a Tool reports progress with, `gotato.ToolProgress`, is now `gotato.ToolProgressFunc`, because `ToolProgress` is the Event.
- **New Events.** `session_claimed` (with why, and which instance held the Session before), `session_released`, `session_fenced` and `liveness_lapsed` come with the Claim; `message_queued`, `message_committed`, `message_dropped` and `message_rejected`, and `abort_requested`, come from a live Session's Steer, FollowUp and Abort; `model_retried` comes from the gateway; `reply_part_kept` is the stored part of a streaming reply; `session_created`, `metadata_changed` and `compaction_*` come from the Record; `run_requested`, `run_waiting`, `run_rejected`, `session_loaded`, `session_saved`, `session_deleted` and `store_write_failed` are Instance Events. An Instance that opens a Session it holds already writes `session_claimed` again, with the same Epoch, where it wrote a busy mark.
- **New in the Go API:** `gotato.Feed`, `gotato.Subscription`, `gotato.WithFeed`, `gotato.WithRunStart`, `gotato.MarshalEvent`, `gotato.UnmarshalEvent`, `gotato.ReportModelRetry` (for a gateway), `session.StoredEvents`, `session.WithPublisher`, `session.WithWaited`, `service.Runner.SubscribeSession`, `SubscribeInstance` and `service.ForAgents`.
- **HTTP.** Every Event, in `…/runs/stream` and in `GET …/events`, is one flat object with `kind`, `time`, `session_id` and `number`, and the fields of its kind at the same level, where it had `payload`. The SSE `event:` name is the new kind. **Breaking**, within the version change above:

  ```json
  // before
  {"session_id":"s1","run_id":"r1","sequence":9,"kind":"run_end","event_class":"protected","payload":{"status":"completed"},"timestamp":"…"}
  // after
  {"kind":"run_ended","time":"…","session_id":"s1","number":19,"run_id":"r1","status":"completed","turns":2,"tool_calls":1,"usage":{…},"elapsed_ms":8}
  ```

  A Run's stream starts with the Event that claimed, or created, its Session and ends with `run_ended`. `GET …/events` lists every stored Event of the Session, read from its Record, messages included; it listed the retained window of protected Events, 1000 at most, before.
- **gRPC.** `Event` is an `EventHeader` and a `oneof event` with one message for each type; `payload_json`, `event_class`, `sequence`, `kind` and `message_id` are gone. A Message that an Event carries travels as JSON bytes (`message_json`). `Usage` adds `cache_read_tokens` and `cache_write_tokens`. **Breaking**, within the version change above. `adapter/grpc` has `KindOf(event)` for the kind of an Event on the wire. The generated code is regenerated with `buf generate`.
- **CLI.** `events` and `run --events jsonl` print the flat objects above, where they printed `payload`; the Run's stream begins with `session_created` and `session_claimed` for a new session, and has no `session_loaded` or `session_saved`. **Breaking**, within the version change above.

### Many instances

These are additions, within the version change that the unreleased release already holds, but for one break: the streaming routes answer a refusal at admission with a status, before the stream starts.

- **An instance refuses a new Session past a hard limit.** `service.Admission.MaxActiveSessions` (`gotato serve --max-sessions`, `gotato-grpc --max-sessions`; off by default) counts the Sessions an instance has active. A Run that would activate another one past it is refused before anything is loaded, claimed or created: `service.ErrOverloaded` (the new code `gotato.ErrOverloaded`, `overloaded`), and a `run_rejected` Instance Event with the reason `activation_limit`. HTTP answers 503 with the header `Gotato-Not-Run: true` and `"not_run": true` in the body, also for the streaming routes, which answer it before the stream starts (`Runner.StreamRunAdmitted`); gRPC answers `UNAVAILABLE` with the trailer `gotato-not-run: true`. The mark tells it from the 503 of a store failure, which may have run. A Run on a Session that is active already, and creating or forking a Session, are not refused. `Runner.ActiveSessions` reports the count, and `/readyz` adds `active_sessions`.
- **The streaming routes answer a refusal at admission with a status.** `POST /v1/runs/stream` and `POST /v1/sessions/{id}/runs/stream` know whether a Run is admitted before they write a byte, so a refused Run gets the status and body that `/v1/runs` gives it, and no stream: draining 409, capacity (`MaxActiveRuns`) 429, and the new activation limit 503 marked not run. Every failure after admission still arrives in-band as `event: error`. No status code or mark of `/v1/runs` changed, and gRPC already answered with a status. **Breaking** for a client of the streaming routes that read these refusals from the stream:

  ```text
  before  POST /v1/runs/stream, the instance draining   →  200 text/event-stream
          event: error
          data: {"error":"Conflict","code":"invalid_state","message":"invalid_state: service: draining"}
  after   POST /v1/runs/stream, the instance draining   →  409 application/json
          {"error":"Conflict","code":"invalid_state","message":"invalid_state: service: draining"}
  ```

  At capacity it is the same, with 429 and `"code":"limit_exceeded"`. Read the status before you read the stream.
- **The endpoints read the `session-id` header.** On HTTP the header, and on gRPC the metadata `session-id`, must agree with the Session ID in the path or the body, or the call fails with 400 or `INVALID_ARGUMENT`; a missing one agrees with everything. `POST /v1/sessions`, `POST /v1/runs`, a fork, and the gRPC `CreateSession`, `Run` and `ForkSession` answer with the new Session's ID in a header of the same name, for the client to send next time. A load balancer hashes on it.
- **A Session's stream resumes on any instance.** `GET /v1/sessions/{id}/stream` and the gRPC `StreamSession` send a Session's stored Events after a number, from the Record, and then the live ones. The number is the standard `Last-Event-ID` header or the `after` parameter. Every stored Event on that stream, and on the Run streams, has its number as the SSE `id:`. `Runner.StreamSession` opens it: it checks the Record's epoch and the number, and follows the Record when another instance writes it (`Config.StreamPoll`, one second by default; `gotato.SubscribeTail`). A position that cannot be resumed answers 409 `cannot_resume` (gRPC `FAILED_PRECONDITION`) with the current epoch. A stream follows the Record it opened, and never delivers an Event of a Record created again under the same ID. When its Session is deleted, or deleted and created again, it ends with a not-found error (HTTP `event: error` with `"error":"Not Found"` and no `code`; gRPC `NOT_FOUND`) or with `cannot_resume` (gRPC `FAILED_PRECONDITION`), and drops what was queued: at once on the instance that deletes the Session, or opens, creates or streams the new Record, and elsewhere at the first look that finds the Session gone or the new Record's Events, so an old stream elsewhere stays open, idle, while the new Record is no longer than what it has seen. An instance keeps a Feed per Record, named by Session ID and epoch, so a stream of the new Record never waits for the old one's numbers. `Runner.SubscribeSession` is the same stream: it now fails at once for an unknown Session or a number past the Record's end, and ends with `io.EOF` when the instance's drain is over. An idle stream costs one `Log` per look, and a `Load` only after a look that returned Events.
- **A Record has an epoch.** `record_epoch` is chosen when the Record is created and changes only if it is deleted and created again under the same ID. It is in `session_created`, in every Session summary and document, in a Run's result, in the first frame of a Session's stream, and in the gRPC header metadata `record-epoch`. A Record written before has none of its own, and every surface names the same one, made of its creation time (`session.Summary.Epoch`, `Record.RecordEpoch`). `gotato run --json` adds `record_epoch`. `testkit/storetest` adds a check that a store keeps `Summary.RecordEpoch`; an adapter that drops it fails the suite.
- **A stream that ends says whether to reconnect, and when.** `service.NewReconnect` gives a wait of half a second to one and a half, spread at random, with the reason (`why`: `draining`); the drain uses it for a Run's stream and for a Session's stream, which it ends once every Run is saved and released. A Session's first frame also sets the SSE `retry:` wait, a different one for each client. An idle stream sends a keepalive comment every 15 seconds (`httpapi.WithKeepAlive`).
- **The multi-instance harness has a hashing proxy.** `service/workload` runs several instances over one store behind a proxy that hashes on `session-id`, with requests sent through it and straight to random instances, concurrent messages and an instance killed. It expects the same Transcripts and Event order as one instance, and a stream cut at every Event, over HTTP and over gRPC, to resume equal to an uncut one.
- **An open or a create no longer cuts the stream of a newer Record.** When the Record was deleted and created again on another instance just after this instance wrote its claim, and a client had already streamed the new Record here, the stream ended with `cannot_resume`. A stream is now cut only when it is shown to be of a replaced Record. No field or code changes.

### Contracts

- HTTP: the contract version is now `"4"`. Every Event in a stream or in `GET …/events` has `session_id` where it had `agent_id`, and is the flat object that Events describes. `GET /v1/agents` adds `max_effect`, an object from Agent name to cap; an Agent without a cap has no entry. A stream whose Run the drain stopped ends with `event: result` and then `event: reconnect`, which carries an SSE `retry:` field and `{"retry_after_ms": N, "why": "draining"}`; a run result adds `reconnect` and `record_epoch`. The streaming routes `POST /v1/runs/stream` and `POST /v1/sessions/{id}/runs/stream` answer a refusal at admission with its status before the stream starts: draining 409 and capacity 429, where they answered 200 and an in-band `event: error` (**Breaking**, see Many instances for the before and after), and the new activation limit 503. The other additions of Many instances are not a break, so the version stays: the route `GET /v1/sessions/{id}/stream`, the `id:` of stored Events, the `session-id` header, `Gotato-Not-Run`, 503 `overloaded`, 409 `cannot_resume`, `record_epoch`, and `active_sessions` on `/readyz`.
- gRPC: the contract version is now `"4"`. `Event` is now an `EventHeader` and a `oneof`, as Events describes. A `StreamRun` whose Run the drain stopped sends its result, then ends with `UNAVAILABLE` and a `retry-after-ms` trailer, where it ended with `OK` before. `AgentsResponse` adds `map<string, string> max_effect = 2`, with the same meaning as on HTTP. The generated code also picks up comments that `service.proto` had gained since the last generation. The additions of Many instances are not a break, so the version stays: the method `StreamSession` with `StreamSessionRequest`, `StreamStart` and `SessionUpdate`, `record_epoch` on `SessionSummary`, `RunResult` and `SessionCreated`, the `session-id` metadata, and the trailer `gotato-not-run`.
- CLI: the contract version that `doctor` reports is now `"2"`, because `events` and `run --events jsonl` print the flat Events that Events describes, with `session_id`. `tools list --json` and `tools active --json` add `max_effect` when the agent has a cap, and `tools` is now `[]` rather than `null` when no tool is listed.

## v0.1.2 — 2026-10-16

Phase 1 of [ROADMAP.md](ROADMAP.md), Measure: the reference workload, the live run, and a number for every benchmark in [GOALS.md](GOALS.md) but G-B13. The HTTP and gRPC contract version is now `"3"`.

### Runtime

- **The panel is gone.** It re-rendered small content that changes every Turn, such as the time, onto the last message of every request. Such content is never cached, so it cost its full price on every Turn, and after a tool call it was appended to the tool's output. A request is now the system prompt, the tools and the history. A fact the model needs now, such as the current time, comes from a tool it calls. A Session that still carries `gotato.panel` in its metadata runs as before, without a panel. **Breaking:**
  - Go: `ModelContext.Panel`, `ModelContext.RenderPanel`, `gotato.PanelTag`, `modelctx.WithPanel`, `modelctx.PanelFunc`, `modelctx.Time`, `modelctx.Report.PanelBytes`, `service.MetaPanel`, `service.PanelFromSpec`, `service.InspectOptions.Panel` and `service.Config.Now`, which only the time panel read, are removed.
  - CLI: `--panel` is gone from `run`, `session create` and `context inspect` (exit 2 if given).
  - HTTP and gRPC: the contract version is now `"3"`. The context report (`GET /v1/sessions/{id}/context`, the gRPC `report_json`) has no `panel_bytes`, and the `context_built` payload has no `panel_bytes`.

  ```go
  // before: the time on every request
  builder := modelctx.WithPanel(modelctx.FullHistory(), func(context.Context, gotato.ContextSnapshot) ([]gotato.Block, error) {
      return []gotato.Block{modelctx.Time(time.Now())}, nil
  })
  // after: a tool the model calls when it needs the time
  clock, _ := gotato.NewFuncTool("clock.now", "Returns the current time in RFC 3339 UTC.",
      func(context.Context, struct{}) (string, error) { return time.Now().UTC().Format(time.RFC3339), nil })
  ```
- **A bad Tool call no longer ends the Run.** A call that names an unknown Tool, or carries arguments that are not valid JSON or fail the Tool's schema, settles as a failed Tool Result that never ran. Its text names the problem and, for an unknown Tool, lists the Tools the model was shown, so the model can correct the call on its next Turn. On that Turn, every gateway sends the call back with `{}` in place of arguments that are not a JSON object, so the provider accepts the request. `MaxTurns` and `MaxToolCalls` still bound a model that keeps getting it wrong. **Breaking:** `Prompt` no longer returns `ErrToolResolutionFailure` or `ErrToolArgumentFailure` for these calls. Read the Tool Result instead: `Status == ToolResultFailed`, `Executed == false`, and a `SafeError` that starts with the error code.
- **A streamed reply is stored as its blocks, not its fragments.** The Agent kept one `ContentPart` per streamed text or reasoning delta, so a reply was saved and replayed as hundreds of fragments. It now merges contiguous text deltas, and contiguous reasoning deltas, into one Part each; a reasoning block ends when its signature arrives. `message_update` Events still carry each text delta. The Responses gateway also joins adjacent text Parts of Sessions saved before this change.
- **A reasoning block that streams no text keeps its own Part.** A `ModelReasoningDone` signed the last reasoning Part even when an earlier block had already signed it. So an Anthropic `redacted_thinking` block, a thinking block with its display omitted, or a Responses reasoning item without a summary overwrote the block before it, and that block was missing when the Session was replayed. Each such block is now stored as a reasoning Part of its own, with an empty `Text` and its artifact in `Signature`.
- **`TextOf` returns text only; `ReasoningOf` returns reasoning.** `gotato.TextOf` used to join text and reasoning Parts, so a model's thinking leaked into final answers (`FinalText`), compaction summaries, CLI output and the `content` the chat gateway sends back. **Breaking:** code that wanted the thinking must call `gotato.ReasoningOf`.

  ```go
  // before: text and reasoning together
  all := gotato.TextOf(message)
  // after
  answer := gotato.TextOf(message)
  thinking := gotato.ReasoningOf(message)
  ```
- **A Turn that the output limit cut off can continue.** With `CoreLimits.MaxTruncatedContinuations` above 0, a Turn that stopped at the output limit before any Tool call goes on with `TruncatedContinuationText`, up to that many times per Run, instead of settling the Run on the cut-off reply. Off by default.

- **Context inspection shows what the next Turn sends.** `Runner.Inspect`, and with it `gotato context inspect` and `build`, `GET /v1/sessions/{id}/context` and the gRPC `Context` method, now builds the Agent a Run would build and asks it. The request it reports includes the Agent's `ContextTransformer` and `MessageConverter` extensions, its tools and its model options (`request.options`), so its `prefix_hash` equals the one the next Turn's `context_built` Event reports. The report gains `compaction_pending` and `compact_ceiling`. In Go, the core Agent implements `ContextInspector` (`InspectContext`, which fails with `ErrBusy` during a Run), and `modelctx.ReportOf` turns its `ContextInspection` into a report; `AutoCompactor` gains `Pending` and `Ceiling`. Inspection runs the context extensions and the tool sources, so they must not have side effects. **Breaking:** `RuntimeAgent` now includes `ContextInspector`, so a type of your own that implements `RuntimeAgent` must add `InspectContext`.

- **The Runner times the store with Events.** It emits `session_loaded` before `agent_start` and `session_saved` after `agent_end`, for each load and save a Run makes, with `session_id`, `messages` and the store's time in `elapsed_ms` (fractional milliseconds). A one-shot Run that creates its Session also emits a `session_saved` before `agent_start`. They reach the `StreamRun` sink, the HTTP and gRPC streams, `gotato run --events jsonl` and the `EventObserver`s in `AgentSpec.Extensions`, and are not recorded in the Session. A failed load or save is marked `failed: true`, without the store's error text. A stream's first Event is now one of these, not `agent_start`; consumers already have to tolerate unknown kinds.
- **A Run loads its Session once.** The Runner loaded an existing Session before taking its lock and again after, so every Run read the store twice. It now reads it once, under the lock. A Run on a Session that does not exist still fails with `session.ErrNotFound`, now after the lock is taken.
- **Every request carries the Session's prompt cache key.** `ModelRequest.CacheKey` groups the requests that share a cacheable prefix; like the cache breakpoints, it never changes the prompt or the prefix hash. `gotato.WithCacheKey` sets it on an Agent, and the Runner gives each Session its own, `service.CacheKey(sessionID)`, a hash of the ID so a caller's ID never reaches the provider. A Spec's `Options` may set another. Context inspection shows it as `request.cache_key`. `Runner.Fork` gives the child its parent's key in `gotato.cache_key` (`service.MetaCacheKey`), so a fork reads its parent's cached prefix, and the Runner sends that key for any Session that has one.
- **The message cache breakpoint is on the last message.** `AssembleRequest` marked the message before the last one, so on Anthropic each Turn left its newest message out of the cache. The marker is now on the last message, a one-message request included, so the whole request is cached for the next Turn. **Breaking** only for code that read `CacheBreakpoints` and expected `Index` to be `len(Messages)-2`.

### Measurement

- **The reference workload has an agentic task.** `testkit.SupportCaseTask` (`support_case`) is one Run of five Turns of the model's own choosing: it lists a customer's orders, reads the most recent one and the refund policy, refunds the damaged item with `orders.refund` (Effect `write_external`), and reports the refund ID. Its instruction is a support playbook long enough for a provider's prompt cache, and an older order with the same item is past the refund window, so a wrong choice fails and must be corrected. `ReferenceTasks` now returns seven tasks.
- **Startup and footprint are measured.** `bench/refservice` is the smallest service a team would write around Gotato, in a Go module of its own and checked in CI. `BenchmarkStartup` there times it from process start to the first 200 from `/healthz` (GOALS.md G-B10), and `footprint.sh` prints its modules and stripped binary size as JSON (G-B12).
- **The store's time and the time to the first token come from Events.** `service/workload` reads the store's time from `session_loaded` and `session_saved` instead of a timer around the store, and reports `before_request_per_run_ms` (from a Run's start to its first request) next to `model_first_token_ms` (the model's own time to its first text token), for G-B06.
- **The reference workload and the live run.** `testkit.ReferenceTasks` returns the tasks GOALS.md measures, each with a fake model and a check of its answers: a lookup, a 10-prompt conversation, a tool error and its recovery, and lookups in Sessions of 10, 100 and 1,000 messages. `service/workload` plays them through a Runner, one at a time or under load, and reports failures by cause, token usage and cost, the cache hit rate, Gotato's time per Turn next to the model's, the time to load a Session, and whether each Turn's Context matches its inspection. `gotato-workload` prints that report as JSON, and with `-live` plays the tasks against the provider in your gateway configuration, at the price you pass with `-price`.

### Providers

- **The gateway sends the prompt cache key.** `cache_key_field` names the body field that carries `ModelRequest.CacheKey`, and `cache_key_header` the header (`Config.CacheKeyField`, `Config.CacheKeyHeader`). On `openai-responses` the field defaults to `prompt_cache_key`; set `cache_key_field: none` to send none. Chat completions send no field unless you name one, and `anthropic-messages` refuses a field, because it caches by `cache_control`. A provider that keys its cache by a session header needs `cache_key_header`, such as `session-id`: on such an endpoint, the reference workload's `history_100` went from no cached input to 93% from the second Turn on, over five plays. A restarted stream keeps the header. Requests to `openai-responses` now carry `prompt_cache_key` whenever the Agent has a cache key, which every Run through a Runner has.
- **Tool names on the wire stay readable.** Every adapter sends a Tool ID that providers accept (`^[a-zA-Z0-9_-]{1,64}$`) unchanged, and replaces each other character with `_`, so `demo.echo` goes out as `demo_echo`. When two IDs end up with the same name, an ID that providers accept keeps it, and the other gets a readable prefix plus a short hash; so does an ID longer than 64 characters. Unsafe IDs used to go out as `gotato_` plus their base64 encoding, which models copied back wrong. Mapping a name back to its Tool ID is unchanged.
- **The chat, Responses and Anthropic Messages gateways restart a stream that breaks before any output.** A stream that broke after its headers (a connection reset, or a close before `[DONE]`, `response.completed` or `message_stop`) used to fail with a raw `unexpected EOF`, or as `model_protocol_failure`. While nothing has reached the caller, the gateway now sends the request again, within `max_retries`. Once output was delivered, it returns a `*gateway.Error` with `Retryable: true` and a message that contains `stream interrupted:`, which the Loop reports as `model_failure`. A chat stream that ends after a `finish_reason` without `[DONE]` now completes. **Breaking:** code that matched `model_protocol_failure` for a cut stream must match `model_failure`, and can read the cause with `errors.As(err, &gatewayErr)`.
- **The chat gateway keeps and sends back `reasoning_content`.** It dropped `delta.reasoning_content`, so a thinking model that requires it back failed mid-Run (DeepSeek answers HTTP 400). Reasoning deltas are now stored as reasoning Parts, and an assistant message's reasoning goes back as `reasoning_content`, left out when empty.
- **Per-request headers.** `gateway.WithRequestHeaders(ctx, headers)` adds headers to the requests of one Run, for a provider that routes each conversation by a header. Configured and per-request headers can set `User-Agent` (default `gotato/0.1`) but never the authentication, content-type or streaming headers.
- **The chat gateway counts cache reads.** `Usage.CacheReadTokens` is filled from `prompt_tokens_details.cached_tokens` and DeepSeek's `prompt_cache_hit_tokens`, as the Responses and Anthropic adapters already did.

## v0.1.1 — 2026-10-15

### Runtime

- **A reply that the output limit cut off keeps `StopMaxTokens`, even when it holds Tool calls.** The Tools that finished still run, and a `TurnStopper` sees the cut-off in `TurnSnapshot.Assistant.StopReason`. `TurnSnapshot.StopReason` stays the Turn's own reason, which is `StopToolCalls` once Tools ran. **Breaking:** code that read `Message.StopReason == StopToolCalls` to find Tool calls must read `len(Message.ToolCalls) > 0` instead.

### Providers

- **The Responses gateway no longer delivers a Tool call that the output limit cut short,** because its arguments are incomplete. It used to deliver the call with those arguments and report `StopToolCalls`.

## v0.1.0 — 2026-10-11

The first tagged release. The notes below list the breaking changes from the development builds before it.

### Tool Registry

- **`toolregistry.New` returns an error.** `toolregistry.New(tools...)` now returns `(*Registry, error)` and fails on the first invalid or duplicate Tool. It used to drop that Tool silently. Call `New` and check the error, or use `MustNew(tools...)` for Tools known to be valid.

  ```go
  // before
  reg := toolregistry.New(fsRead, shell)
  // after
  reg, err := toolregistry.New(fsRead, shell)
  if err != nil { /* ... */ }
  // or, when the Tools are already validated
  reg := toolregistry.MustNew(fsRead, shell)
  ```

- **Registry errors name the Tool.** `Registry.Register`, `Unregister`, `Activate` and `Deactivate` wrap `ErrDuplicate` and `ErrNotFound` with the offending ID, and `errors.Is` still matches. `List`, `Describe` and `Active` return copied specs. A Tool registered with a whitespace-padded ID is stored under the trimmed ID, so `Describe(" spaced ")` no longer finds a Tool registered as `"spaced"`. A zero-value `Registry` now accepts `Register`.

### Sessions and stores

- **`FileStore.Save` is durable.** It fsyncs the temp file and the store directory before it returns, so a successful `Save` survives a crash or a power loss.
- **`FileStore.List` reports unreadable files.** It returns an error when any session file is unreadable. It used to omit that file silently. One corrupt file now fails the listing.

### Runtime

- **`internal/testmodel` became `testkit`.** `EchoModel` and `DemoModel` are now exported from `testkit`, together with `FakeModel`, `ReplayModel`, `FakeTool`, `EventRecorder` and session fixtures. The old package was internal, so no external code could import it.

  ```go
  // before
  import "github.com/jinhuang712/gotato/internal/testmodel"
  model := testmodel.DemoModel{}
  // after
  import "github.com/jinhuang712/gotato/testkit"
  model := testkit.DemoModel{}
  ```

- **`NewAgent` returns `RuntimeAgent`.** It is an `Agent` plus the control, lifecycle, event and inspection interfaces that the core always implemented. Code that assigns the result to an `Agent` variable is unchanged. Code that found `ControllableAgent`, `EventSource`, `ToolInspector`, `IdleWaiter`, `RunCanceler` or `LifecycleAgent` by type assertion can call them directly.
- **`Transcript` gained `Len()`.** An Agent enforces `MaxMessages` through `Transcript.Len()`, so it no longer copies a `Session`-backed history on every commit. `session.Session` and the default in-memory transcript already implement it. **Breaking:** a custom Transcript must add `Len() int`.

  ```go
  type Transcript interface {
      Len() int
      Messages() []Message
      Append(Message) error
  }
  ```

- **Identifiers changed format.** `AgentID`, `RunID` and `MessageID` values are no longer `agent-1`, `run-7` or `message-12`. They are `<prefix>-<8 hex process nonce><counter>`, for example `run-3fa9c1e02`, so they stay unique across restarts and across processes that share one Session store. Treat IDs as opaque strings, and stop parsing the numeric suffix. The types and JSON field names are unchanged.
- **Every request is assembled for prompt caching.** The agent builds each request through `gotato.AssembleRequest`. Tools are sorted by ID. `gotato.ForModel` strips `ID`, `Usage`, `StopReason` and `ContentPart.Metadata` from messages. `CacheBreakpoints` hints are added. **Breaking:** an adapter that read those runtime fields from `ModelRequest.Messages` must take them from the Session instead. `ModelContext` gained `System` and `Panel` block lists, and a builder that sets only `Messages` keeps working.
- **New event kind `context_built`.** Every Turn emits one protected `context_built` event before the model request. Its payload holds `messages`, `source_messages`, `prefix_hash`, `prefix_messages`, `panel_bytes`, `system_bytes`, `tools`, `strategy`, `selected_messages`, `dropped_messages` and the builder's metadata. Consumers that assert exact event sequences must include it. Every consumer must tolerate unknown kinds.
- **Blank prompts are rejected.** `Prompt`, `Steer` and `FollowUp` reject a message whose parts are all blank text, such as `UserMessage("")` or `UserMessage("  ")`, with `invalid_argument`. Messages with binary or JSON parts are accepted as before.
- **What stayed the same:** the `Agent` interface, `NewAgent` and every existing `With*` option, the message, tool, model and extension types, and the meaning of `CoreLimits`. `DefaultLimits()` is newly exported, so you can override part of the limits.

### Context

- **Compaction results changed shape.** `modelctx.Compact` sets `MessagesBefore` on the no-op path too, where it used to be 0. `Result.Compaction` is a `*session.Compaction`, present only when `replaced` is true. A no-op compaction no longer emits an all-zero `compaction` object in the CLI, HTTP or gRPC JSON.

### Service

- **`orchestration` and `host` became `service`, with wire contract 2.** `orchestration`, `host` and `cmd/gotato-agent` are removed. The service is `service.Runner`: a Session store plus an Agent created for each Run. `service/httpapi` serves it over HTTP (`ContractVersion "2"`), and `adapter/grpc` serves it over gRPC (`gotato.v2.SessionService`, replacing `gotato.v1.AgentService`). The Session ID is the unit of identity. Conversation keys, agent generations, retirement and spawn groups are gone. Derived work is `session.Fork` plus another Run.

  | Before | After |
  |---|---|
  | `POST /v1/runs {"agent_name","conversation_key","prompt"}` | `POST /v1/sessions {"agent"}` then `POST /v1/sessions/{id}/runs {"prompt"}`, or `POST /v1/runs {"prompt","agent"}` to create and run in one call |
  | `POST /v1/runs/stream` (SSE) | `POST /v1/sessions/{id}/runs/stream` or `POST /v1/runs/stream` (SSE: `event: <kind>` … `event: result`) |
  | `POST /v1/runs/async`, `GET /v1/runs/{id}`, `POST /v1/runs/progress` | removed: use the stream or a client-side job |
  | `POST /v1/runs/{run_id}/cancel` | the same, plus `POST /v1/sessions/{id}/cancel` |
  | `GET /v1/conversations/{id}` | `GET /v1/sessions/{id}` (the full document) |
  | `POST /v1/agents/{id}/close`, `/admin/drain` | removed: an Agent lives for one Run, and drain happens on process shutdown |
  | `go run ./cmd/gotato-agent` | `gotato serve` (HTTP) or `(cd adapter/grpc && go run ./cmd/gotato-grpc)` |

  Removed from the root package: `SpawnID`, `Event.SpawnID`, `Event.OriginRunID`, `AgentName`, `ConversationID`, `ConversationKey`, `AgentGeneration`, `LifecycleEvent.ConversationID` and `LifecycleEvent.Generation`. Store lineage and tenancy in `Session.Metadata`.

- **HTTP body limit.** `service/httpapi` rejects a JSON request body larger than 1 MiB with `413 Request Entity Too Large`.
- **Creating a session with an explicit ID is atomic.** `POST /v1/sessions` with an ID returns `409` when the ID exists, instead of racing a concurrent create.
- **`Runner.Fork` and `Runner.Inspect` signatures changed.** `Runner.Fork(ctx, sessionID, newID string)` takes an explicit new ID, empty for a random one, and rejects an existing ID instead of overwriting it. `Runner.Inspect(ctx, sessionID, options ...InspectOptions)` accepts `Instruction` and `Panel` overrides for one inspection. `service.RunRequest` gained `SkipSave`.

### Providers

- **The gateway authenticates with an API key only, and Responses replaces Codex.** The `pi_oauth` auth type, the `chatgpt.com/backend-api` default, the `originator: pi` and `chatgpt-account-id` headers, and every read or write of `~/.pi/agent/auth.json` are removed. `api: openai-codex-responses` is accepted as an alias for `api: openai-responses`, which now targets `https://api.openai.com/v1/responses` by default. `api: openai-completions` is an alias for `openai-chat-completions`. A YAML file with `auth.type: pi_oauth` fails to load with an explicit message.

### CLI

- **`run --context SPEC` is gone,** together with the `window:N` and `summary:N` strategies. Within a session the model sees the whole history. Use `--compact-ceiling N` for automatic compaction and `--panel time,cwd` for content that changes every turn. The run outcome field `context` is replaced by `compacted`.
