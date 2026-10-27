# Changelog

What changed in Gotato, from the user's side. Newest first. Gotato has made no release yet, so *Unreleased* holds the changes since 2026-10-16, and the dated sections below it hold the work before that. When the first release is cut, it gets its own section. A breaking change carries a before/after note, so you can move your code in one pass. Additive changes may be listed, but breaking ones must be.

## Unreleased

### A notice_queued Event names the notice's kind `notice_kind`

**Breaking**: before the first release nothing keeps an old name. `NoticeQueued.Label` held the notice's kind (`Message.Notice.Kind`). It cannot be called `Kind`, because `kind` in the Event JSON names the Event, so it is `NoticeKind`, and the key says the same.

| Before | After |
|---|---|
| `NoticeQueued.Label` (`label`) | `NoticeQueued.NoticeKind` (`notice_kind`) |

### A RecordInfo's times are `time.Time`

**Breaking**: before the first release nothing keeps an old form. `session.RecordInfo` held its times as strings in a fixed millisecond format, while `Record.CreatedAt()`, `UpdatedAt()` and `LastRunEnd()` returned `time.Time`. The three fields are `time.Time` now. On the wire (a store's JSON, `GET /v1/sessions`, a Session summary) they are RFC 3339 with as many fraction digits as the time has, where they had three. `last_run_end` is left out when no Run has ended (`omitzero`). `RecordInfo.Epoch()` of a Record written before epochs is unchanged: `created-` and the creation time to the millisecond.

| Before | After |
|---|---|
| `RecordInfo.CreatedAt`, `UpdatedAt`, `LastRunEnd` of type `string`, `"2026-10-17T00:00:00.000Z"` | type `time.Time`; `LastRunEnd` is the zero time when no Run has ended |
| `info.UpdatedAt > other.UpdatedAt` | `info.UpdatedAt.After(other.UpdatedAt)` |
| a store adapter keeps three strings | keeps three times; `storetest.Run` compares them with `Equal`, so a store may change the zone but not the instant |

### The kept tail has one token setting, and counts name their unit

**Breaking**: before the first release nothing keeps an old name. The kept tail of a compaction was sized three ways: `Keep` in Messages, `Floor` in tokens (which ignored the Run boundary and the quarter-window cap), and the default, `SegmentSettings.TailTokens`. `Floor` is gone; `TailTokens` is the one token setting, and a tail it sizes now always stays within the latest Run and under the quarter cap. The compaction settings that hold a count of tokens or Messages say so in their names.

| Before | After |
|---|---|
| `CompactConfig{Floor: n}`, `CompactPolicy{Floor: n}` (the most tokens the tail holds, past the Run boundary and the quarter cap) | gone; `SegmentSettings: modelctx.SegmentSettings{TailTokens: n}`, which the latest Run and a quarter of the window (or ceiling) bound |
| `CompactConfig.Keep` | `CompactConfig.KeepMessages` |
| `CompactConfig.Window`, `CompactPolicy.Window` | `WindowTokens` |
| `CompactConfig.Ceiling`, `CompactPolicy.Ceiling` | `CeilingTokens` |
| `CompactConfig.SegmentBudget`, `CompactPolicy.SegmentBudget` | `SegmentBudgetTokens` |
| `MergeConfig.Budget`, `MergeConfig.Ceiling` | `BudgetTokens`, `CeilingTokens` |
| `AutoCompactor.Ceiling()`, `CompactPolicy.EffectiveCeiling()` | `CeilingTokens()`, `EffectiveCeilingTokens()` |
| `POST /v1/sessions/{id}/compact` with `{"keep": 2}` | `{"keep_messages": 2}` |

### Gateway settings name their unit and type

A setting that holds a number says its unit, and a setting with a family of constants has its own type, so the constants carry the type's name. Before, `Config.API`, `Config.Provider` and `Config.DeferredTools` were plain strings and the deferred forms were `DeferredNative` and `DeferredList`. The YAML strings that `api`, `provider` and `deferred_tools` take are the same, and an unknown value is refused as before.

| Before | After |
|---|---|
| YAML `context_window: 200000` (the loader is strict, so the old key now fails and names `context_window`) | `context_window_tokens: 200000` |
| `Config.ContextWindow`, `Client.ContextWindow()`, the `modelctx` interface method `ContextWindow() int` | `Config.ContextWindowTokens`, `Client.ContextWindowTokens()`, `ContextWindowTokens() int` |
| `Config{API: "anthropic-messages"}` (`string`) | `Config{API: gateway.APIAnthropicMessages}`; `Config.API` is a `gateway.API`, and `APIChatCompletions`, `APIResponses`, `APIAnthropicMessages` are typed constants |
| `Config{Provider: "anthropic"}` (`string`) | `Config{Provider: gateway.ProviderAnthropic}`; `Config.Provider` is a `gateway.Provider`, with typed `ProviderAnthropic`, `ProviderOpenAI`, `ProviderDeepSeek`, `ProviderOther` |
| `Config.DeferredTools string`, `DeferredNative`, `DeferredList`, `Client.DeferredTools() string` | `Config.DeferredTools gateway.DeferredForm`, `DeferredFormNative`, `DeferredFormList`, `Client.DeferredTools() gateway.DeferredForm` |

### Event fields written one way

The same thing is written the same way in Go and on the wire. Before, a duration was an `int64` on some Events and a `float64` on others, a Tool was `tool` here and `tool_id` there, and a list of loaded Tools had four names. A client that reads Event JSON, a stored Record or a Tool implementation changes the keys below; a stored Record written before this change does not read back.

| Before (Go field, JSON key) | After |
|---|---|
| `RunEnded.ElapsedMS`, `TurnEnded.ElapsedMS`, `RunMetrics.ElapsedMS`, `ModelRetried.WaitMS` (`int64`, whole milliseconds) | `float64` milliseconds, with the sub-millisecond part, like `SessionLoaded`, `SessionSaved` and `ToolEnded`; keys `elapsed_ms`, `wait_ms` unchanged |
| `ToolLoaded.Tool`, `ToolUnloaded.Tool` (`tool`) | `ToolID` (`tool_id`) |
| `ToolEnded.ToolID`, `ToolResultSpilled.ToolID` (`tool_id,omitempty`) | `tool_id`, always written |
| `ModelRequest.Options` (`options`) | `ModelRequest.Config` (`config`) |
| `ToolUse.QualifiedID` (`qualified_id`) | `ToolUse.ToolID` (`tool_id`) |
| `ToolUse.ArgumentsJSON` (`arguments_json`) | `ToolUse.Arguments` (`arguments`), as on `ToolCall` |
| `Compaction.Tools`, `CompactionEnded.Tools`, `SessionCreated.Tools`, `Segment.Tools`, `session.Document.Tools` (`tools`) | `LoadedTools` (`loaded_tools`) |
| `ToolResult.Loaded` (`loaded`) | `ToolResult.LoadedTools` (`loaded_tools`), as on `ModelRequest.LoadedTools` |

A Tool that reads its call changes `use.QualifiedID` to `use.ToolID` and `use.ArgumentsJSON` to `use.Arguments`.

Phases 2, 3, 4 and 5 of [ROADMAP.md](plan/ROADMAP.md): the Effect cap, the names, every step of a Run written as it happens, one writer per Session, the drain, the Event design with the Claim Events, the load balancer's part, stream resume on any instance, and the multi-instance harness. The Effect cap is an addition. The names ship as one planned break. The store contract changes too, and every defect in [TODO.md](plan/TODO.md) is fixed; the breaking changes among them each carry a before and after below. It also fixes how a Run ends when it fails or is canceled during a tool batch, and what the Runner returns for a canceled Run.

### Fields that carried nothing

**Breaking**: before the first release nothing keeps an old field. Each row is a field that was never read, or that only a test or a compatibility fallback used.

| Before | After |
|---|---|
| `service.Agent{Extensions: []any{x}}` | `service.Agent{Options: []gotato.SessionOption{gotato.WithExtension(x)}}` |
| `gotato.Compaction.SummaryMessageID` (`summary_message_id`) | gone; a Segment has no summary Message, and nothing read or wrote it |
| `session.Document.MaxSegment` (`max_segment`) | gone; `Load` reads the largest number from the Segments the Document lists. A Segment closed alone, which no Checkpoint lists, no longer keeps its number across a Snapshot and Load (the Runner never closes one) |
| `session.Document.EventLimit` (`event_limit`) | gone; a Record keeps its last 1000 Events in memory |
| `modelctx.CompactConfig.Now`, `CompactPolicy.Now`, `MergeConfig.Now` | gone; they use the wall clock, and no test set them |
| `service.NoticeResult.Queued`; `queued: true` in the notice answer of the HTTP route (`POST /v1/sessions/{id}/notices`) and of `gotato session notice --json` | gone; the answer is `{"session_id", "number", "message_id"}`, and a 202 or exit 0 says the notice was queued |

### Names follow one set of rules (batch 3)

[AGENTS.md → Naming](AGENTS.md#naming) applied to constants, settings structs and the retryable convention. **Breaking**: nothing keeps an old name; wire strings do not change, and `ErrorCode` constants stay `Code…`.

**A constant is prefixed with the name of its type.** The pattern is `<Type><Member>`; each row lists the members.

| Type | Before | After |
|---|---|---|
| `gotato.EventKind` | `KindRunStarted`, `KindRunEnded`, `KindTextDelta`, … (all 46) | `EventKindRunStarted`, `EventKindRunEnded`, `EventKindTextDelta`, … |
| `gotato.EventClass` | `ClassStored`, `ClassDelta`, `ClassInstance` | `EventClassStored`, `EventClassDelta`, `EventClassInstance` |
| `gotato.ClaimWhy` | `ClaimNewSession`, `ClaimIdle`, `ClaimWriterGone`, `ClaimWaited` | `ClaimWhyNewSession`, `ClaimWhyIdle`, `ClaimWhyWriterGone`, `ClaimWhyWaited` |
| `gotato.CompactionWhy` | `CompactionOnDemand`, `CompactionPastCeiling`, `CompactionBeforeUnload`, `CompactionBeforeCacheExpires`, `CompactionOverflow`, `CompactionPastBudget` | `CompactionWhyOnDemand`, `CompactionWhyPastCeiling`, `CompactionWhyBeforeUnload`, `CompactionWhyBeforeCacheExpires`, `CompactionWhyOverflow`, `CompactionWhyPastBudget` |
| `gotato.MessageSource` | `SourcePrompt`, `SourceSteer`, `SourceFollowUp`, `SourceInterrupt`, `SourceNotice` | `MessageSourcePrompt`, `MessageSourceSteer`, `MessageSourceFollowUp`, `MessageSourceInterrupt`, `MessageSourceNotice` |
| `gotato.ToolLoadedWhy`, `ToolUnloadedWhy` | `ToolLoadedNames`, `ToolLoadedQuery`, `ToolLoadedCall`, `ToolUnloadedCompaction`, `ToolUnloadedGone` | `ToolLoadedWhyNames`, `ToolLoadedWhyQuery`, `ToolLoadedWhyCall`, `ToolUnloadedWhyCompaction`, `ToolUnloadedWhyGone` |
| `gotato.CacheAnchor` | `CacheAfterSystem`, `CacheAfterTools`, `CacheAfterMessage` | `CacheAnchorAfterSystem`, `CacheAnchorAfterTools`, `CacheAnchorAfterMessage` |
| `gotato.ModelEventKind` | `ModelTextDelta`, `ModelReasoningDelta`, `ModelReasoningDone`, `ModelToolCall`, `ModelUsage`, `ModelDone` | `ModelEventKindTextDelta`, `ModelEventKindReasoningDelta`, `ModelEventKindReasoningDone`, `ModelEventKindToolCall`, `ModelEventKindUsage`, `ModelEventKindDone` |
| `gotato.NoticeSource` | `NoticeFromGotato`, `NoticeFromService` | `NoticeSourceGotato`, `NoticeSourceService` |
| `gotato.ToolEffect` | `EffectUnspecified`, `EffectRead`, `EffectWriteLocal`, `EffectWriteExternal`, `EffectDestructive` | `ToolEffectUnspecified`, `ToolEffectRead`, `ToolEffectWriteLocal`, `ToolEffectWriteExternal`, `ToolEffectDestructive` |
| `gotato.ContentKind` | `ContentText`, `ContentReasoning`, `ContentImage`, `ContentJSON` | `ContentKindText`, `ContentKindReasoning`, `ContentKindImage`, `ContentKindJSON` |
| `gotato.ToolResultStatus` | `ToolResultOK`, `ToolResultBlocked`, `ToolResultFailed`, `ToolResultCanceled` | `ToolResultStatusOK`, `ToolResultStatusBlocked`, `ToolResultStatusFailed`, `ToolResultStatusCanceled` |
| `gotato.StopReason` | `StopNone`, `StopEndTurn`, `StopToolCalls`, `StopMaxTokens`, `StopCanceled`, `StopError`, `StopCutOff` | `StopReasonNone`, `StopReasonEndTurn`, `StopReasonToolCalls`, `StopReasonMaxTokens`, `StopReasonCanceled`, `StopReasonError`, `StopReasonCutOff` |
| `gotato.RunStatus` | `RunRunning`, `RunCompleted`, `RunCanceled`, `RunDeadlineExceeded`, `RunFailed` | `RunStatusRunning`, `RunStatusCompleted`, `RunStatusCanceled`, `RunStatusDeadlineExceeded`, `RunStatusFailed` |
| `service.MessageKind` | `MessageSteer`, `MessageFollowUp`, `MessageInterrupt` | `MessageKindSteer`, `MessageKindFollowUp`, `MessageKindInterrupt` |
| `service.QueuePolicy` | `RejectWhileBusy`, `WaitWhileBusy` | `QueuePolicyReject`, `QueuePolicyWait` |
| `modelctx.Result.Reason` (was `string`) | `ReasonBelowMinTokens`, `ReasonNothingToClose` | new type `CompactReason`: `CompactReasonBelowMinTokens`, `CompactReasonNothingToClose` |
| `modelctx.MergeResult.Reason` (was `string`) | `MergeUnderBudget`, `MergeNothingToMerge`, `MergeRecapFailed`, `MergeWriteFailed` | new type `MergeReason`: `MergeReasonUnderBudget`, `MergeReasonNothingToMerge`, `MergeReasonRecapFailed`, `MergeReasonWriteFailed` |

`SessionStatus…`, `Role…`, `session.Layer…`, `workload.Kind…` and `workload.Cause…` already followed the rule.

**A struct of settings is a `Config` or a `Policy`, never `Options`; a function type that configures is an `Option`, prefixed when a package has two.** A per-call setting is a `Config`; a standing rule stays a `Policy` (`modelctx.CompactPolicy`, `service.WarmPolicy`).

| Before | After |
|---|---|
| `modelctx.CompactOptions`, `MergeOptions`, `ReadResultOptions`, `ReadSegmentOptions` | `modelctx.CompactConfig`, `MergeConfig`, `ReadResultConfig`, `ReadSegmentConfig` |
| `gotato.ModelOptions`, `gotato.WithModelOptions(…)` | `gotato.ModelConfig`, `gotato.WithModelConfig(…)`; `ModelRequest.Options` keeps its name and JSON key |
| `service.InspectOptions` | `service.InspectConfig` |
| `gotato.Option` (beside `SubscribeOption`) | `gotato.SessionOption` |
| `session.Option` (beside `WriterOption`) | `session.RecordOption` |
| `mcp.Option` (beside `HTTPOption`) | `mcp.ToolSetOption` |

**A `Default…` constant names its unit** where the type does not. A `time.Duration` needs no suffix.

| Before | After |
|---|---|
| `modelctx.DefaultSegmentsBudget` (estimated tokens) | `modelctx.DefaultSegmentsBudgetTokens` |

**The retryable convention is `Retryable() bool`,** matching `ContextOverflow() bool`. A Model error that says its failure is transient implements `Retryable() bool`.

| Before | After |
|---|---|
| `interface{ IsRetryable() bool }` read by the core | `interface{ Retryable() bool }` |
| `gateway.Error.Retryable` (field), `(*gateway.Error).IsRetryable()` | `(*gateway.Error).Retryable()`; the field is unexported |

`gotato.RuntimeError.Retryable` and `gotato.Retryable(err)` keep their names.

### Names follow one set of rules (batch 2)

The rules of [AGENTS.md → Naming](AGENTS.md#naming) applied to names that did not yet follow them: a name is the glossary's, `Load` reads from a store, `Parse` from bytes and `From…` builds from a value in memory. **Breaking**: nothing keeps an old name.

| Before | After |
|---|---|
| `service.Runner.Session(ctx, id)` (returns a `*session.Record`) | `service.Runner.Record(ctx, id)` |
| `session.Summary`, `Record.Summary()`, `session.SummaryOf(r)` | `session.RecordInfo`, `Record.Info()`, `session.InfoOf(r)` |
| `session.Loaded.Summary` | `session.Loaded.Info` |
| `session.Run` (the stored per-Run struct) | `session.RunInfo` |
| `service.RunResult` (the Runner's answer: Session ID, Agent, `gotato.RunResult`, final text, counts) | `service.RunResponse`, pairing with `service.RunRequest`; `gotato.RunResult` keeps its name |
| `session.Load(doc)` | `session.FromDocument(doc)` |
| `testkit.LoadReplay(data)` | `testkit.ParseReplay(data)` |
| `session.Create(ctx, store, r, instance)` (returns a `*Writer`) | `session.CreateWriter(ctx, store, r, instance)`, pairing with `OpenWriter` |
| `FileStore` batch file `{"entries":[…],"summary":{…}}` | `{"entries":[…],"info":{…}}`; a `FileStore` directory written before this change is not read |

`session.Entry.IsCheckpoint()` keeps its name: "checkpoint" is a noun, and the standard library keeps `Is` for a noun (`fs.FileMode.IsDir`).

### Names follow one set of rules (batch 1)

[AGENTS.md → Naming](AGENTS.md#naming) fixes the rules exported names follow. This batch applies three of them. **Breaking**: nothing keeps an old name, and no old wire string is accepted.

**American spelling, in Go names and in wire strings.** `cancelled` is `canceled`, as in `context.Canceled`.

```text
before  "cancelled"   stop reason, Run status, Tool Result status, error code,
                      cause, HTTP and CLI JSON, replay fixtures
after   "canceled"

before  gotato.ErrCancelled, workload.CauseCancelled
after   gotato.CodeCanceled (see the error codes below), workload.CauseCanceled
```

`gotato.RunCanceled`, `StopCanceled` and `ToolResultCanceled` keep their names; only their strings changed.

**A Run is stopped with Abort,** the glossary's one verb.

| Before | After |
|---|---|
| `Session.CancelRun(ctx, runID)` | `Session.AbortRun(ctx, runID)` |
| `service.Runner.CancelSession(ctx, id)` | `service.Runner.AbortSession(ctx, id)` |
| `POST /v1/sessions/{id}/cancel` | `POST /v1/sessions/{id}/abort` |
| response `{"status": "cancel_requested"}` | `{"status": "abort_requested"}` |

`Interrupt` stays: it is a Steer that cuts a model call short, not a stop.

**An error code is `Code…`, a sentinel error is `Err…` and carries a code, and a code's name matches its string.** The constants of type `gotato.ErrorCode` are renamed; the sentinel variables (`service.ErrBusy`, `session.ErrNotFound`, …) keep their names.

| Before | After |
|---|---|
| `gotato.ErrInvalidArgument`, `ErrInvalidState`, `ErrBusy`, `ErrSessionClosed`, `ErrSessionClosing`, `ErrModelFailure`, `ErrContextOverflow`, `ErrModelProtocolFailure`, `ErrToolResolutionFailure`, `ErrToolArgumentFailure`, `ErrToolExecutionFailure`, `ErrExtensionFailure`, `ErrLimitExceeded`, `ErrDeadlineExceeded`, `ErrNotSupported`, `ErrConflict`, `ErrStoreFailure`, `ErrOverloaded`, `ErrDraining`, `ErrNotFound`, `ErrCannotResume`, `ErrNotWriter` | the same names with `Code` for `Err`: `gotato.CodeInvalidArgument`, `CodeBusy`, … |
| `gotato.ErrCancelled` (`"cancelled"`) | `gotato.CodeCanceled` (`"canceled"`) |
| `gotato.ErrToolArgumentFailure` (`"tool_argument_validation_failure"`) | `gotato.CodeToolArgumentFailure` (`"tool_argument_failure"`) |
| `gotato.ErrInternalInvariant` (`"internal_invariant_failure"`) | `gotato.CodeInternalInvariant` (`"internal_invariant"`) |

Sentinels that had no code gain one. `errors.Is` now matches a sentinel by identity (the sentinel itself, or an error that wraps it); it used to match any `*RuntimeError` of the same code, which made two sentinels with one code match each other. `gotato.IsCode(err, code)` matches by code.

| Sentinel | Before | After |
|---|---|---|
| `session.ErrExists` | no code | `invalid_state` |
| `session.ErrExpired` | no code | `invalid_state` |
| `session.ErrOldLayout` | no code | `not_supported` |
| `service.ErrNotPersisted` | no code | `store_failure`, not retryable by itself, as before |
| `modelctx.ErrBadRecap` | no code | `model_protocol_failure` |
| `modelctx.ErrNoRecapContext` | no code | `invalid_argument` |
| `testkit.ErrScriptExhausted` | no code (wraps `io.ErrUnexpectedEOF`) | `model_failure` (still wraps `io.ErrUnexpectedEOF`) |
| `gotato.ErrEventsLost` | no code | `cannot_resume` |

### String helpers move to `internal/text`

Five string helpers were exported only because sibling packages used them. They now live in `internal/text`, which only Gotato's own packages can import. **Breaking** for code that called them; none has a replacement, because none is part of a contract.

```text
before  gotato.EscapeTags(s), gotato.Paginate(lines, limit), gotato.SplitLines(text, limit),
        gotato.SpillRef(id), gotato.CallOfRef(ref)
after   none; they are internal
```

`gotato.SpillRefPrefix` and `gotato.ReadResultToolID` stay, since a ref is what the model reads and passes back.

### Implementation helpers leave the public API

These exports were steps used only inside their own package. They are unexported. **Breaking** for code that called them; none has a replacement, because none is part of a contract.

```text
before  gotato.CutRunes, gotato.SpillBody, gotato.RenderBlocks, gotato.QueuedNotice,
        ModelContext.RenderSystem, Feed.SetSource, ToolEffect.Rank
        session.EventOf, session.FromEntries, session.LayerOf, session.ReadSpilled,
        session.DefaultSpillCacheBytes, Record.CloseSegment, Record.CheckpointSegments
        modelctx.MergeDigest, modelctx.MergeRecapAsk, modelctx.MergeRecapModelRequest,
        CompactPolicy.EffectiveBudget
        gateway.ParseYAML, gateway.YAMLConfig (LoadYAML stays)
        mcp.ConvertResult, mcp.EffectOf, mcp.SlimSchema
after   none; they are internal
```

`Record.CloseSegmentAndCheckpoint`, `Record.MergeSegments` and `Compact` close and list Segments; read a rebuilt Record with `session.Open` or `session.View`.

### The CLI is a test tool, not a deliverable

`gotato` is no longer shipped or versioned. It moved to `internal/cmd/gotato` and is our own test tool, a thin client of the packages. Its commands, flags, JSON fields and exit codes promise nothing. **Breaking** for anything that installed it.

```text
before  go install github.com/jinhuang712/gotato/cmd/gotato@latest
after   none; in this repository: go run ./internal/cmd/gotato
```

### One way to subscribe; EntryKind is EventKind

`Session.SubscribeFrom` and `Runner.SubscribeSession` did what `Subscribe` with `SubscribeAfter` and `StreamSession` do. `session.EntryKind` was an alias of `gotato.EventKind`. All three are removed. **Breaking.**

```text
before  s.SubscribeFrom(ctx, n)
after   s.Subscribe(ctx, gotato.SubscribeAfter(n))

before  runner.SubscribeSession(ctx, id, n)
after   runner.StreamSession(ctx, id, service.StreamFrom{After: n})

before  session.EntryKind
after   gotato.EventKind
```

### An Agent's reference text is part of its instruction

The "Static block" is gone. Reference text an Agent carries, such as a schema or a runbook, is written into its instruction. `modelctx.FullHistory()` was an alias of the core builder. `ErrOldSummary` refused a Record compacted before Segments existed; no such Record exists.

```text
before  modelctx.WithStatic(modelctx.FullHistory(), modelctx.Resource("AGENTS.md", s))
        modelctx.Text(tag, s), modelctx.JSON(tag, v)
after   Agent.Instruction / gotato.WithInstruction(instruction + "\n\n" + s)

before  modelctx.FullHistory(), modelctx.StrategyFullHistory
after   gotato.FullHistoryContext()

before  modelctx.ErrOldSummary, modelctx.MetadataCompaction; Compact refused such a Record
after   gone; the Report's metadata no longer has static_blocks
```

### Hooks that only tests used leave the public API

Six exported names existed for tests alone. They are gone from the public API; the tests of each package reach them through an `export_test.go`. `service.Config.Liveness` and `service.Config.StreamPoll` stay: they are settings of a running service.

```text
before  service.Config{Clock: clock}, service.Clock, service.Timer
after   gone; service's tests use WithClock(cfg, clock) from export_test.go

before  httpapi.New(runner, httpapi.WithKeepAlive(d))
after   gone; the keepalive is fixed at 15 seconds

before  session.WithClock(now), session.WithEventLimit(n), session.DefaultEventLimit
after   gone; the limit is fixed at 1000 Events; session's tests use export_test.go

before  session.NewMemoryStore(session.WithStoreClock(now)), session.NewFileStore(dir, opts...), session.StoreOption
after   session.NewMemoryStore(), session.NewFileStore(dir)
```

### A zero limit always means its default

`CoreLimits` read a zero field two ways. Without `WithLimits` a zero was the default; once `WithLimits` was called, a zero size limit admitted nothing, a zero progress cap suppressed progress, a zero `MaxNotices`, `MaxSteerMessages` or `MaxFollowUpMessages` refused that control message, and a zero deadline disabled it. `WithLimits(CoreLimits{MaxParallelTools: 4})` gave a Session that admitted no message. Now a zero field is its default, for every field, with or without `WithLimits`. Nothing is kept for the old reading.

| Before | After |
|---|---|
| `l := gotato.DefaultLimits(); l.MaxParallelTools = 4; gotato.WithLimits(l)` (a bare `CoreLimits{MaxParallelTools: 4}` admitted nothing) | `gotato.WithLimits(gotato.CoreLimits{MaxParallelTools: 4})` |
| `ModelCallDeadline: 0` or `ToolCallDeadline: 0` disabled the deadline | the default (2 minutes); a deadline cannot be turned off, and a Run's own deadline is still its `ctx` |
| `MaxSteerMessages`, `MaxFollowUpMessages` or `MaxNotices` of 0 refused that message (`message_rejected` with `why: "disabled"`) | the default (8, 4, 16); `why` is only `queue_full` now. A queue cannot be switched off |
| `CoreLimits.MaxToolProgressBytes` (1 MiB), `CoreLimits.MaxToolProgressUpdates` (1000) | removed; the same caps stay inside the core |
| `service.Agent.Limits` with `MaxNotices: 0` refused `POST …/notices` | the default queue size (16) |


### Every HTTP route is under `/v1/sessions`

Gotato's HTTP API had two ways to do some things. `POST /v1/runs` and `POST /v1/runs/stream` created a Session and ran it in one call, which `POST /v1/sessions` followed by `POST /v1/sessions/{id}/runs` already does. `POST /v1/runs/{run_id}/cancel` duplicated `POST /v1/sessions/{id}/cancel`, but was not scoped by Session: it never reached the Run's writer, so it was wrong on more than one instance. Both are removed, and with them `service.Runner.CancelRun`. A run request no longer takes `agent` or `metadata`: they applied only when a run created the Session, and `POST /v1/sessions` takes them.

```text
before  POST /v1/runs {"prompt":"hi","agent":"echo"}
after   POST /v1/sessions {"agent":"echo"}                  → {"id": …}, and the session-id header
        POST /v1/sessions/{id}/runs {"prompt":"hi"}

before  POST /v1/runs/{run_id}/cancel
after   POST /v1/sessions/{id}/cancel

before  runner.CancelRun(ctx, runID)
after   runner.CancelSession(ctx, sessionID)
```

### The gateway is configured by its YAML file only

The gateway had three ways to be configured: a YAML file, environment variables read by `gateway.NewFromEnv()`, and `GOTATO_GATEWAY_CACHE_RETENTION`, which won over the file. Now there is the YAML file. A secret reaches it through `${NAME}`, which the loader expands from the environment. `GOTATO_GATEWAY_CONFIG`, the CLI's path to the file, stays. The `auth` section is removed too: `api_key` was its only scheme. The loader rejects unknown keys, so an old `auth:` key fails with `field auth not found`.

| Before | After |
|---|---|
| `GOTATO_GATEWAY_API_KEY=… ` and `gateway.NewFromEnv()`; the `OPENAI_BASE_URL`, `OPENAI_API_KEY` and `OPENAI_MODEL` fallbacks | a YAML file with `api_key: ${GOTATO_GATEWAY_API_KEY}`, and `gateway.LoadYAML(path)` |
| `GOTATO_GATEWAY_API`, `_BASE_URL`, `_ENDPOINT`, `_MODEL`, `_PROVIDER` | the `api`, `base_url`, `endpoint`, `model` and `provider` keys |
| `GOTATO_GATEWAY_CACHE_RETENTION=5m`, over each model's own | `cache_retention: 5m` in the model's file; `gateway.CacheRetentionEnv` is removed |
| `auth: {type: api_key}`; `gateway.AuthConfig`, `Config.Auth` | nothing: delete the key |

### One way to give a Session its tools; unused options go

Each thing now has one way. `WithTool` is folded into the variadic `WithTools`; `WithFunc` is `NewFuncTool` plus `WithTools`; `WithDeadlines` is a field pair of `CoreLimits`; `WithRootNamespace` had no caller. No old name is kept.

| Before | After |
|---|---|
| `gotato.WithTool(t)` | `gotato.WithTools(t)` |
| `gotato.WithFunc("n", "d", fn)` | `tool, err := gotato.NewFuncTool("n", "d", fn)`, then `gotato.WithTools(tool)`; a type the schema cannot describe now fails at `NewFuncTool` |
| `gotato.WithDeadlines(m, t)`: only the two deadlines, other limits keep their defaults | `gotato.WithLimits(gotato.CoreLimits{ModelCallDeadline: m, ToolCallDeadline: t})` |
| `gotato.WithRootNamespace("ns")` | none; a root Tool keeps its declared ID |

### The core lives in `gotato/`, and `storetest` next to the Store

The package layout follows three rules: the module root holds no Go code; a directory's name is its package's name, so no import needs an alias; a test-helper package sits next to what it tests. The core directory `gotato-core` is now `gotato`, and the Store contract suite moved beside `session`. `gotato.NewSession` and every other call stay as they were; only two import paths change, and the alias is no longer needed. `gotato/layering_test.go` (was `gotato-core/layering_test.go`) keeps the rules.

```go
// before
import (
    gotato "github.com/jinhuang712/gotato/gotato-core"
    "github.com/jinhuang712/gotato/testkit/storetest"
)
// after
import (
    "github.com/jinhuang712/gotato/gotato"
    "github.com/jinhuang712/gotato/session/storetest"
)
```

### Our test tools leave the public surface

The reference tasks, the workload driver and the fake gateway are tools for testing Gotato itself. They moved under `internal/`, so they promise nothing to a team. `testkit` keeps the fakes and recorders: `FakeModel`, `ReplayModel`, `FuncModel`, `FakeTool`, `EventRecorder`, `NewRecord`, and the echo and demo models. `examples/larkbot` and `bench/refservice` still build, since Go's `internal/` rule is by import path and larkbot's path sits under the root module's.

| | before | after |
|---|---|---|
| reference tasks (`Task`, `ReferenceTasks`, `LongSession`, `ToolSearchTask`, `PaymentsRecapCheck`, `LargeExport`, `WithLoadedToolsOf`, ...) | `github.com/jinhuang712/gotato/testkit` | `github.com/jinhuang712/gotato/internal/reftasks` |
| workload driver | `github.com/jinhuang712/gotato/service/workload` | `github.com/jinhuang712/gotato/internal/workload` |
| workload command | `go run ./cmd/gotato-workload` | `go run ./internal/cmd/gotato-workload` |
| fake gateway | `github.com/jinhuang712/gotato/test/fakegateway` | `github.com/jinhuang712/gotato/internal/fakegateway` |
| fake gateway command | `.../test/fakegateway/cmd/fakegateway` | `.../internal/fakegateway/cmd/fakegateway` |

`LogsRead`, `RecapQuestion`, `DemoRecap`, `DemoBigBytes`, `PaymentsInstruction`, `SupportInstruction`, `SupportCasePrompt`, `ToolSearchInstruction`, `WorkloadInstruction` and `OrdersLookupID` are no longer exported: nothing outside their own package used them. `gotato-core/layering_test.go` now also fails when a shipped package imports `internal/`.

### `toolregistry` is removed

Nothing used the package besides the README quickstart, the getting-started guide and one example. `gotato.WithTools` gives a Session its tools, a `ToolSource` can change them between Turns, ToolSets group them, and `Runner.SetToolActive` switches one on or off for a Session.

```go
// before
import "github.com/jinhuang712/gotato/toolregistry"
gotato.WithToolSource(toolregistry.MustNew(weather))

// after
gotato.WithTools(weather)
```

FEATURES.md renames G-F10 from "Tool sets and the registry" to "Tool sets", and the Glossary drops the Tool Registry term.

### Contract versions

Gotato has made no release, so no consumer has seen a contract version. The numbers that the development builds carried (the HTTP `ContractVersion` `"2"` to `"4"`) were counters of the builds, not versions of a release. They are reset: the contract is version `"1"` at the first release, and from then on every break raises it. Until then nothing is versioned except by this changelog, and every break has its entry here.

| | before | after |
|---|---|---|
| HTTP `ContractVersion` (`GET /healthz` `contract`, `gotato serve` `contract`) | `"4"` | `"1"` |

### Removed

- **The reader of pre-Event Record entries is removed** (`session/legacy.go`). Gotato has made no release, so no Record written before the Event design exists. **Breaking** for a store that still holds one. Before: a Record whose Entries had the old kinds (`checkpoint`, `message`, `log_snapshot`, `run_start`, ...) still loaded, and `session.StoredEvents` mapped them onto Events. After: such an Entry is refused as an unknown kind, never misread. The Store suite in `session/storetest` no longer checks a `checkpoint` Entry. `session.ErrOldLayout` stays: it refuses an older on-disk layout.

- **The gRPC adapter, `adapter/grpc`, and the `gotato-grpc` binary.** Gotato ships HTTP (`service/httpapi`) as its one wire transport ([G-N27](FEATURES.md#g-n27--no-grpc-adapter)). A service that wants gRPC maps `service.Runner` itself, the way `service/httpapi` does. **Breaking:**

  | Before | After |
  |---|---|
  | `import grpcadapter "github.com/jinhuang712/gotato/adapter/grpc"`, `grpcadapter.New(runner)` | serve `service/httpapi`, or map `service.Runner` onto your own gRPC service |
  | `gotato-grpc` (`go run ./cmd/gotato-grpc` in `adapter/grpc`) | `gotato serve` |

### Examples

- **A chat-bot example, `examples/larkbot`.** A Lark chat bot that runs several Gotato instances, with a fake Lark and a local kind cluster that plays seven scenarios. It is a module of its own and adds nothing to the root module. Guide: [Example: a chat bot](guides/example-chat-bot.md).

### MCP

- **An MCP tool's progress, a server's instructions, and a tool that asks for input.** Progress notifications reach the Tool's progress function, so they are `tool_progress` Events; before, they were dropped. A server's `instructions` (first paragraph) is the ToolSet's description when `WithDescription` is not set; before, the description was empty. A tool that asks for input mid-call (elicitation, sampling) ends with a failed Tool Result that says so; before, the call failed with the SDK's error text.
- **A restarted MCP server is reached again at once.** When a server forgets the ToolSet's session (it restarted, and answers 404 to the old session ID), `adapter/mcp` now starts a new session and sends the resolution or the call again once; the server did not run it. Before, the first resolution after the restart failed with `session not found`, so a Run that began then had no tools from that server, and the first call failed twice before the third succeeded. After, both succeed on the first try. A call whose connection closed during it is still not sent again, since the server may have run it.

### Layout

- **The core package moved from the module root to `gotato-core/`.** The package is still named `gotato`, so `gotato.NewSession` and every other call stay as they were; only the import path changes. The module root now holds no Go file, and `gotato-core/layering_test.go` keeps it so. `gateway.example.yaml` moved to `gateway/`. Before, a program imported `github.com/jinhuang712/gotato`. After, it imports `github.com/jinhuang712/gotato/gotato-core`; give it the name `gotato` in the import, as in `gotato "github.com/jinhuang712/gotato/gotato-core"`. The other packages and the three adapter modules keep their paths. The module path `github.com/jinhuang712/gotato` is unchanged.

### Instance names

- **`service.Config.Instance` is a label, and each start makes its own store identity.** The Runner appends a random suffix, so `pod-0` is written as `pod-0-3fa9c1d2` in the Liveness row, the Claims, `session_claimed` Events and the advertised-address row, and `Runner.Instance()` returns that form. Before, the name was the identity, and a name had to be new for each process start: a restarted process under the same name used the expired row and every write failed with `session.ErrExpired`; a new process with the name of a live one took its Claim as its own, with no holder check and the same Epoch; and the old process's late `Close` removed the new process's row. After, none of these can happen: a restarted process writes at once, a process that overlaps another is refused as busy or `not_writer` like any other instance, and `Close` removes only its own row. With no name set, nothing changes. If you compared `Runner.Instance()` or a Claim's `instance` to the name you configured, compare its prefix, or read `Runner.Instance()` ([FEATURES.md → G-F27](FEATURES.md#g-f27--service-many-instances-)).

### Tool search

- **A Session's tools can be listed and switched by ID for ToolSet tools too.** `Runner.SetToolActive` and `gotato tools activate|deactivate` now accept the qualified ID of a tool of the Agent's ToolSets (`files.read`) and still write only `gotato.tool.<id>`. `Runner.Tools` with a Session and `gotato tools list --session` list every tool of every ToolSet, loaded or not, with `toolset`, `loaded` and `active`, and show a ToolSet that did not resolve as one `unavailable` entry. The core package adds `Session.ToolSets()`, which reports the installed ToolSets and their tools. Before, a ToolSet tool was switched off only by writing the metadata key by hand, and the listing showed `search_tools` and the loaded tools; `activate` of a ToolSet tool exited `3`. After, it succeeds (exit `0`) and the listing is complete. The new `tools list` fields are additions and are omitted when empty.

- **The model loads tools one by one, from a catalog.** A ToolSet is a grouping and a source; the unit of loading is one tool ([DESIGNS.md → Tools load one by one from a catalog](DESIGNS.md#tools-load-one-by-one-from-a-catalog--2026-10-25), [FEATURES.md → G-F34](FEATURES.md#g-f34--tool-search-)). `search_tools` replaces `activate_toolset`; its description is the catalog, which lists a loaded tool like any other and so stays the same bytes while tools load. It takes `names[]` (load exactly), `queries[]` (BM25, standard library: load the top 3, list 5 more) and `toolset` (list a folded ToolSet's tools, load none). A call to a catalog tool that is not loaded does not run: the tool is loaded and the call fails with "was not loaded, so this call did not run. It is loaded now; call it again." A loaded tool stays until a compaction, which keeps the tools called in the Segment it closes or in the kept tail and unloads the rest. A tool above the Effect cap, or switched off for the Session (`gotato.tool.<id>`), is never in the catalog, never found and never loaded. In this step the tool list changes when a tool loads (the `list` form); the native form is Phase 7, step 4. No old name is kept.

  | Before | After |
  |---|---|
  | `WithToolSet(set)`: the model called `activate_toolset` to make the whole set visible on the next request | `WithToolSet(set)`: the catalog lists the set's description and all its tool names; the model loads tools with `search_tools` |
  | no such mode | `gotato.WithFoldedToolSet(set)`: the catalog shows the description and a count of tools |
  | `activate_toolset` (`gotato.ActivationToolName`), visible while a ToolSet was inactive | `search_tools` (`gotato.SearchToolName`), visible while the catalog holds a tool the Session may load |
  | `CoreLimits.MaxActiveToolSets` (16) | removed; there is no limit on loaded tools |
  | Event `toolset_activated` (`{toolset}`), `gotato.ToolSetActivated` | `tool_loaded` (`{tool, why}`, why is `names`, `query` or `call`; `gotato.ToolLoaded`) and `tool_unloaded` (`{tool, why}`, why is `compaction`; `gotato.ToolUnloaded`), both facts of the Record |
  | `session.Record.ActiveToolSets()`, `SetActiveToolSets`, `gotato.ActiveToolSetSource` | `LoadedTools()`, `SetLoadedTools`, `gotato.LoadedToolSource`; the names are qualified tool IDs (`orders.lookup`), not ToolSet names |
  | `tool_sets` (names of ToolSets) in `session_created`, `compaction_ended`, `Compaction` and `Segment` JSON, CLI `context compact --json` `compaction.tool_sets` | `tools` (qualified tool IDs); `Segment.Tools` holds the loaded tools its stretch called |
  | every ToolSet resolved on its first activation, and a failure reached the model as a failed result | every ToolSet is resolved when the Session is built and again at the start of every Run, because the catalog needs its tool names; one that does not resolve is skipped (see below), and a canceled context fails with `canceled` |
  | a tool of a ToolSet could not be switched off for a Session | `gotato.WithToolFilter(keep)`; the Runner sets it from `gotato.tool.<id>` for every tool of every ToolSet |
  | `Runner.Tools` with a Session listed the Agent's tools, `read_segment` and `read_result` | adds `search_tools` (its description is the catalog) and the tools the Session has loaded, in `Runner.Tools` and `gotato tools list --session` |

  `gotato.WithToolSearcher(searcher)` plugs in a team's own search: `ToolSearcher.Search(ctx, query, tools []ToolSpec) ([]string, error)` returns IDs best first. The `toolregistry` package is unchanged: it is a `ToolSource` for tools switched on and off by the application, and never served `activate_toolset`.

- **The gateway sends deferred tools natively: the tool list stays the same while tools load.** The new key `deferred_tools` (`Config.DeferredTools`) is `native` or `list`. The default is `native` on `anthropic-messages` (every catalog tool has `defer_loading: true`, and a load is a `tool_reference` block in the `search_tools` result) and on `openai-responses` (`search_tools` is a client-executed `tool_search`, a catalog tool is a function with `defer_loading: true`, and a load is a `tool_search_output` item); `openai-chat-completions` always uses `list`. A request carries `ModelRequest.LoadedTools` and `ModelRequest.UnloadedTools`, and a Tool Result carries `ToolResult.Loaded`; all three are additions. **Breaking** for a team on those two wire APIs whose gateway refuses the native form:

  | Before | After |
  |---|---|
  | a load added the tool to the tool list on every wire API | the tool list is fixed while tools load on `anthropic-messages` and `openai-responses`; set `deferred_tools: list` for the old behavior |

- **The MCP adapter, `adapter/mcp`.** A Go module of its own (`github.com/jinhuang712/gotato/adapter/mcp`) on the official MCP Go SDK v1.8.0: one MCP server is one `ToolSet`, over streamable HTTP or stdio, with the Effect from the tool's annotations, slimmed schemas, and the content mapped to Parts ([FEATURES.md → G-F12](FEATURES.md#g-f12--the-mcp-adapter-)). An addition; the root module imports nothing of it.

- **A ToolSet that fails to resolve is skipped, and the catalog is read again at every Run.** An MCP server's `list_changed` takes effect at the next Run, also on a warm Session. A loaded tool that its ToolSet no longer carries is unloaded with `tool_unloaded` (`why: gone`); the Event's `why` gains the value `gone`. A skipped ToolSet is warned with a stored `extension_failed` Event (`stage: "toolset:<name>"`, `code: tool_resolution_failure`). No new Event kind, field or code, so HTTP and the CLI report it as they report `extension_failed`. Step 5 of Phase 7. **Breaking** for code that relied on the failure:

  | Before | After |
  |---|---|
  | a ToolSet that did not resolve failed `NewSession` with `tool_resolution_failure` | `NewSession` succeeds; the ToolSet is listed as `(unavailable now; no tools)` and an `extension_failed` Event (`toolset:<name>`) warns at each Run it stays down |
  | a ToolSet that returned an invalid or duplicate Tool failed `NewSession` with `invalid_argument` | the same ToolSet is unavailable; a bad name, a dotted name or a duplicate ToolSet name still fail `NewSession` |
  | the catalog was fixed for the life of the Session | the catalog is fixed within a Run and read again at the start of the next |

### Messages from any endpoint

Phase 8, step 3 of [ROADMAP.md](plan/ROADMAP.md) ([FEATURES.md → G-F26](FEATURES.md#g-f26--steering-a-run-)). All of it is addition, except two answers that change for a Stop and for a message left behind, in the table.

- **A message that arrives while a Run is ending is not answered `busy`.** `run_ended` comes a few milliseconds before the Run saves and releases its Session; a message sent in that window (a chat bot replying to `run_ended`, say) found the Session held and was refused. `Runner.Send`, and so `POST /v1/sessions/{id}/messages` and `gotato session message`, now waits for the release, bounded by the caller's context, and starts the next Run. A Run on another instance still answers `not_writer`; a plain `Runner.Run` on a busy Session is unchanged. A Stop in the window finds the ending Run and returns nil; a notice already waited for the Session.

  | Before | After |
  |---|---|
  | a message in that window: `busy` (HTTP 409), and the message was lost | the message runs in a following Run (HTTP 200 with `run`) |

- **Steer, FollowUp, Interrupt and Stop reach a Run through the Runner and every endpoint.** A Run going on the instance takes the message at its own point; with no Run going, the message starts one.

  | Surface | Message | Stop |
  |---|---|---|
  | Go | `Runner.Send(ctx, service.MessageRequest{SessionID, Kind, Text})` → `MessageResult{SessionID, MessageID, Queued, Run}` | `Runner.CancelSession` |
  | HTTP | `POST /v1/sessions/{id}/messages` `{"kind"?, "text"}`: 202 `{"session_id","message_id","queued":true}`, or 200 `{"session_id","queued":false,"run":{…}}` | `POST /v1/sessions/{id}/cancel` |
  | CLI | `gotato session message <id> [--kind steer\|follow_up\|interrupt] "text"` | `gotato session cancel <id>` |

- **`Interrupt`.** `gotato.Session.Interrupt(message)` cancels the model call in flight, keeps the text it had sent as a Cut-off reply, lets a running tool finish, and starts the next Turn of the same Run with the message. `gotato.SourceInterrupt` is the `source` of its `message_queued` and `message_committed`. It uses the Steer queue and `MaxSteerMessages`.
- **`not_writer`.** A message or a Stop for a Run that another live instance writes is not run and retryable, with the writer's advertised address. `gotato.ErrNotWriter` joins `gotato.ErrorCodes()`; `service.WriterOf(err)` reads the address.

  | Surface | Answer |
  |---|---|
  | HTTP | 409, `Gotato-Not-Run: true`, header `Gotato-Writer`, body `{"code":"not_writer","not_run":true,"retryable":true,"writer":"…"}` |
  | CLI | exit 1, `{"error","exit_code":1,"code":"not_writer","not_run":true,"retryable":true,"writer":"…"}` |

  The address is optional (`service.Config.Address`, `gotato serve --advertise`). A `run_rejected` Instance Event with the reason `not_writer` reports the refusal.
- **Forwarding, opt in.** `httpapi.WithForwarding(client)` (`--forward`) sends the request to the writer's address once, with the marker `Gotato-Forwarded: true`, and returns its answer. A marked request is never forwarded again. A forward that cannot connect answers HTTP 502.
- **Stores carry the address.** `session.InstanceRow` and `session.InstanceState` gain `Address`, and the `storetest` suite checks that a store returns it. A `session.Store` adapter that persists the whole row needs no change; one that copies fields must copy it. `session.Record.Holder()` reports the Claim and whether its holder is using the Record.
- **A message left in a queue is reported.**

  | Before | After |
  |---|---|
  | a Steer or FollowUp still queued when its live Session closed was discarded with no Event | in the Runner, a message queued as its Run settles runs in a following Run on the same Session and instance, in acceptance order, its ID kept (`gotato.WithQueueHandoff`, `Session.TakeQueued`, `Requeue`, `DropQueued`); dropped with `message_dropped` only for `run_ended` (stopped or canceled), `draining`, `writer_failed` or `queue_full`. A Go `gotato.Session` without the option still drops them at the end of its Run (`run_ended`), and `session_closed` covers one left when it closes |
  | `Runner.CancelSession` and `POST …/cancel` on a Session whose Run another live instance writes: `invalid_state` ("no active run"), 409 | `not_writer`, 409, with the writer's address |


### Runtime notices

Phase 8, step 4 of [ROADMAP.md](plan/ROADMAP.md) ([FEATURES.md → G-F29](FEATURES.md#g-f29--runtime-notices-)). Most of it is addition; the two breaks are in the table.

- **A notice is a Message of its own kind.** `gotato.RoleNotice` and `Message.Notice` (`NoticeInfo{Kind, Source, CausedBy}`) tell the model a fact the user did not say. It waits in the queue beside the Steer queue, goes in right before the next model call, and starts no Round and no Run. The request carries it as `<notice kind="…">…</notice>`, with the text escaped, and a request that holds one ends its system part with the fixed sentence about the four tags.
- **A service sends its own.**

  | Where | Call |
  |---|---|
  | Go | `Session.Notice(ctx, kind, text)`; `Runner.Notice(ctx, service.NoticeRequest{SessionID, Kind, Text})` → `NoticeResult{SessionID, Queued, Number, MessageID}` |
  | HTTP | `POST /v1/sessions/{id}/notices` `{"kind", "text"}`: 202 `{"session_id","queued":true,"number","message_id"}` |
  | CLI | `gotato session notice <id> --kind K "text"` → `{"session_id","queued":true,"number","message_id"}` |

  A Run going on another live instance answers `not_writer`, and is forwarded when forwarding is on, as a message is. Gotato's own kinds (`continue`, `time_away`) are reserved. `CoreLimits.MaxNotices` (16) bounds the queue.
- **A notice outlives its instance.** The Record keeps the notices that were queued and neither committed nor dropped (`session.Record.PendingNotices`, and `Summary.Notices` in the store's summary); a Run on any instance queues them again when it starts. A notice sent with no Run going takes the Session over for the one write of its `notice_queued`. `Summary.LastRunEnd` and `session.Record.LastRunEnd()` keep the Session's last busy time the same way. Only a service's notice is kept there; Gotato's own (`time_away`) is worked out afresh at each Run. **For store-adapter authors:** a `session.Store` adapter that copies the `Summary` field by field must copy `Notices` and `LastRunEnd`; the `storetest` suite now fails an adapter that loses either one across Create, Append, Load and List.
- **`time_away`.** A message that starts a Run after the Session was idle longer than `service.Agent.TimeAwayAfter` (30 minutes by default; a negative value turns it off) is followed by a notice with how long it has been and the time now, in the zone of `Compact.Location` (UTC by default). The CLI sets it with `--time-away 2h|off` or `GOTATO_TIME_AWAY`.
- **Gateway.** All three wire APIs take a notice as tagged text in a user turn (Anthropic Messages joins it to the adjacent user turn, after the Tool Results), so it starts no Round and the reasoning in flight stays. There is no native form ([FEATURES.md → G-N25](FEATURES.md#g-n25--a-notice-has-no-native-form)).
- **Compaction.** A Segment keeps a notice from a service word for word, as `<notice kind="…">` with its text escaped, and leaves Gotato's own out.

  | Before | After |
  |---|---|
  | the output-limit continuation (`MaxTruncatedContinuations`) was a user Message with `TruncatedContinuationText`, so it started a new Round and Claude and OpenAI lost the cut reply's reasoning | a `continue` notice with the same text (`gotato.TruncatedContinuationText`); `notice_committed` names the `reply_ended` that caused it in `message.notice.caused_by`; the Round stays |
  | `gotato.SourceContinuation`, the `source` of that `message_committed` | removed; the continuation is a `notice_committed`. `gotato.SourceNotice` is the `source` of a `message_dropped` or `message_rejected` for a notice |

- **The notice kinds `settings_changed` and `run_interrupted` are removed, and no native form of a notice is built.** Gotato never sent either kind, and a service that used one of the names was refused. They are not built ([G-N24](FEATURES.md#g-n24--no-notice-for-a-settings-change-or-an-interrupted-run), [G-N25](FEATURES.md#g-n25--a-notice-has-no-native-form)).

  | Before | After |
  |---|---|
  | `gotato.NoticeSettingsChanged` and `gotato.NoticeRunInterrupted` were reserved: `Session.Notice`, `Runner.Notice`, HTTP and the CLI refused `settings_changed` and `run_interrupted` as `invalid_argument` | removed; only `continue` and `time_away` are reserved, and the two names are free for a service |
  | FEATURES.md planned a `system` message in the middle of a conversation (Anthropic Messages) and a `developer` message (OpenAI Responses) for a notice | not planned; a notice is tagged text in a user turn on every wire API, as it was. The gateway never had a setting for it |

### Limits

- **The core counts no Turns or Tool calls, caps no Tool Result and sets no Run deadline.** A limit stays only where it guards a real failure ([DESIGNS.md → A limit guards a real failure](DESIGNS.md#a-limit-guards-a-real-failure--2026-10-25)). A Run ends by its model's final reply, by its `ctx`, by a `TurnStopper` or by the compaction ceiling. Step 1 of Phase 7 in [ROADMAP.md](plan/ROADMAP.md). No old name is kept.

  | Before | After |
  |---|---|
  | `CoreLimits.MaxTurns` (32): the 33rd Turn failed the Run with `limit_exceeded` | removed; a team that wants a cap on Turns writes a `TurnStopper` (`ExampleTurnStopper`, [FEATURES.md → G-F01](FEATURES.md#g-f01--agents-and-runs-)) |
  | `CoreLimits.MaxToolCalls` (128): the next call failed the Run with `limit_exceeded` | removed; the same `TurnStopper` counts calls from the `TurnSnapshot` |
  | `CoreLimits.MaxToolResultBytes` (4 MiB): a larger Tool Result failed the Run with `limit_exceeded` | removed; a result over one page (32 KiB) is spilled into the Record and the Context holds a preview and a ref (*Large Tool Results*, below). `MaxMessageBytes` still refuses a Message too large to commit, but a text result no longer reaches it |
  | `CoreLimits.RunDeadline` (5 minutes) | removed; a Run's deadline in the core is its `ctx` |
  | `gotato.WithDeadlines(run, model, tool)` | `gotato.WithLimits` with `ModelCallDeadline` and `ToolCallDeadline` (see *One way to give a Session its tools*) |
  | `service.RunRequest.Timeout` replaced the Agent's `RunDeadline`, through `Limits` | it replaces `service.Agent.RunTimeout`, new, 30 minutes by default (`service.DefaultRunTimeout`; negative means no deadline). A sooner deadline on the caller's context still wins |
  | `read_segment` pages were sized from the Agent's `MaxToolResultBytes` | always `modelctx.DefaultReadSegmentPageBytes` |

  `MaxMessageBytes`, `ModelCallDeadline` and `ToolCallDeadline` are unchanged (`MaxActiveToolSets` goes in *Tool search*, below). `limit_exceeded` stays for the message and transcript limits, the queues and the compaction ceiling. No JSON field, CLI flag or gateway key carried the removed limits.

### Large Tool Results

- **A Tool Result over one page is spilled into the Record, and the model reads the rest with `read_result`.** The text of a result longer than 32 KiB (`gotato.SpillPageBytes`, about 8K tokens) is stored in full as a `tool_result_spilled` log Entry, written just before the Tool Result Message. The Message, and so every Context built from it, holds the first 8,000 bytes (about 2K tokens, cut on a rune boundary), the ref `result:<call_id>` and a line that says how to read the rest. `read_result(ref, page?)` is offered to every Agent that has a tool, declares `read`, and pages as `read_segment` does (the paging is now `gotato.Paginate`, shared). A live Record keeps the bodies it wrote or read (stored ones are dropped past 16 MiB, oldest first); a load reads no body. A result of several MiB no longer fails the Run on `MaxMessageBytes`. An image or a file in a result stays in the Message. A Session over a Transcript that is not a `session.Record` or `session.Writer` spills nothing (`gotato.SpillKeeper`).

  | Before | After |
  |---|---|
  | a Tool Result over 4 MiB failed the Run (`limit_exceeded`, `MaxMessageBytes`); a smaller one stayed in every request | the text past 32 KiB is spilled; the Message holds a preview and `result:<call_id>` |
  | `ToolResult` had no `spilled` field | `ToolResult.Spilled` (`{"seq","bytes"}`, omitted when empty) in every Message JSON: HTTP, `session show --json` |
  | no `tool_result_spilled` Event | `tool_result_spilled` (`call_id`, `tool_id`, `bytes`, and `body` in the stored Entry only): `gotato events`, HTTP events and the Go feed carry it without `body` |
  | an Agent's tool list was its tools (and `read_segment` once it had a Segment) | adds `read_result` whenever the Agent has a tool, in `tools list --session`, `context inspect` and every request; `service.Runner.Tools` lists it |
  | `FileStore.Log(after)` opened every write of the Record | it skips the files that end at or before `after` |
  | the demo model answered `use-tool` only | also `use-big-tool`, which returns a result over one page |

  The `storetest` suite gains a check that a log Entry of a megabyte is kept whole, read at its position, and not loaded; every `session.Store` adapter must pass it. Every change here is an addition.

### Access control

- **Every request-driven call reads through the store, so a store wrapper can refuse a caller** ([FEATURES.md → G-N22](FEATURES.md#g-n22--access-control-belongs-to-applications)). `Runner.CancelSession`, `Runner.Send` and `Runner.Notice` read the Record with the request's `ctx` before they act on a Run live on the Instance.

  | Before | After |
  |---|---|
  | canceling, steering or sending a notice to a Run live on this Instance touched no store, so a wrapper could not refuse the caller | the call reads the Record first, and a wrapper's `not_found` is returned as it is |
  | `CancelSession` (HTTP `POST /v1/sessions/{id}/cancel`) for a Session the store does not know: `ErrNoRun`, HTTP 409 `invalid_state` | `not_found`: HTTP 404, CLI exit 3 |

### Errors

- **Every error says whether it ran and whether a retry may succeed, and the runtime decides.** `RuntimeError` gains `NotRun` (JSON `not_run`), and `gotato.NotRun(err)` and `gotato.Retryable(err)` read both through wrapped errors. `gotato.ErrorCodes()` lists every code. HTTP and the CLI keep one table from each code to their status, and a test fails when a code has none. A model failure the provider calls transient (a rate limit, an overload, a broken stream) is a `model_failure` with `retryable: true`; the gateway's `Error` says so through `IsRetryable()`. **Breaking:**

  | Before | After |
  |---|---|
  | a draining instance refused a Run with `ErrDraining`, code `invalid_state`: HTTP 409, no mark | code `draining`: HTTP 503 with `Gotato-Not-Run: true`; `retryable: true` |
  | `invalid_state` for a draining instance | `draining`; `invalid_state` stays for the other state conflicts |
  | `session.ErrNotFound` was a bare sentinel, with no code: HTTP 404 and the CLI's exit 3 found it by `errors.Is`, an error body had no `code` | it carries the new code `not_found` (`errors.Is(err, session.ErrNotFound)` still works): 404 with `"code": "not_found"`, exit 3 |
  | only `overloaded` was marked not run, over HTTP | the runtime marks every refusal before a Run starts: `draining`, `overloaded`, `busy`, `not_found`, the `MaxActiveRuns` capacity refusal (429), an empty prompt, an unknown Agent. The header is set on any status, 409 and 404 included. |
  | a Run whose last writes failed: `status: "completed"` and the error `ErrNotPersisted` | a Run whose reply or end was not written settles as `status: "failed"` with `result.error` set (`store_failure`), and `ErrNotPersisted` is returned. A Run whose only failed write is the release of the Session stays `completed`, with no error: all its Events are stored, and the failed release is told by `session_saved` with `failed: true` and `store_write_failed`. `completed` never carries an error, and every other status always does. The CLI exits 1 for a failed write, as before. |
  | HTTP error body `{"error","code","message","not_run"}` | adds `retryable`; the 500 and 409 of a Run that was not saved carry `code` and `retryable` too |
  | CLI failure JSON `{"error","exit_code"}` | adds `code`, `not_run` and `retryable` |

 

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
- **The compaction's summary writer is a Recap writer.** The name follows [GLOSSARY.md](GLOSSARY.md); no old name is kept. **Breaking:**

  | Before | After |
  |---|---|
  | `modelctx.Summarizer`, method `Summarize(ctx, []Message)` | `modelctx.RecapWriter`, method `WriteRecap(ctx, RecapRequest)` (see the entry on the model's Recap below) |
  | `modelctx.TruncateSummarizer`, `modelctx.ModelSummarizer` | `modelctx.TruncateRecap`, `modelctx.ModelRecap` |
  | `CompactOptions.Summarizer`, `CompactPolicy.Summarizer` | `CompactOptions.Recap`, `CompactPolicy.Recap` |
  | `gotato.Compaction.Summarizer`, JSON `summarizer` (`compaction_ended`, the CLI's `compaction`) | `gotato.Compaction.Recap`, JSON `recap` |
  | `gotato context compact --summarizer truncate\|model` | `gotato context compact --recap truncate\|model` |


### Runtime

- **A Session is compacted before any model call, past the ceiling and on an overflow.** The Loop calls a `gotato.TurnPreparer` (`PrepareTurn(ctx, PendingTurn) (TurnPrepared, error)`) on the Session's goroutine before every model call, with no Tool call open, and once more when the provider refused a call as too long before any output. `modelctx.AutoCompactor` is a `TurnPreparer`. It compacts when the Context is past the ceiling (`past_ceiling`), mid-Run included, and on an overflow (`overflow`), and sends the call again once. The ceiling is the model's `context_window` less max(10% of it, 16K tokens) when the gateway knows the window, so a model with a window is now compacted by default; `CompactPolicy.Ceiling` and `gotato.compact_ceiling` override it, and a negative `CompactPolicy.Ceiling` turns it off. A Turn compacts at most once. Still past the ceiling after it, the Run ends with `limit_exceeded`; still refused, with `context_overflow`. The size is the usage the last model call reported plus an estimate of what came after, and a compaction clears the usage of the Messages it keeps. The Runner installs the compactor for every Agent. `ModelRecap` reads the request about to be sent from the cache. **Breaking** for a library user of `RunPreparer` or `CompactPolicy.Context`:

  | Before | After |
  |---|---|
  | `RunPreparer.PrepareRun(ctx, Transcript) error`, called once at the start of a Run, before its prompt was committed | removed; `TurnPreparer.PrepareTurn(ctx, PendingTurn) (TurnPrepared, error)`, called before every model call, with the prompt committed |
  | `modelctx.AutoCompactor` was a `RunPreparer` and compacted only at the start of a Run, and only with a `Ceiling` above 0 | a `TurnPreparer`; with a ceiling of 0 it uses the one the window gives, and with neither it compacts only on an overflow |
  | `CompactPolicy.Context`, set by the Runner from a second, inspect-only Session | removed; the compactor reads `PendingTurn.Request` |
  | the Run before the one that starts stayed in the kept tail | the same: the Run that is starting does not count as the latest (`CompactOptions.CurrentRun`) |
  | a provider's refusal as too long ended the Run with `context_overflow` | the Session is compacted and the call sent again once; a second refusal ends the Run with `context_overflow` |
  | a Run whose Context was past a ceiling it could not reach after the compaction went on | it ends with `limit_exceeded` when the estimate of the Context is still past the ceiling |
  | `Message.Usage` of a kept Message stayed after a compaction | it is cleared in the kept tail |

- **The model reads a Segment back with `read_segment`.** Once a Session has a Segment, the model gets a built-in tool, `read_segment(segment, page?)`, that returns the Segment's Messages from the Record's log, tool calls and tool results included and reasoning left out, 32 KiB of text a page (or half of the Agent's `MaxToolResultBytes` when that is lower). Its Effect is `read`. A prefix on each Message says whose it is (`[user]`, `[assistant]`, `[call tool]`, `[tool tool]`, `[notice]`), the four tags are escaped, and a line that starts with such a prefix gets a backslash in front. When the log no longer holds the Entries, it says the Messages were removed. `modelctx.SegmentTools(record, store, opts)` is the `ToolSource` a library user installs; the Runner installs it for every Session, and `Runner.Tools` with a Session lists it. `session.CommittedMessages` and `session.EntryLog` read a range of the log back. A Session's tool list gains `read_segment` once it has a Segment, which changes the request at that compaction, as a compaction does anyway.
- **The oldest Segments merge when they pass their budget.** The Segments' budget is half the ceiling in force, or `modelctx.DefaultSegmentsBudget`, 32K tokens, with none (`CompactPolicy.SegmentBudget` and `CompactOptions.SegmentBudget` set another). `modelctx.MergeSegments` takes the oldest Segments, at least two, until the rest fit in half the budget, into one merged Segment, `gotato.Segment{Merged: […]}`, which opens the Context and is named by its range, `<segment n="1-3">` and `<recap n="1-3">`. The Segments it replaced stay in the log and are read with `read_segment` by their own numbers, also in a fork. The merge is a Checkpoint of its own (`Record.MergeSegments`), recorded as a compaction with `merged`, and keeps the kept tail and the ToolSets as they were. It runs right after `Compact` closes a Segment (`Result.Merge` says what it did), at the start of a Run when the Segments are past their budget (`compaction_started` with the new reason `past_budget`), and on an overflow. Its Recap is written from the Recaps of the Segments alone: `ModelRecap` is a `modelctx.MergeRecapWriter` that sends one message with the Recaps and nothing else, under the five-heading rules; with `NoRecap`, no writer, `TruncateRecap` or any writer that is not a `MergeRecapWriter`, `modelctx.MergeDigest` writes a digest by code, recorded as `digest`. A merge whose Recap or write fails leaves the Segments unmerged (`compaction_failed`), and the Run goes on, except on an overflow, where it ends with `context_overflow`.
- **An overflow merges the Segments sized by the limit the refusal names.** `gotato.PendingTurn` gains `Refusal`, the error that refused the request. The gateway's `Error` gains `ContextLimit()`, which reads the limit in tokens from the wording of the Anthropic, Chat Completions and Responses refusals (0 when it names none), and the compactor sizes the merge to the smaller of that and the budget.

  | Before | After |
  |---|---|
  | `modelctx.Compact` and `Runner.Compact` closed one Segment and left the Segments as they were, however many there were | they also merge the oldest Segments when the Segments pass their budget; `modelctx.Result` gains `merge`, and `POST /v1/sessions/{id}/compact` and `gotato context compact --json` carry it. A Session with fewer than 32K tokens of Segments (half the ceiling in force) sees no change |
  | `gotato.CompactionWhy` had `on_demand`, `past_ceiling`, `before_cache_expires`, `before_unload`, `overflow` | adds `past_budget` |
  | `gotato.Segment` held a stretch the compaction closed | adds `Merged`, the Segments a merged Segment stands for. `gotato.Compaction` adds `Merged` |
  | `Record.CheckpointSegments` and `CloseSegmentAndCheckpoint` required the numbers to ascend | a merged Segment may be listed first, whatever its number, and only first; `Record.MergeSegments` closes one and lists it |
  | an overflow on a Session whose Segments were past the limit closed one more Segment and was refused again | the oldest merge in the same Turn |

- **The default `MaxMessages` is 10,000, where it was 1,000.** The limit counts the Messages a load reads, and once a Session has Segments those include the Messages that render them, so a Session of short messages would have reached 1,000 long before its Context grew large. The size of the Context is bounded by the ceiling in tokens and by `MaxTranscriptBytes`, which stays at 32 MiB. A Session that set `MaxMessages` itself keeps its value.
- **An Agent can cap the Effect of its tools.** `gotato.WithMaxEffect(effect)` and `service.Agent.MaxEffect` name the highest Effect a tool may have. A tool above the cap is never shown to the model, including the tools of a ToolSet activated later, and a call to it fails as a call to an unknown tool. A tool that declares no Effect counts as destructive, so any lower cap hides it. A Session cannot switch on a hidden tool: `Runner.Tools` does not list it, and `Runner.SetToolActive` reports it as unknown. There is no cap by default, so an Agent that names none behaves as before. An unknown Effect fails `NewSession` and `service.New`.
- **The tool that activates a ToolSet declares `read`.** It changes only which tools the model sees, so every cap keeps it. Its `ToolSpec`, and the `ToolUse` a `PreToolUse` extension gets for it, now carry `EffectRead` instead of no Effect.
- **`context_built` counts are numbers, and the ContextBuilder's metadata has its own key.** The Session merged the builder's `Metadata` into the payload under the same keys, so `FullHistoryContext`'s `"source_messages": "3"` replaced the Session's count, and `selected_messages` and `dropped_messages` always arrived as strings. The payload now has the shape of the `context inspect` report: `strategy`, `messages`, `source_messages`, `selected_messages`, `prefix_hash`, `prefix_messages`, `system_bytes` and `tools` are the Session's own, each count a number, and the builder's `Metadata` sits under `metadata`, as its strings. The same payload reaches HTTP and `gotato events`. **Breaking:** read `dropped_messages`, and any key your own builder sets, from `metadata`.

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

- **A Run that fails during a tool batch still answers every tool call.** A Run could fail after its assistant message was committed with tool calls but before every result was: `MaxToolCalls` reached in preflight, a tool result over `MaxToolResultBytes`, a failing `PreToolUse` or `PostToolUse` extension, or a failing observer. Those calls stayed without a result, and every provider refuses a request like that, so no later Run of the Session could succeed. The failed Run now commits a closing result for each open call, `failed`, or `canceled` when the Run was canceled, with a `SafeError` that starts with the error code, and emits `tool_result_committed` for it. These closing results do not count against the Message and transcript limits, since the Run may be ending on one of them.
- **An aborted Run starts no more tools.** In a sequential batch, the tools after the one running at `Abort` were still started with the canceled context, so a tool that ignored its context carried out its side effect after the Run was stopped. They are now settled as `canceled` with `Executed` false, and never started. Each of them, and each tool canceled while running, now gets its `tool_execution_end` Event.
- **A tool that was running at cancellation is recorded as executed.** Its result was `canceled` with `Executed` false, even when the tool had already finished, or had started and could have done its work. A tool whose executor started now keeps `Executed` true, and a result that had already arrived keeps its content. `Executed` false now means only that the executor never ran: the call was blocked, rejected, or never started.
- **A canceled Run in the Runner keeps its result.** `CancelRun`, `CancelSession` and `Drain` canceled the context the live Session's `Prompt` waited on, so `Prompt` returned before the Run settled, and every canceled Run came back with an empty `run_id`, no Turns and no usage, over HTTP and the CLI too. Once the live Session is built, the Runner now stops a Run only through `Abort`, and the result carries the Run's ID, usage and metrics, as the Session's Run record does.
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
- **A Record keeps no text deltas.** The reply they make up is in its Message. **Breaking:** `GET /v1/sessions/{id}/events` and `gotato events` do not list text deltas (`message_update` before the rename, `text_delta` now).
- **`Record.Set` with an unchanged value changes nothing.** It no longer moves `UpdatedAt`.
- **Text that imitates Gotato's own tags is escaped.** `ForModel` writes the `<` that starts or ends `<notice>`, `<segment>`, `<tool_calls>` or `<recap>` (any case) as `&lt;` in user and assistant text, Tool Result content and `SafeError`, so a user, outside data or the model's own words cannot speak as the system. Before: a user message `<recap>x</recap>` reached the model unchanged. After: `&lt;recap>x&lt;/recap>`. New in the root package: `EscapeTags`, the tag names, `MetaTag` (a Part with this metadata is Gotato's own tagged text and is left alone) and `TagsSentence`, which `AssembleRequest` adds to the system part only when a request holds a marked Part. Requests without those are byte for byte as before.

### Sessions and stores

- **A writer of a deleted Record is refused by the Record created again.** `Store.Append` took only a position, so a writer that loaded a Record, which was then deleted and created again under the same ID at the same length, wrote into the new Record, and the new Record's Summary named the old epoch. On a file store shared by two processes, a position could be written twice. The Summary that `Append` already takes now carries the writer's epoch (`Summary.RecordEpoch`, read with `Summary.Epoch`), and a store refuses with `ErrNotFound` when the Record's epoch is another. The signature does not change. `MemoryStore` and `FileStore` do it, and `testkit/storetest` adds a check, so an adapter that compares only the length now fails the suite. The file store does it with a directory per epoch, so no window is left between a writer's check and its write (next entry). **Breaking** for a store adapter only:

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
- **The file store keeps a directory per Record, named by its epoch,** with one file per write: `<id>/<epoch>/<position>.json`, where `<epoch>` is a hash of the Record's epoch. A write is linked into place, so two processes on one directory can never both write a position, and a crash never leaves a torn write. A Record that is deleted and created again under the same ID has another epoch, so another directory, and a writer of the old Record finds none to write into and gets `ErrNotFound`, however late it looks: there is no window left between its check and its link. `Create` builds the directory under a temporary name and renames it to `<id>`, so of two creators of one ID only one wins. A Record of an earlier layout, a directory `<id>/<position>.json` or a session document `<id>.json`, is **not read and not migrated**: Gotato has made no release, and the file store refuses it with `session.ErrOldLayout` (it names the path) from `Load`, `Log`, `Create` and `List`, and never reads it as a Record or as a missing one. The head cache of the store no longer compares inode numbers, modification times and sizes, which only guarded a Record created again in the same directory. **Breaking:**

  | Before | After |
  |---|---|
  | `<id>/<position>.json`, and a session document `<id>.json` of schema version 1 read as a Checkpoint and moved into a directory by its first write | `<id>/<epoch>/<position>.json`; a store directory written before is refused with `ErrOldLayout`: move the store directory away, or remove the old sessions' files by hand (`FileStore.Delete` removes one; `gotato session delete` first loads the session, so it refuses too) |
  | a Record deleted and created again between a writer's epoch check and its link took the old writer's batch | the old writer gets `ErrNotFound`, and the new Record keeps only its own Entries |
- **The Runner writes as an Instance.** `service.Config.Instance` names it, and is generated when empty; `Runner.Instance` returns it. `Runner.Session` and `Runner.Sessions` read Sessions. `RunRequest.Update` sets metadata as the Run's first write. `CreateSessionExclusive` and `Fork` with an explicit ID rely on the store's `Create`, so they stay exclusive across Instances. `session_saved` sums every write the Run made since the Session was opened, and adds `writes`.
- **New error codes `conflict` and `store_failure`.** HTTP answers 409 and 503. A Run whose result another writer fenced answers 409 instead of 500.
- **`gotato run --no-save` no longer stores settings.** `--instruction` and `--compact-ceiling` given with `--session` and `--no-save` apply to that run only; without `--no-save` they are the run's first write, under its Claim.
- The session document gains `seq`, and a summary gains `seq` and `running`.

- **Every step of a Run is written as it happens.** A Transcript may implement the new `gotato.Journal`, whose `Step` the live Session calls after the prompt, before each model call, for every second or 4 KiB of a streaming reply, after the reply, before each group of Tools runs, after each Tool Result, and after each Steer, FollowUp or continuation. A failed step fails the Run with its code (`conflict` or `store_failure`), and a failed model-call step stops the Run before the model is called. `session.Writer` is a Transcript and a Journal, and the Runner gives it to the live Session, so a crash loses at most the step in flight. A Run makes more writes: about one per step, against one per Run before.
- **Tool Results are committed after each group of Tools runs,** in call order, instead of after every Tool of the Turn has run. **Breaking:** with several Tools in a Turn, a group's `tool_result_committed` Events now come before the next group's `tool_execution_end`.
- **A Session opened after its writer stopped answers every Tool call.** A call that started and has no result gets a failed result whose text says the Tool's outcome is unknown (`session.StoppedToolResult`), and a call that never started one that says it was not run (`session.NotRunToolResult`). They are worked out each time the Session is loaded, and never written. `session.View` also shows the parts of a reply that was streaming when its writer stopped as a cut-off reply.

- **One Instance runs a Session at a time.** A Runner keeps a Liveness row in the store, renewed every third of `service.Config.Liveness.TTL` (10 seconds by default) and timed by the store's clock. A Claim counts only while its Instance's row is alive. Opening a Session that another Instance holds takes it over when that Instance is dead, or alive and idle; when it is alive and using the Session, draining or not, the Run waits up to `Liveness.Wait` (5 seconds by default), and is then refused with `busy` and a `retry_after_ms` detail, which HTTP sends as 409 with `Retry-After`. Before every write, a Runner checks that its own row was renewed within the TTL less a margin, and otherwise writes nothing, so an Instance that was paused or cut off stops before it calls the model. `Runner.Close` removes the row, so another Instance takes its Sessions over at once, and a closed Runner writes nothing more; `gotato` and `gotato serve` call it when they stop. A Session's summary gains `busy`, set by the Claim from the moment a writer opens it until it releases it (`Writer.Release`). `session.WithGuard` and `session.WithHolderCheck` are the Writer options that do this.

- **Two instances that open one idle Session at once no longer fail half their Runs.** The instance that held the Session reopened it without writing anything, so the store did not mark it busy, and another instance claimed it at the same moment; the holder's first write then failed with `conflict`, 409 over HTTP, before any model call. The retry meant for a lost race never ran either, because the check for a lapsed row matched every conflict. Now the holder writes a busy mark, the new log Entry `session.EntryBusy`, which takes the next position as a Claim does, so exactly one of the two writes lands, and the instance that loses waits for the other's Run. This adds one write to every Run on the instance that already holds the Session, about 8 ms with the file store on macOS.
- **An instance drains in order.** `Runner.Drain` first marks the Runner's Liveness row draining (`InstanceRow.Draining`). Then `Draining` reports true, so `/readyz` answers 503, and the Runner admits no new Run and takes no Session over from another Instance: both fail with `ErrDraining`. Creating or forking a Session still works, since it activates nothing. Every Run in flight finishes, saves and releases its Session. At the drain's deadline the Runs still going are stopped, saved and released, and each returns with the new `RunResult.Reconnect` (`retry_after_ms`, a wait between half a second and a second and a half) once every Run of the drain is released. Before, the row was never marked, and the Runs cut at the deadline came back as plain canceled Runs. `gotato serve` says `draining` and `stopped` on stderr.
- **An ended Liveness row is removed.** `Store.Beat` with a ttl of 0 removes the row instead of keeping it expired, and `Store.Instance` reads a removed row as not alive, so every Claim that names it ends. The memory and file stores remove it, so `.instances/` no longer keeps a file for every command that ran. `testkit/storetest` adds "an ended row is removed", and fails a store that keeps ended rows or reads a removed row as alive. **Breaking** for a store adapter:

  | Before | After |
  |---|---|
  | `Beat(ctx, row, 0)` ends the row, and a later `Beat` of that ID fails with `ErrExpired` | `Beat(ctx, row, 0)` removes the row, and its ID is unknown again; the Instance never beats again |

- **A ToolSet the model switches on stays on for the Session.** `toolset_activated` is now a fact of the Record, not a log entry: `session.Record.ActiveToolSets()` lists the names in the order they were switched on. A later Run, also on another Instance, starts with those ToolSets active, so its first request carries their Tools. `session_created` and `compaction_ended` gain `tool_sets`, so a Checkpoint keeps them, and `Fork` and `Document` copy them. A name no installed ToolSet carries, a ToolSet that no longer resolves, and a ToolSet past `MaxActiveToolSets` stay off. A ToolSet installed with `WithActiveToolSet` is the Agent's choice and is never switched off by this. A custom `gotato.Transcript` opts in by implementing `gotato.ActiveToolSetSource`. Before, the next Run began with every ToolSet inactive and the model had to activate it again. A Record written before this change restores nothing, since its activations were log entries. Resolving ToolSets while a Session is built can be canceled: the new `gotato.NewSessionContext(ctx, options...)` takes a context, `NewSession` calls it with `context.Background()` and cannot be canceled, and the Runner passes the Run's context. A context that ends during the restore fails the build with `canceled` or `deadline_exceeded`.
- **The Record keeps Segments, written once.** A compaction can close a stretch of history into a `gotato.Segment`: its number, its times, the range of Entries it covers (`SegmentSource`), the Messages that render it, a line per tool call, a Recap and the ToolSets it used. `Record.CloseSegment` appends it as its own `segment_closed` Event, and its Messages are never written again, because a provider's prompt cache reads their bytes. `Record.CheckpointSegments` then appends a `compaction_ended` Checkpoint that lists the Segment numbers and keeps the tail, and does not copy the Segments' Messages. A load reads the latest Checkpoint, resolves its numbers to the stored Messages, and goes on with the tail. The original Entries stay in the log, unread by the Context. `Record.Segments()`, `Tail()`, `NextSegment()` and `Position(id)` (the Entry number and time of each Message since the last Checkpoint) read it back, `Fork` copies the Segments with the parent's Session ID in their `Source`, and `Document` carries them. `Compaction` gains `segment`, `tokens_before`, `tokens_after`, `ceiling`, `recap_usage` and `tool_sets` (`recap` names the Recap writer, or `none`), and `CompactionWhy` gains `before_cache_expires` and `overflow`. Nothing closes a Segment yet; `Compact` still replaces the old part with one summary. **Breaking** for a store adapter: `Store.Load` also returns the `segment_closed` Entries from before the last Checkpoint (`Loaded.Segments`), which an adapter finds by `Entry.Kind` as it finds the Checkpoint; `testkit/storetest` checks it.

  | Before | After |
  |---|---|
  | `Loaded{Facts, Head, Summary}` | `Loaded{Facts, Segments, Head, Summary}`; with no Segments, `Segments` is empty and nothing changes |
  | `compaction_ended.messages` and `session_created.messages` hold every Message the Context starts with | they hold the Messages after the Segments the Checkpoint lists in `segments`; a Checkpoint with no `segments` is as before |

  **`gotato.Message` gains `RunID`**, the Run that committed it, so a Segment can group Messages by Run. The Loop sets it on the prompt, each reply (a cut-off one too), each tool result including the closing results of a failed Run, and each Steer, FollowUp and truncation continuation. A Message that did not come from a Run, such as history a Record starts with, has none, and so does any Message in a Record written before this change; both load with an empty `RunID`. It is stored in the Message's JSON as `run_id` (omitted when empty), so entries, Checkpoint tails, Segments, `Fork` and `Document` keep it. It is a record only: `ForModel` drops it, and no wire API encodes it, so a request and its `prefix_hash` are the same as before.

  | Before | After |
  |---|---|
  | `{"id":"m1","role":"user",…}` | `{"id":"m1","run_id":"run-…","role":"user",…}`; no `run_id` when empty |


- **A compaction closes a Segment built by code.** `modelctx.Compact`, the automatic compaction at the start of a Run, `Runner.Compact`, `POST /v1/sessions/{id}/compact` and `gotato context compact` close everything since the last Checkpoint, less a kept tail, into a `gotato.Segment`. The user's Messages and each Run's final reply stay word for word (one past 2K tokens is cut, with a line naming `read_segment`, which waits for a later step; an image or a file becomes a placeholder), each tool call is one line (`HH:MM tool {arguments cut at 200 characters, escaped} → ok|error code`, repeats counted, past 100 lines counted by tool name), and tool results, reasoning, a Run's other text and Gotato's own notices are left out. The Segment is appended as a `segment_closed` Event, then the `compaction_ended` Checkpoint that lists it, in one step. The ToolSets the Segment called and the kept tail calls stay active; the others the model switched on are switched off. `modelctx.SegmentSettings`, carried by `CompactOptions` and `CompactPolicy`, sets the zone of the times (UTC by default) and the limits. The Recap is the text of the configured `RecapWriter`, if any, and the Segment has none otherwise. **Breaking:**

  | Before | After |
  |---|---|
  | `Compact` replaced the Messages before the last `Keep` with one summary Message tagged `metadata.compaction = "summary"` | `Compact` closes a Segment and keeps a tail: the `compaction_started` Event is followed by `segment_closed` and `compaction_ended` |
  | `--keep` defaulted to 4 in the CLI, and `keep` to 4 in HTTP when absent or 0 | without `keep`, the tail is the latest Run's Turns, up to 20K tokens and at most a quarter of the window (or of the ceiling); `--keep N`, and `{"keep":N}` still name a number of Messages |
  | `CompactPolicy.Floor` defaulted to half the ceiling, and a Session's `gotato.compact_ceiling` reset it | `Floor` is the tail's budget in tokens when above 0, and 0 means the default tail; the Session's ceiling no longer resets it |
  | the cut moved to the next user Message | the cut moves to the next Message that is not a tool result (a Turn boundary) |
  | `CompactOptions.Recap` nil wrote a `TruncateRecap`; `gotato context compact --recap` defaulted to `truncate` | nil writes no Recap; `--recap` defaults to `none`, and `truncate` and `model` stay |
  | a Recap writer that failed failed the compaction (`compaction_failed`, an error) | the Segment is kept without a Recap and `compaction.recap` is `none`; a compaction fails only when its Record append fails or its Run is stopped |
  | `compaction.summary_message_id` was always present | it is omitted (a Segment has no summary Message) |
  | `modelctx.ErrHasSegments`: `Compact` refused a Record that holds Segments | removed: `Compact` closes the next Segment; it refuses a Record whose last Checkpoint holds one old summary with the new `modelctx.ErrOldSummary` (`invalid_state`: 409, CLI exit 1), and migrates nothing |

  `session.Record` gains `SetActiveToolSets`, which a compaction uses to narrow the ToolSets before its Checkpoint. The fix for the Segment's tool call lines is part of this: the arguments pass through `EscapeTags`, so an argument that holds `</tool_calls>` cannot end the block early.

- **The compact endpoints take `min_tokens`, and say why they closed nothing.** `CompactOptions.MinTokens`, `{"min_tokens":N}` and `gotato context compact --min-tokens N` skip a stretch since the last Checkpoint that is estimated below N tokens, so a service's idle timer does not pay for a stretch too short to be worth it. The result gains `reason` (`below_min_tokens`, or `nothing_to_close` when the kept tail covers the whole stretch). All are additions; the default, 0, always compacts. A negative `keep` or `min_tokens` is `invalid_argument` (HTTP 400, CLI exit 2).

- **The model writes the Recap, on the cached prefix, and it is off by default.** A `ModelRecap` sends the Context the next model call would send (the same instruction, tools, Messages, cache hints and key) and one plain user message after it that asks for the Recap with five headings. That message is never stored and carries no tag. The reply must start with one of the five `## ` headings, use no other `## ` heading, have `## What happened`, not call a tool, and not be cut off at the output limit (8K tokens, `ModelRecap.MaxTokens`, `gotato context compact --recap-max-tokens`). A reply that fails leaves the Segment without a Recap (`recap: none`), and the compaction's `recap_usage` still records the call. No default writes it: a team sets `Recap: modelctx.ModelRecap{}` on the Agent's `CompactPolicy` (the Runner gives it the Agent's model), `CompactOptions.Recap`, or `--recap model`. `RecapRequest`, `NoRecap`, `ModelRecap.MaxTokens`, `CompactOptions.Context`, `CompactPolicy.Context`, `ValidateRecap`, `RecapAsk` and `RecapModelRequest` are new. **Breaking** for a library user who wrote a `RecapWriter` or used `ModelRecap`:

  | Before | After |
  |---|---|
  | `RecapWriter.WriteRecap(ctx, []gotato.Message) (Message, error)` | `WriteRecap(ctx, modelctx.RecapRequest) (Message, error)`; the closed Messages are `request.Closed` |
  | `ModelRecap{Model, Instruction, Label}` sent the closed Messages as JSON in one user message, under an instruction of its own, and kept any reply with the prefix `Summary of earlier conversation:` | `ModelRecap{Model, MaxTokens, Label}` sends the next call's Context plus the ask, validates the reply, and keeps it as it is; `Instruction` is removed |
  | `ModelRecap` worked on a bare `Model` | it needs `CompactOptions.Context` (the Runner sets it); without one it fails and the Segment keeps no Recap |
  | `gotato context compact --recap model` | unchanged, plus `--recap-max-tokens N` |

- **A Checkpoint lists its Segments once and in order; a Segment can be closed and listed in one step; a Document carries the largest Segment number.** `Record.CheckpointSegments` refuses a number listed twice, a number listed after a larger one, and a number never closed, with `invalid_argument`, and changes nothing. The new `Record.CloseSegmentAndCheckpoint` closes a Segment and writes the Checkpoint that lists it as neighbouring Entries of one batch; `CloseSegment` alone still stores a Segment that no Checkpoint lists, which a load leaves out of the Context. `session.Document` gains `max_segment`, which `Snapshot` fills and `Load` restores. All are additions except the refusals, which no code reaches yet, since nothing closes a Segment.

  | Before | After |
  |---|---|
  | `CheckpointSegments(…, []int{1, 1}, …)` and `[]int{2, 1}` put a Segment in the Context twice or out of order | both return `invalid_argument` |
  | `Load(Snapshot())` of a Record with an unlisted Segment 2 gave `NextSegment() == 2` again | `NextSegment() == 3`; a Document with no `max_segment` is read from its Segments, as before |

- **A `Document` carries a Segment's Messages twice, for now.** Once a Segment exists, `messages` holds its rendered Messages and `segments` holds them again, so a Document with Segments is larger. Compaction closes Segments now, so a compacted Session's Document is larger than before; we revisit it if the size matters.

### Providers

- **A Chat Completions stream that carries `{"error": {...}}` in place of a chunk fails with that error.** Before, the decoder ignored the object, saw the stream end with no finish, took it for a cut stream, and sent the request again. Now the Run fails with the provider's message, once, and an error that names a context overflow answers `ContextOverflow()` as the Responses API's `error` event does. Found by playing it on the fake gateway (`test/fakegateway`).

- **A stream cut after some output is sent again, as one cut before any output is** ([FEATURES.md → G-F31](FEATURES.md#g-f31--recovering-a-cut-stream-)). The same request goes out again within `max_retries`, with a `model_retried` Event; the text received is kept as a Cut-off reply that no model sees, and the new reply is a reply of its own. A stream reader that sees `model_retried` discards what it received of that reply. A Run fails with `model_failure` only when the attempts run out.

  | Before | After |
  |---|---|
  | a stream cut after some output failed the Run with `model_failure` at once, and the Run's reply was the Cut-off reply | the request is sent again, up to `max_retries`; the Run completes with the full second reply, and fails with `model_failure` only when every attempt was cut |
  | `model_retried` was stored only for a request the provider refused or could not receive | `model_retried` is also stored for each restart of a cut stream, before or after output |
  | no Event told a reader to drop text it had received | `model_retried` after `text_delta` Events: drop them; `reply_cut_off` and a new `reply_started` follow |
  | `gotato.ReportModelRetry(ctx, retry)` returned nothing | it returns whether a Session heard it; the gateway restarts after output only when it did, so a caller with no Session still gets a retryable error rather than a reply twice |
  | the notice kinds `last_turn` and `stream_cut`, `gotato.NoticeLastTurn` and `gotato.NoticeStreamCut`, were reserved and planned | removed: nothing sends them, and the kinds are free for a service. The last Turn before a deadline is the caller's notice ([G-N21](FEATURES.md#g-n21--a-last-turn-that-answers-is-the-callers-notice)) |

- **The gateway reads an error's `code` and `type` too, and a team can add its own overflow phrases.** Before, `readErrorBody` kept only `error.message`, so a request refused as too long that said so only in `error.code` or `error.type` (an OpenAI `context_length_exceeded` under a vague message) ended as an ordinary `model_failure`. Now a 400 or 413 is a `context_overflow` when any of the three matches a phrase, and the new `overflow_phrases` key (`Config.OverflowPhrases`) adds the team's own phrases or codes to the defaults.

  ```yaml
  overflow_phrases:
    - request_too_big
    - "bigger than we take"
  ```
- **A Tool Result's `json` Parts are escaped like text.** Before, `ForModel` escaped only `text` Parts, so a Tool Result built by hand with a `json` Part that held `</tool_calls>` reached the model as text with the tag intact (a Tool written as a Go function was safe, since `json.Marshal` already escapes `<`). Now the start or end of one of the four tags in a `json` Part is written with the JSON escape for `<`, so the JSON stays valid and parses to the same value.
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
- **A reply records the model that answered it.** Behind a gateway that falls back to another model, the model that answers can differ from the one requested, and nothing said so. `Message.Model` now holds the model the provider reported, read from `model` in each chat chunk, from Responses' `response.created` and `response.completed`, and from Anthropic's `message_start`. A custom `gotato.Model` reports it in the new `ModelEvent.Model`. It is empty when the provider names none, and `ForModel` drops it, so it never reaches a request. Stored Messages and the HTTP session documents gain an optional `model`.
- **A request the provider refuses as too long ends the Run with `context_overflow`.** The gateway marks a 400 or 413 whose error body says the request is longer than the context window (`gateway.Error.Overflow`, `ContextOverflow()`), on all three wire APIs and in the error events of the Responses and Messages streams. The Loop reads any Model error with a `ContextOverflow() bool` method, so a custom `gotato.Model` can say the same. The new code is `gotato.ErrContextOverflow`. The phrases it looks for are in `gateway/overflow.go`, and have not yet been checked against live providers. **Breaking:** a Run that overflows fails with another cause.

  ```text
  before  run_ended {"status":"failed","cause":"model_failure"}
  after   run_ended {"status":"failed","cause":"context_overflow"}
  ```

  A Claude reply that stops with `model_context_window_exceeded` was read as a finished one. It now fails the Run with `context_overflow` too, keeps the text received as a cut-off reply, and is never continued as an output-limit stop (`MaxTruncatedContinuations` does not apply), since a continuation would ask the model again on a full window. HTTP maps an error with this code to `422` where it is returned as a call's error; a Run that fails with it is still a `200` with the code in `result.error`.
- **A model's context window and cache retention are known to the gateway.** The new `context_window` key (`Config.ContextWindow`, in tokens; 0 is unknown, and gotato keeps no table of windows) is read back by `Client.ContextWindow()`. `Client.CacheRetention()` says how long the provider keeps the prompt cache as the client asks for it: 1 hour or 5 minutes on Claude's Messages API, 30 minutes on Responses when it sends a ttl, and 0 (unknown) elsewhere. `modelctx.WindowOf` and `modelctx.RetentionOf` read both from any `gotato.Model` that has the methods.

### Events

The Event design of [DESIGNS.md → Every step is an Event, and a stored Event is an Entry](DESIGNS.md#every-step-is-an-event-and-a-stored-event-is-an-entry--2026-10-18) is built, against the build of 2026-10-16. It is one break. It is part of the same pre-release break as the changes above.

- **The Event `message_withdrawn` is removed.** Nothing could withdraw a waiting message, so Gotato never sent it ([G-N23](FEATURES.md#g-n23--a-waiting-message-is-not-withdrawn)). It is a break of the Event kinds, and it is part of the same pre-release break, and no stream or log can hold one, because the Event was never sent.

  | Before | After |
  |---|---|
  | `gotato.MessageWithdrawn`, `gotato.KindMessageWithdrawn` (`message_withdrawn`: `{message_id, source}`) | removed; `gotato.UnmarshalEvent` reads the kind as a `gotato.UnknownEvent`, like any kind it does not know |

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
  | `gotato.EventObserver`, installed with `WithExtension`, awaited before the Loop went on | removed; `s.Subscribe(ctx)`, `s.SubscribeRun(ctx)`, `s.Subscribe(ctx, gotato.SubscribeAfter(after))` |
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

- **Streams, loads and unsaved Runs keep to the Record.** A Session's stream reads from the Record any stored Event that another Instance wrote, where it skipped it; `compaction_ended` carries the Claim, so a load that starts at a compaction still knows the Writer and its Epoch; a load of a Session whose last Run never ended rebuilds the Cut-off reply from the parts kept, and stores it as a `reply_cut_off`; a `Subscribe(ctx, gotato.SubscribeAfter(n))` over a Record that is not stored fails with `gotato.ErrEventsLost` where it started in the middle of the log; and a Run with `SkipSave` numbers its Events from 1 on its own stream, since it stores none.
- **Two names moved to make room for the Events.** **Breaking:** the Session's status constants `gotato.SessionCreated`, `SessionIdle`, `SessionBusy`, `SessionClosing` and `SessionClosed` are now `SessionStatusCreated` … `SessionStatusClosed`, because `SessionCreated` is the Event; and the function type a Tool reports progress with, `gotato.ToolProgress`, is now `gotato.ToolProgressFunc`, because `ToolProgress` is the Event.
- **New Events.** `session_claimed` (with why, and which instance held the Session before), `session_released`, `session_fenced` and `liveness_lapsed` come with the Claim; `message_queued`, `message_committed`, `message_dropped` and `message_rejected`, and `abort_requested`, come from a live Session's Steer, FollowUp and Abort; `model_retried` comes from the gateway; `reply_part_kept` is the stored part of a streaming reply; `session_created`, `metadata_changed` and `compaction_*` come from the Record; `run_requested`, `run_waiting`, `run_rejected`, `session_loaded`, `session_saved`, `session_deleted` and `store_write_failed` are Instance Events. An Instance that opens a Session it holds already writes `session_claimed` again, with the same Epoch, where it wrote a busy mark.
- **New in the Go API:** `gotato.Feed`, `gotato.Subscription`, `gotato.WithFeed`, `gotato.WithRunStart`, `gotato.MarshalEvent`, `gotato.UnmarshalEvent`, `gotato.ReportModelRetry` (for a gateway), `session.StoredEvents`, `session.WithPublisher`, `session.WithWaited`, `service.Runner.StreamSession`, `SubscribeInstance` and `service.ForAgents`.
- **HTTP.** Every Event, in `…/runs/stream` and in `GET …/events`, is one flat object with `kind`, `time`, `session_id` and `number`, and the fields of its kind at the same level, where it had `payload`. The SSE `event:` name is the new kind. **Breaking**:

  ```json
  // before
  {"session_id":"s1","run_id":"r1","sequence":9,"kind":"run_end","event_class":"protected","payload":{"status":"completed"},"timestamp":"…"}
  // after
  {"kind":"run_ended","time":"…","session_id":"s1","number":19,"run_id":"r1","status":"completed","turns":2,"tool_calls":1,"usage":{…},"elapsed_ms":8}
  ```

  A Run's stream starts with the Event that claimed, or created, its Session and ends with `run_ended`. `GET …/events` lists every stored Event of the Session, read from its Record, messages included; it listed the retained window of protected Events, 1000 at most, before.
- **CLI.** `events` and `run --events jsonl` print the flat objects above, where they printed `payload`; the Run's stream begins with `session_created` and `session_claimed` for a new session, and has no `session_loaded` or `session_saved`. **Breaking**.
- **Field-level moves that the entries above imply.** The Event design renames the fields inside the kinds as well as the kinds. These are the ones the tables above do not name one by one. **Breaking**:

  | Before | After |
  |---|---|
  | `agent_end` payload `error` (the error code of a Run that did not complete) | `run_ended.cause` |
  | `turn_end` payload `summary` (`elapsed_ms`, `text_bytes`, `reasoning_bytes`, `tool_calls`, `input_tokens`, `output_tokens`, `total_tokens`, `cache_read_tokens`, `cache_write_tokens`, `tool_results[]`) | `turn_ended` carries `elapsed_ms`, `text_bytes`, `reasoning_bytes` and `tool_calls` at the top, the token counts in `usage`, and `tools[]` (`tool_id`, `call_id`, `status`, `executed`) in place of `tool_results[]`; there is no `summary` |
  | the top-level `tool_call_id` of an Event (HTTP, `gotato events`) | `call_id`, a field of `tool_started`, `tool_ended`, `tool_progress` and `tool_result_committed` |
  | the top-level `message_id` of an Event | a field only of the kinds that declare it, such as `reply_started`, `text_delta` and `tool_started`; a Message that an Event carries is in its `message` field |
  | `message_start` payload `role` | none; the kind says what started (`reply_started`) |
  | `gotato.ContextSnapshot.AgentID`, `gotato.TurnSnapshot.AgentID` (given to a `ContextTransformer` and a `TurnStopper`) | `SessionID` |
  | `gotato.LifecycleKind`, the kind of a `LifecycleEvent` | removed with `LifecycleEvent`; `session_created` and `session_unloaded` are Events |
  | `gotato.EventSource`, with `Subscribe(ctx) (EventStream, error)` | the name now belongs to a source of stored Events, `EventsAfter(ctx, after, limit) ([]Event, error)`, which `gotato.Feed` reads; the old method is gone: call `Session.Subscribe` |

### Many instances

These are additions, but for one break: the streaming routes answer a refusal at admission with a status, before the stream starts.

- **An instance refuses a new Session past a hard limit.** `service.Admission.MaxActiveSessions` (`gotato serve --max-sessions`; off by default) counts the Sessions an instance has active. A Run that would activate another one past it is refused before anything is loaded, claimed or created: `service.ErrOverloaded` (the new code `gotato.ErrOverloaded`, `overloaded`), and a `run_rejected` Instance Event with the reason `activation_limit`. HTTP answers 503 with the header `Gotato-Not-Run: true` and `"not_run": true` in the body, also for the streaming routes, which answer it before the stream starts (`Runner.StreamRunAdmitted`). The mark tells it from the 503 of a store failure, which may have run. A Run on a Session that is active already, and creating or forking a Session, are not refused. `Runner.ActiveSessions` reports the count, and `/readyz` adds `active_sessions`.
- **The streaming routes answer a refusal at admission with a status.** `POST /v1/runs/stream` and `POST /v1/sessions/{id}/runs/stream` know whether a Run is admitted before they write a byte, so a refused Run gets the status and body that `/v1/runs` gives it, and no stream: draining 409, capacity (`MaxActiveRuns`) 429, and the new activation limit 503 marked not run. Every failure after admission still arrives in-band as `event: error`. No status code or mark of `/v1/runs` changed. **Breaking** for a client of the streaming routes that read these refusals from the stream:

  ```text
  before  POST /v1/runs/stream, the instance draining   →  200 text/event-stream
          event: error
          data: {"error":"Conflict","code":"invalid_state","message":"invalid_state: service: draining"}
  after   POST /v1/runs/stream, the instance draining   →  409 application/json
          {"error":"Conflict","code":"invalid_state","message":"invalid_state: service: draining"}
  ```

  At capacity it is the same, with 429 and `"code":"limit_exceeded"`. Read the status before you read the stream.
- **The endpoints read the `session-id` header.** On HTTP the `session-id` header must agree with the Session ID in the path or the body, or the call fails with 400; a missing one agrees with everything. `POST /v1/sessions`, `POST /v1/runs` and a fork answer with the new Session's ID in a header of the same name, for the client to send next time. A load balancer hashes on it.
- **A Session's stream resumes on any instance.** `GET /v1/sessions/{id}/stream` sends a Session's stored Events after a number, from the Record, and then the live ones. The number is the standard `Last-Event-ID` header or the `after` parameter. Every stored Event on that stream, and on the Run streams, has its number as the SSE `id:`. `Runner.StreamSession` opens it: it checks the Record's epoch and the number, and follows the Record when another instance writes it (`Config.StreamPoll`, one second by default; `gotato.SubscribeTail`). A position that cannot be resumed answers 409 `cannot_resume` with the current epoch. A stream follows the Record it opened, and never delivers an Event of a Record created again under the same ID. When its Session is deleted, or deleted and created again, it ends with a not-found error (HTTP `event: error` with `"error":"Not Found"` and no `code`) or with `cannot_resume`, and drops what was queued: at once on the instance that deletes the Session, or opens, creates or streams the new Record, and elsewhere at the first look that finds the Session gone or the new Record's Events, so an old stream elsewhere stays open, idle, while the new Record is no longer than what it has seen. An instance keeps a Feed per Record, named by Session ID and epoch, so a stream of the new Record never waits for the old one's numbers. `Runner.StreamSession` fails at once for an unknown Session or a number past the Record's end, and ends with `io.EOF` when the instance's drain is over. An idle stream costs one `Log` per look, and a `Load` only after a look that returned Events.
- **A Record has an epoch.** `record_epoch` is chosen when the Record is created and changes only if it is deleted and created again under the same ID. It is in `session_created`, in every Session summary and document, in a Run's result, in the first frame of a Session's stream. A Record written before has none of its own, and every surface names the same one, made of its creation time (`session.Summary.Epoch`, `Record.RecordEpoch`). `gotato run --json` adds `record_epoch`. `testkit/storetest` adds a check that a store keeps `Summary.RecordEpoch`; an adapter that drops it fails the suite.
- **A stream that ends says whether to reconnect, and when.** `service.NewReconnect` gives a wait of half a second to one and a half, spread at random, with the reason (`why`: `draining`); the drain uses it for a Run's stream and for a Session's stream, which it ends once every Run is saved and released. A Session's first frame also sets the SSE `retry:` wait, a different one for each client. An idle stream sends a keepalive comment every 15 seconds (`httpapi.WithKeepAlive`).
- **The multi-instance harness has a hashing proxy.** `service/workload` runs several instances over one store behind a proxy that hashes on `session-id`, with requests sent through it and straight to random instances, concurrent messages and an instance killed. It expects the same Transcripts and Event order as one instance, and a stream cut at every Event, over HTTP, to resume equal to an uncut one.
- **A Session stays warm after its Run, and compacts itself while it idles.** The Runner keeps the Session's Record and its Writer in memory for the Agent's TTL, 60 minutes by default (`service.Agent.Warm`, a `service.WarmPolicy`; `gotato serve --warm-ttl`; a negative TTL keeps nothing). The copy holds no Claim while idle, so another instance takes the Session over as before. Before each Run the instance reads the Entries after the copy's head: none, and it claims the Session again with the Epoch it held and runs without a load; some, or a Record deleted or created again, and it drops the copy and loads. `Runner.WarmSessions` counts the copies; `service.Config.Clock` is the clock that times them, so a test waits for no TTL. `Writer.Stale` and `Writer.Resume` in `session` are the check and the claim.
- **An idle Session is compacted shortly before its cache expires or it leaves memory.** By default, 5 minutes before min(TTL, cache retention) (at most half the TTL), with the compaction `Runner.Compact` makes and `min_tokens` set to 20K tokens, recorded as `before_cache_expires` or `before_unload`; a stretch below it writes nothing. A Run that arrives cancels it and is served first. `WarmPolicy.NoIdleCompaction` (`--no-idle-compaction`) turns it off; `IdleLead` and `IdleMinTokens` set the lead and the minimum.

  | Before | After |
  |---|---|
  | the Runner never wrote an idle Session | an idle Session of an Agent with the defaults is written once, about 55 minutes after its Run, when its stretch since the last Checkpoint holds 20K tokens (a Claim, the compaction's Events, a release); turn it off with `--no-idle-compaction` |
  | every Run on an existing Session sent `session_loaded` | a Run on a warm copy loads nothing and sends none; a load that follows a stale copy or a TTL still does |
  | `session_unloaded` came only from a live Session that closed (`why: "close"`) | the Runner sends it when a warm copy leaves: `ttl`, `taken_over`, `deleted`, `drain`, `closed`, `failed` or `liveness_lapsed` |
  | `session_saved.writes` of a Run counted the writes since the Session was opened | it counts that Run's writes, its Claim included, also on a warm copy |
  | `Runner.Close` and `Runner.Drain` released nothing for an idle Session | they drop the warm copies and write nothing, since an idle copy holds no Claim |
- **Warm Sessions count toward `MaxActiveSessions`, and the longest-idle one is evicted first.** `Admission.MaxActiveSessions` (`--max-sessions`) now limits the Sessions an instance keeps: those with a Run and the warm ones. A Run that would activate one Session more evicts the longest-idle warm Session (`session_unloaded` with why `evicted`, not compacted first; an idle compaction in progress is canceled) and runs. It is refused as not run (`overloaded`) only when every Session is running a Run. A Run on a Session that is warm here adds nothing to the count.

  | Before | After |
  |---|---|
  | the limit counted the Sessions with a Run; warm Sessions were free | the limit counts warm Sessions too; at the limit a new Run evicts the longest-idle one instead of being refused |
  | `session_unloaded` never said `evicted` | it does, when a warm Session is evicted for room |
  | `Runner.ActiveSessions` | unchanged: it still counts the Sessions with a Run; `Runner.WarmSessions` counts the idle ones |
- **An open or a create no longer cuts the stream of a newer Record.** When the Record was deleted and created again on another instance just after this instance wrote its claim, and a client had already streamed the new Record here, the stream ended with `cannot_resume`. A stream is now cut only when it is shown to be of a replaced Record. No field or code changes.

### Measurement

- **The fake gateway plays the native form of deferred tools.** `test/fakegateway` accepts `defer_loading` and `tool_reference` on `/v1/messages`, and a client `tool_search` with `tool_search_call` and `tool_search_output` on `/v1/responses`, from the documented shapes, and refuses what they refuse (every tool deferred, a `cache_control` on a deferred tool, a reference to a tool that is not deferred, a search call with no output). The new quirk `load=<tool ID>` makes the model search for the tool, call it once loaded, and answer. It plays the documented shape, not a real provider. Nothing changes for a team that does not use the fake.
- **The gateway's request bytes are checked at a compaction, at a merge and with reasoning in the replies (G-G07).** `TestWireRequestOnlyGrowsAtItsEndAcrossCompactions` encodes the `long_session` requests for each wire API, and `TestFakeRequestOnlyGrowsAtItsEndWithReasoning` plays a Session with reasoning against the fake gateway. Both pass. FEATURES.md no longer says that the gateway moves the cache mark to the end of the last Segment after a compaction: it never did, and the marks of G-F04 carry the Segments. Tests and text only: no behavior of the runtime changed.
- **The long_session task measures a long Session.** `testkit.LongSessionTask` plays 2,000 Turns of 20 KB Tool Results in 100 Runs on one Session against a fake model with a 128K-token window (`testkit.LongSession` sets the size, `LongSessionRecapTask` turns the model's Recap on). `testkit.Task` gains `Window` and `Recap`; the driver gives the Model the window, counts every request against it, refuses one over it, and `workload.TaskReport` gains `window`: the requests, those over the window, the largest, the size at Turns 1, 10, 100, 1000 and 2000, the compactions and merges, and the Recap's tokens against what each compaction took out. `gotato-workload -tasks long_session` plays it; the default workload does not hold it. G-G10 passes on it; see [GOALS.md](plan/GOALS.md).
- **The payments check of G-E01 ships.** `testkit.PaymentsRecapCheck` is a conversation whose facts only Tool Results held, with five questions about them and a fake model. `workload.RunRecapCheck` compacts the Session with the model's Recap and asks the questions before and after, unsaved and with the tools switched off. `gotato-workload -recap-check` prints the report (`-live` plays it on your gateway; `-min-share` makes the exit code 3 below a share), and never runs in CI against a provider. `testkit.DemoModel` answers a Recap's ask, so `gotato context compact --model demo --recap model` runs without a provider.

### Contracts

- Additions to both, `gotato context compact --json` and `POST /v1/sessions/{id}/compact` report a merge of the oldest Segments as `merge`; `Compaction` adds `merged`, and a Session's tool list holds `read_segment` once it has a Segment. A compaction's `why` may now be `past_budget`.
- HTTP: every Event in a stream or in `GET …/events` has `session_id` where it had `agent_id`, and is the flat object that Events describes. `GET /v1/agents` adds `max_effect`, an object from Agent name to cap; an Agent without a cap has no entry. A stream whose Run the drain stopped ends with `event: result` and then `event: reconnect`, which carries an SSE `retry:` field and `{"retry_after_ms": N, "why": "draining"}`; a run result adds `reconnect` and `record_epoch`. The streaming routes `POST /v1/runs/stream` and `POST /v1/sessions/{id}/runs/stream` answer a refusal at admission with its status before the stream starts: draining 409 and capacity 429, where they answered 200 and an in-band `event: error` (**Breaking**, see Many instances for the before and after), and the new activation limit 503. The other additions of Many instances are not a break, so the version stays: the route `GET /v1/sessions/{id}/stream`, the `id:` of stored Events, the `session-id` header, `Gotato-Not-Run`, 503 `overloaded`, 409 `cannot_resume`, `record_epoch`, and `active_sessions` on `/readyz`.
- CLI: `events` and `run --events jsonl` print the flat Events that Events describes, with `session_id`. `tools list --json` and `tools active --json` add `max_effect` when the agent has a cap, and `tools` is now `[]` rather than `null` when no tool is listed.

## Before the first release — 2026-10-16

Phase 1 of [ROADMAP.md](plan/ROADMAP.md), Measure: the reference workload, the live run, and a number for every benchmark in [GOALS.md](plan/GOALS.md) but G-B13.

### Runtime

- **The panel is gone.** It re-rendered small content that changes every Turn, such as the time, onto the last message of every request. Such content is never cached, so it cost its full price on every Turn, and after a tool call it was appended to the tool's output. A request is now the system prompt, the tools and the history. A fact the model needs now, such as the current time, comes from a tool it calls. A Session that still carries `gotato.panel` in its metadata runs as before, without a panel. **Breaking:**
  - Go: `ModelContext.Panel`, `ModelContext.RenderPanel`, `gotato.PanelTag`, `modelctx.WithPanel`, `modelctx.PanelFunc`, `modelctx.Time`, `modelctx.Report.PanelBytes`, `service.MetaPanel`, `service.PanelFromSpec`, `service.InspectOptions.Panel` and `service.Config.Now`, which only the time panel read, are removed.
  - CLI: `--panel` is gone from `run`, `session create` and `context inspect` (exit 2 if given).
  - HTTP: the context report (`GET /v1/sessions/{id}/context`) has no `panel_bytes`, and the `context_built` payload has no `panel_bytes`.

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

- **Context inspection shows what the next Turn sends.** `Runner.Inspect`, and with it `gotato context inspect` and `build`, and `GET /v1/sessions/{id}/context`, now builds the Agent a Run would build and asks it. The request it reports includes the Agent's `ContextTransformer` and `MessageConverter` extensions, its tools and its model options (`request.options`), so its `prefix_hash` equals the one the next Turn's `context_built` Event reports. The report gains `compaction_pending` and `compact_ceiling`. In Go, the core Agent implements `ContextInspector` (`InspectContext`, which fails with `ErrBusy` during a Run), and `modelctx.ReportOf` turns its `ContextInspection` into a report; `AutoCompactor` gains `Pending` and `Ceiling`. Inspection runs the context extensions and the tool sources, so they must not have side effects. **Breaking:** `RuntimeAgent` now includes `ContextInspector`, so a type of your own that implements `RuntimeAgent` must add `InspectContext`.

- **The Runner times the store with Events.** It emits `session_loaded` before `agent_start` and `session_saved` after `agent_end`, for each load and save a Run makes, with `session_id`, `messages` and the store's time in `elapsed_ms` (fractional milliseconds). A one-shot Run that creates its Session also emits a `session_saved` before `agent_start`. They reach the `StreamRun` sink, the HTTP streams, `gotato run --events jsonl` and the `EventObserver`s in `AgentSpec.Extensions`, and are not recorded in the Session. A failed load or save is marked `failed: true`, without the store's error text. A stream's first Event is now one of these, not `agent_start`; consumers already have to tolerate unknown kinds.
- **A Run loads its Session once.** The Runner loaded an existing Session before taking its lock and again after, so every Run read the store twice. It now reads it once, under the lock. A Run on a Session that does not exist still fails with `session.ErrNotFound`, now after the lock is taken.
- **Every request carries the Session's prompt cache key.** `ModelRequest.CacheKey` groups the requests that share a cacheable prefix; like the cache breakpoints, it never changes the prompt or the prefix hash. `gotato.WithCacheKey` sets it on an Agent, and the Runner gives each Session its own, `service.CacheKey(sessionID)`, a hash of the ID so a caller's ID never reaches the provider. A Spec's `Options` may set another. Context inspection shows it as `request.cache_key`. `Runner.Fork` gives the child its parent's key in `gotato.cache_key` (`service.MetaCacheKey`), so a fork reads its parent's cached prefix, and the Runner sends that key for any Session that has one.
- **The message cache breakpoint is on the last message.** `AssembleRequest` marked the message before the last one, so on Anthropic each Turn left its newest message out of the cache. The marker is now on the last message, a one-message request included, so the whole request is cached for the next Turn. **Breaking** only for code that read `CacheBreakpoints` and expected `Index` to be `len(Messages)-2`.

### Measurement

- **The reference workload has a task whose tools load and unload.** `testkit.ToolSearchTask` (`tool_search`) is seven Runs on one Session with a base tool and three ToolSets, a dozen tools in all (two listed, one folded). The model loads tools by name, by a query and by listing the folded ToolSet, calls one before it is loaded and again once it is, and a compaction unloads one that no Run called. `testkit.Task` gains `Options`, added to the Agent after its tools, and `testkit.WithLoadedToolsOf(messages)` is a `session.Option` that copies the loads of a history, which `session.WithMessages` leaves out. G-G07 and G-G09 are checked on it, and G-G07 now also on the gateway's bytes for every task (GOALS.md).
- **A load is written with the Tool Result that lists it.** `tool_loaded` was written at the end of the batch, after the `search_tools` result, so a crash between the two left a result that said "Loaded" for a tool the Record did not hold as loaded, and the next instance built another request. Both are now one write; in the Event log `tool_loaded` follows the `tool_result_committed` it belongs to, as before, but a later result of the same batch now follows it. Found by the crash test over `tool_search`.
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

## Before the first release — 2026-10-15

### Runtime

- **A reply that the output limit cut off keeps `StopMaxTokens`, even when it holds Tool calls.** The Tools that finished still run, and a `TurnStopper` sees the cut-off in `TurnSnapshot.Assistant.StopReason`. `TurnSnapshot.StopReason` stays the Turn's own reason, which is `StopToolCalls` once Tools ran. **Breaking:** code that read `Message.StopReason == StopToolCalls` to find Tool calls must read `len(Message.ToolCalls) > 0` instead.

### Providers

- **The Responses gateway no longer delivers a Tool call that the output limit cut short,** because its arguments are incomplete. It used to deliver the call with those arguments and report `StopToolCalls`.

## Before the first release — 2026-10-11

The notes below list the breaking changes from the development builds before 2026-10-11.

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

- **Compaction results changed shape.** `modelctx.Compact` sets `MessagesBefore` on the no-op path too, where it used to be 0. `Result.Compaction` is a `*session.Compaction`, present only when `replaced` is true. A no-op compaction no longer emits an all-zero `compaction` object in the CLI or HTTP JSON.

### Service

- **`orchestration` and `host` became `service`, with a new wire contract.** `orchestration`, `host` and `cmd/gotato-agent` are removed. The service is `service.Runner`: a Session store plus an Agent created for each Run. `service/httpapi` serves it over HTTP (`ContractVersion`). The Session ID is the unit of identity. Conversation keys, agent generations, retirement and spawn groups are gone. Derived work is `session.Fork` plus another Run.

  | Before | After |
  |---|---|
  | `POST /v1/runs {"agent_name","conversation_key","prompt"}` | `POST /v1/sessions {"agent"}` then `POST /v1/sessions/{id}/runs {"prompt"}`, or `POST /v1/runs {"prompt","agent"}` to create and run in one call |
  | `POST /v1/runs/stream` (SSE) | `POST /v1/sessions/{id}/runs/stream` or `POST /v1/runs/stream` (SSE: `event: <kind>` … `event: result`) |
  | `POST /v1/runs/async`, `GET /v1/runs/{id}`, `POST /v1/runs/progress` | removed: use the stream or a client-side job |
  | `POST /v1/runs/{run_id}/cancel` | the same, plus `POST /v1/sessions/{id}/cancel` |
  | `GET /v1/conversations/{id}` | `GET /v1/sessions/{id}` (the full document) |
  | `POST /v1/agents/{id}/close`, `/admin/drain` | removed: an Agent lives for one Run, and drain happens on process shutdown |
  | `go run ./cmd/gotato-agent` | `gotato serve` (HTTP) |

  Removed from the root package: `SpawnID`, `Event.SpawnID`, `Event.OriginRunID`, `AgentName`, `ConversationID`, `ConversationKey`, `AgentGeneration`, `LifecycleEvent.ConversationID` and `LifecycleEvent.Generation`. Store lineage and tenancy in `Session.Metadata`.

- **HTTP body limit.** `service/httpapi` rejects a JSON request body larger than 1 MiB with `413 Request Entity Too Large`.
- **Creating a session with an explicit ID is atomic.** `POST /v1/sessions` with an ID returns `409` when the ID exists, instead of racing a concurrent create.
- **`Runner.Fork` and `Runner.Inspect` signatures changed.** `Runner.Fork(ctx, sessionID, newID string)` takes an explicit new ID, empty for a random one, and rejects an existing ID instead of overwriting it. `Runner.Inspect(ctx, sessionID, options ...InspectOptions)` accepts `Instruction` and `Panel` overrides for one inspection. `service.RunRequest` gained `SkipSave`.

### Providers

- **The gateway authenticates with an API key only, and Responses replaces Codex.** The `pi_oauth` auth type, the `chatgpt.com/backend-api` default, the `originator: pi` and `chatgpt-account-id` headers, and every read or write of `~/.pi/agent/auth.json` are removed. `api: openai-codex-responses` is accepted as an alias for `api: openai-responses`, which now targets `https://api.openai.com/v1/responses` by default. `api: openai-completions` is an alias for `openai-chat-completions`. A YAML file with `auth.type: pi_oauth` fails to load with an explicit message.

### CLI

- **`run --context SPEC` is gone,** together with the `window:N` and `summary:N` strategies. Within a session the model sees the whole history. Use `--compact-ceiling N` for automatic compaction and `--panel time,cwd` for content that changes every turn. The run outcome field `context` is replaced by `compacted`.
