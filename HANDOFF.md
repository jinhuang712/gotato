# Handoff — compaction with Segments (G-F06)

Written 2026-10-19, for the next session that picks this work up. Delete this file once the build lands. The design itself lives in the project documents; this page says where things stand, what the maintainer still has to decide, and a plan for the build.

## Where things stand

| What | State |
|---|---|
| Design | Written into the docs on `main`: [DESIGNS.md](DESIGNS.md) → *Compaction closes a Segment, before the cache goes cold*, [FEATURES.md](FEATURES.md) → G-F06 (🚧), G-F10, G-F29 and the gateway `context_window` row, [GLOSSARY.md](GLOSSARY.md) → Segment and Recap, [ROADMAP.md](ROADMAP.md) → Phase 6 |
| Review marks | DESIGNS, FEATURES and ROADMAP are `approved 2026-10-19` |
| Docs review | An independent review found 1 High, 6 Medium and 17 Low findings. The clear ones are fixed, and the nine open decisions were settled on 2026-10-19 by taking each recommendation, and written into the docs. Neither round was reviewed again by a second reader |
| Build plan | Below. Written from reading the code at main 52e9786. Stages 1–4 and the `RunID` were built from it and reviewed by an independent reader, and the plan needed corrections that are now in the text. Stages 5 on have not been built |
| Code | Stages 1–4 and `Message.RunID` are on `main` (landed 2026-10-25). Nothing closes a Segment yet, so no Record holds one |

Not in the repository: notes on how four other agent harnesses compact their context, in `investigation/harness-compaction/` of the main checkout, kept untracked on purpose.

## Before building

1. **The nine decisions are settled**: Segments merge on an overflow and by a default budget (its number is still to choose); a Segment's Messages count toward the limits as a load reads them, and the 1,000-message default is raised or dropped; the Claude cache marker moves to the last Segment's end after a compaction; idle compaction uses the TTL alone when the retention is short or unknown; the provider checks 1, 2 and 4 stay and check 3 is dropped unless a case is found; a notice or Recap with no user Message after it follows G-F29's forms; ToolSet activation is a fact; `--keep N` and `Floor` stay as the kept tail's size; the Recap call records usage, not cost.
2. **The docs are approved** (2026-10-19). The default Segment budget's number is still to pick, or to leave to Stage 0's measurements.
3. **The four cross-stage choices are settled** (D1–D4 below, agreed 2026-10-24). Each stage from 5 on is reviewed by an independent reader before it lands.
4. **Stage 0 needs real provider keys** (a gateway config); its checks never run in CI.

Open defects and decisions unrelated to compaction are in [TODO.md](TODO.md), and the features that wait for a design in [ROADMAP.md → Waiting for design](ROADMAP.md#waiting-for-design).

## How to work here

- **Follow [AGENTS.md](AGENTS.md)**, especially *Editing the Project Documents* and the Definition of Done.
- **Worktree:** `git worktree add .claude/worktrees/compaction -b feat/compaction main`, then work only there.
- **One stage at a time.** After each stage: `gofmt -l .`, `go vet ./...`, `go test -race ./...`, `golangci-lint run ./...` (and the same in `adapter/grpc` when the proto or root types change), then an independent review, then fixes.
- **Landing:** rebase on `main` and fast-forward only, never a merge commit, and only after the maintainer says so. Commits follow [GITFLOW.md](GITFLOW.md).

## Build plan

### Stages

| # | Stage | When |
|---|---|---|
| 0 | Provider checks (live, not in CI) | now |
| 1 | Tags and escaping in every request | ✅ landed 2026-10-25 |
| 2 | Gateway: `context_window`, the cache retention in effect, overflow detection | ✅ landed 2026-10-25 |
| 3 | ToolSet activation becomes Session state | ✅ landed 2026-10-25 |
| 4 | The Record keeps Segments and message positions | ✅ landed 2026-10-25 |
| 4b | `Message.RunID` | ✅ landed 2026-10-25 |
| 5a | The rename: `Summarizer` becomes `RecapWriter` (D3) | now |
| 5 | Segments built by code, on demand and at Run start | after 5a and check 2 |
| 6 | The Agent's model writes the Recap on the cached prefix | now; default after check 1 |
| 7 | A compaction keeps only the ToolSets the Segment used | after 3 and 5 |
| 8 | Past the ceiling before any model call, mid-Run included | after 2, 5, 6 and check 4 |
| 9 | Overflow: compact and send again once | after 2 and 8 |
| 10 | `read_segment` | after 4 and 5 |
| 11 | Merge the oldest Segments | after 6, 8 and 10 |
| 12 | Idle trigger (`before_cache_expires`, `before_unload`) | waits for the warm Session's TTL (Phase 6, G-F27) |
| 13 | Native wire forms for the tags; notices inside Segments | waits for G-F29 and G-F32 |

Stages 1–11 do not need the warm Session. ROADMAP places all of G-F06 in Phase 6, so building them first is an order inside Phase 6, not a ROADMAP change.

### Facts checked in the code (main 52e9786, by reading)

- Runtime notices (G-F29) are not built: `Role` has only `user`, `assistant` and `tool_result` (`types.go`), and the output-limit continuation is a plain `UserMessage(TruncatedContinuationText)` (`loop.go`).
- There is no overflow detection: `classifyModelError` (`session.go`) maps every model failure to `model_failure`, and `gateway.Error` has no overflow flag. `anthropicStopReason` (`gateway/anthropic.go`) maps `model_context_window_exceeded` to `end_turn`, so a silent overflow looks like a normal reply.
- The warm Session and its TTL are not built: the Runner builds a live Session per Run and closes it (`service/service.go`). `CompactionBeforeUnload` is declared and unused.
- G-F32 and MCP (G-F12) are not built.
- The gateway has `cache_retention` but no context window (`gateway/config.go`, `gateway/gateway.go`).
- **ToolSet activation is not kept at all today.** `toolset_activated` is a log entry, not a fact (`session/entry.go`), nothing restores it, and the Runner builds a new registry per Run, so a ToolSet the model switched on is lost after its Run. Stage 3 fixes this first.
- `MaxMessages` and `MaxTranscriptBytes` already count only what a load reads (`transcript.Len()` and `Messages()` hold the last Checkpoint's Messages and what follows).
- Also relied on: `ForModel` strips Part metadata; `Message` has no time, RunID or source; the Anthropic encoder joins consecutive user Messages and the Chat Completions encoder does not; `AutoCompactor` is a `RunPreparer` that runs only at Run start; `Compact` defaults to `TruncateSummarizer`; the HTTP and gRPC compact routes default to `keep=4`, the CLI to `--keep 4 --summarizer truncate`; Gotato has no prices; ToolSets reach a Session through `Options`, not the `Agent`.

### Choices that cut across stages (settled 2026-10-24)

- **D1 — How a Segment is stored: written once, pointed to.** A Segment is appended once as its own `segment_closed` entry, with its rendered Messages (frozen, never rewritten) and its data (number, times, the Record range it covers, tool-call lines, Recap, ToolSets used). A Checkpoint (`CompactionEnded`) lists the Segment numbers it uses and the kept tail; it does not copy a Segment's Messages. The tail's Messages are stored in the Checkpoint too, because `DropReasoning` rewrites them when the Checkpoint is written. The original entries stay in the log, unread by the Context and readable by number range. Older binaries are not supported. Cost accepted: `Loaded.Segments` returns every `segment_closed` entry before the last Checkpoint, including ones a later Checkpoint no longer uses, so a load grows with the history. Tag text is its own Part, marked `ContentPart.Metadata["gotato.tag"]` (`gotato.MetaTag`); a Part the builder writes without it is escaped.
- **D2 — A new root hook.** `gotato.TurnPreparer`, called by the Loop on the Session goroutine before every model call, with no Tool call open: `PrepareTurn(ctx, PendingTurn) (TurnPrepared, error)`, where `PendingTurn` carries the Session and Run IDs, the Turn, the Transcript, where the Run started, the request, whether this is an overflow retry, and a `Flush` that writes what is staged. `AutoCompactor` moves from `RunPreparer` to it; its `PrepareRun` goes (breaking, CHANGELOG before/after). The first call of a Run does what `PrepareRun` did.
- **D3 — Names, no old names kept.** `modelctx.RecapWriter{ Name() string; WriteRecap(ctx, RecapRequest) (Recap, error) }`, with `ModelRecap{Model, MaxTokens, Label}`, `NoRecap{}` and `TruncateRecap`. `Summarizer`, `ModelSummarizer` and `TruncateSummarizer` are removed with no `Deprecated` shim. `CompactOptions.Summarizer` and `CompactPolicy.Summarizer` become `Recap`, and the CLI flag `--summarizer` becomes `--recap` (breaking, CHANGELOG before/after).
- **D4 — Runs inside a Segment are grouped by Run.** `gotato.Message` carries `RunID` (built). The final reply is the Run's last assistant Message without tool calls. A Message with an empty `RunID` (history a Record started with, the `repair-*` results made for unanswered calls) is not from a Run and is kept or left out by its own rule, not grouped. An output-limit continuation carries its Run's ID, so the replies on either side of it are one Run.

### Carry-overs from the Stage 1–4 review

Findings of the independent reviewer that no current code can trigger, because nothing closes a Segment yet. Stage 5 meets them:

- `modelctx.Compact` flattens a Record's Segments into one summary: it reads `Messages()`, which includes the Segment prefix. Cut only after the start of `Record.Tail()`, and call `CheckpointSegments`.
- `CheckpointSegments` accepts duplicate and out-of-order numbers (`[1,1]` puts a Segment in the Context twice). Refuse both.
- `CloseSegment` and `CheckpointSegments` must be called together, so that one write batch holds both. A Segment closed alone and stored is not listed by any Checkpoint.
- A `Document` carries the Segments' Messages twice (in `messages` and in `segments`), so it is about twice the size once Segments exist. Put it in the CHANGELOG when Segments become reachable.
- `Load(doc)` loses the largest Segment number, so `NextSegment` can reuse a number that exists as an orphan in the store.
- `MaxMessages` was raised from 1,000 to 10,000 before anything fills it.
- A tool call's arguments and reasoning Parts are not escaped by `ForModel`; the plan did not list them.
- A Part with `MetaTag` is trusted. If a ToolSet bridge ever copies foreign metadata into a Tool Result, it must drop that key.
- The overflow phrase list in `gateway/overflow.go` is six fragments, unchecked against real providers (Stage 0), and `readErrorBody` keeps only `error.message`, so an error that names the overflow only in `code` or `type` is missed. "Input and `max_tokens` too large" may match too, and compaction cannot fix that one.
- `Client.CacheRetention()` returns 5m for any Anthropic Messages client, even when `cache_retention` is shorter.
- `StatusFor` maps `context_overflow` to 422, but a failed Run is returned as HTTP 200 with the code in `result.error.code`; callers read the JSON.

### Stage 0 — Provider checks

- A file of live checks, either `gateway/livecheck_test.go` behind `//go:build live` (a new convention here) or a `-check` mode in `cmd/gotato-workload`. It reads the gateway config from the environment and never runs in CI.
- The checks FEATURES G-F06 lists:
  1. Recap quality on the payments example: a follow-up needs an ID that appeared only in a tool result.
  2. The model reads `<tool_calls>` lines as calls already made: it neither repeats them nor copies the text.
  4. The reasoning of a tail kept mid-Run, on Claude and OpenAI.
  5. Whether Claude reads the Segments before from the cache at a marker at the end of the last Segment.
- Check 3, dropped unless a case is found (the numbers of the others stay): a request whose history holds a call to a tool no longer defined. Compaction makes none (a kept tail keeps the ToolSets it calls, a Segment holds its calls as text). If Stage 7 or a user's ToolSet change shows one, check it on all three wire APIs then.
- Also measure whether Chat Completions providers accept two user Messages in a row, and the exact overflow error bodies (Stage 2).
- A cache marker at the end of the last Segment on Claude, in place of the one at the last Round's start right after a compaction: does a request read the Segments before from the cache there, and does the next compaction's request?
- Results go into FEATURES G-F06.

### Stage 1 — Tags and escaping

Built and landed 2026-10-25. The text below is the plan; where the build differs, the D1–D4 section above and the code are right.

- New root `tags.go`: constants for the four tag names, `MetaTag = "gotato.tag"`, `EscapeTags(string) string`, and the fixed sentence (wording for the maintainer).
- `ForModel` escapes `<` as `&lt;` where it starts one of the four tags or their closing form, case-insensitively, in user text, Tool Result content and `SafeError`, and in the Recap and kept assistant replies; it skips Parts marked `MetaTag`. `AssembleRequest` adds the sentence to the system part only when a marked Part is present.
- Tests: escaping cases (`<Recap`, `</notice`, not `<recapture`), marked Parts untouched, the sentence only when a tag is present, the prefix unchanged for a plain transcript, the workload context check (G-G03) still passing.

### Stage 2 — Gateway

Built and landed 2026-10-25. The text below is the plan; where the build differs, the D1–D4 section above and the code are right.

- `YAMLConfig.ContextWindow` (`context_window`) and `Config.ContextWindow`; `(*Client).ContextWindow()` and `(*Client).CacheRetention()` (1h or 5m on Anthropic Messages, as `anthropicCacheTTL` decides; 30m on Responses when set; unknown otherwise).
- `Error.Overflow` and `ContextOverflow() bool`, set for a 400 or 413 whose body matches a short list of phrases, in every wire path; `model_context_window_exceeded` maps to a cut-off reply.
- Root `ErrContextOverflow ErrorCode = "context_overflow"`; `classifyModelError` maps it. `modelctx` gets `ContextWindower`, `CacheRetainer`, `WindowOf` and `RetentionOf`. gRPC maps the new code.
- Tests with `httptest` servers per wire API. CHANGELOG: an overflowing Run's cause changes from `model_failure` to `context_overflow`.
- Risk: the phrase list drifts with providers; Stage 0 checks it.

### Stage 3 — ToolSet activation is Session state

Built and landed 2026-10-25. The text below is the plan; where the build differs, the D1–D4 section above and the code are right.

- `KindToolSetActivated` joins the facts (`session/entry.go`); the Record keeps the active names (`ActiveToolSets()`), set by `CompactionEnded.ToolSets` and `SessionCreated.ToolSets`, copied by `Fork`.
- `newToolRegistry` restores them from the Transcript, respecting `MaxActiveToolSets`; a ToolSet that no longer resolves stays off. ToolSets installed with `WithActiveToolSet` are marked as configured and are never switched off.
- Tests: Run 1 activates a ToolSet and Run 2's first request carries its tools, also through a second Runner on the same store; a Checkpoint round trip; old Records restore nothing.
- Decided: activation is a fact. It is built before Segments, and the compaction design relies on it.

### Stage 4 — Segments and message positions in the Record

Built and landed 2026-10-25. The text below is the plan; where the build differs, the D1–D4 section above and the code are right.

- Root types `Segment`, `SegmentSource` (which Record range a Segment number covers, with the Session ID for a fork's inherited Segments) and `ToolCallLine`. `Compaction` gains the Segment number, tokens before and after, the ceiling, the Recap writer (or `none`), the Recap call's usage and the ToolSets kept. `CompactionWhy` gains `before_cache_expires` and `overflow`.
- The Record keeps a Message ID → {number, time} index since the last Checkpoint, `Segments()`, and `CheckpointSegments(...)`; `Fork` copies Segments; the gRPC proto gains a typed `Segment` (regenerate with `buf generate`).
- All fields are additions; the storetest suite must still pass.
- `MaxMessages` and `MaxTranscriptBytes` keep counting what a load reads, which now includes a Segment's Messages. Raise the default of `MaxMessages` or drop the count in this stage, with a CHANGELOG note (the default is a documented limit).

### Stage 5 — Segments built by code

- `modelctx/segment.go`: the builder (user text and each Round's final reply word for word; reasoning, intermediate text, Tool Results and cut-off replies left out; one tool-call line per call as `HH:MM tool {args cut at 200 chars} → ok|failed|blocked|cancelled[: code]`, repeats as one line `×N`, at most 100 lines then counts per tool) and the renderer (header Part, `<tool_calls>` Parts, final replies as assistant Messages with their IDs, the `<recap>` in a trailing user Message).
- The Claude encoder gives the Recap its own content block, and the gateway moves the fourth cache marker from where the last Round started to the end of the last Segment right after a compaction, back once a new Round begins (to verify in Stage 0).
- Times in RFC 3339 with seconds; tool-call lines `HH:MM`, with the date when it changes. Time zone UTC by default, set per Agent.
- An old summary Checkpoint becomes Segment 1 holding only a Recap with its text.
- `Compact`, `Runner.Compact`, and the HTTP, gRPC and CLI compact surfaces close a Segment; `--summarizer truncate|model|none` (CLI default stays `truncate`); `keep` stays as the tail's size, cut at a Turn boundary (decided: `--keep`, `{"keep":N}` and `CompactPolicy.Floor` stay as the size of the kept tail, so nothing breaks).
- Tests: a golden render of the payments example, escaping inside Segments, repeated calls, the 100-line cap, a tool call never split from its result, Segment 1's bytes unchanged after Segment 2, an old Record compacting. Update the compaction tests in `modelctx`, `service`, `httpapi`, `cmd/gotato` and `session`.
- CHANGELOG: breaking, the summary Message becomes a Segment and `Summarizer` is renamed.
- Risk: Chat Completions encoders must join consecutive user Messages, which changes requests that already have two in a row.

### Stage 6 — The model writes the Recap

- `ModelRecap`: the Context the next model call would send, plus one plain user message holding the instruction in FEATURES G-F06, `MaxTokens` 8192. It fails on a tool call, a cut-off reply, empty text or the heading rule; on failure the Segment has no Recap and the record says `none`. A cancelled context fails the whole compaction.
- The Recap's usage goes to the compaction record and, mid-Run, to the Run's usage. Usage only, no cost, until the model catalog has prices.
- Built here for later: `modelctx.IdleDue(lastCall, ttl, retention, lead)` and the 20K-token minimum, tested with a fake clock. It uses the TTL alone when the retention is unknown or not well above the lead (a 5-minute retention counts as not well above); test both.
- `CompactPolicy` gains the Recap writer, its token limit, the time zone, the tool-line limits and the idle settings.
- Risks: a model-written Recap can take a minute or more, against `ModelCallDeadline` (2 min) and `RunDeadline` (5 min). The tools stay in the request so the cache is reused, so the model may call one; `tool_choice: none` (G-F30) would help if it keeps the cache (unverified).

### Stage 7 — ToolSets kept by a compaction

- Kept: model-activated ToolSets whose tools are called in the Segment just closed or in the kept tail, plus those installed with `WithActiveToolSet`. None are switched off by count. Recorded in the Checkpoint and the compaction record; mid-Run, the registry syncs after the rewrite (Stage 8).
- Tests: used, unused, used only in the tail, Agent-installed; after a reload the tools match; `activate_toolset` reappears.

### Stage 8 — Past the ceiling before any model call

- The Loop calls the `TurnPreparer` after assembling a Turn and before `context_built`, bounded by `ModelCallDeadline`. After a rewrite it re-measures, syncs ToolSets, rebuilds the request and adds the Recap's usage. At most one rewrite per Turn.
- `AutoCompactor`: the ceiling from the policy or Session metadata, else window − max(10%, 16K) when the window is known, else off. Size: the last usage after the Checkpoint plus an estimate of what follows; with none, the request bytes ÷ 4. Cut: the earliest Turn boundary in this Run whose tail is at most min(20K tokens, a quarter of the window or of the ceiling); with no finished Turn, the cut falls before the prompt. Still past after compacting: `limit_exceeded` with `Details{"limit":"context"}`.
- `buildSession` always installs the AutoCompactor (for overflow). `CompactPolicy.Floor` stays as the tail's size in tokens; its default changes from half the ceiling to the kept-tail rule above, which needs a CHANGELOG note but is not a break of the field.
- Tests: a mid-Run compaction with large Tool Results (cut between Turns, the prompt inside the Segment, *Where it stands* in the Recap, the next Turn sends the tail, `context_built` matches inspection), once per Turn, `limit_exceeded`, Abort during the Recap call.

### Stage 9 — Overflow

- When the provider refuses a request as too long before any output and the Turn has not compacted, the Loop calls the hook with the overflow flag and sends again once. A second refusal, or no rewrite, ends the Run with the new code.
- The model Recap is skipped on overflow, since its request would overflow too; the Segment is kept without one.
- When the Segments alone are past the limit, merge the oldest (Stage 11) before sending again. Open: a merge's Recap request on the whole Context would overflow too, so how an overflow merge gets its Recap (a request holding only the Recaps being merged, or no Recap) is to settle here and check in Stage 0.
- Tests: one refusal, two refusals, an overflow after a ceiling compaction in the same Turn.

### Stage 10 — `read_segment`

- `modelctx/readsegment.go`: a built-in tool offered only while the Record has Segments; Effect `read`; arguments `{n, page?}`; pages of 32 KiB (configurable). It reads the stored Messages of that Segment (the parent's Record for a fork's inherited Segments), prefixes lines with `[user]`, `[assistant]`, `[tool name]` and escapes a user line that starts like one; it says the Messages were removed when the team pruned them.
- Tests: paging, Tool Results included, a fork, a deleted parent, a merged number, the Effect cap.

### Stage 11 — Merge the oldest Segments

- When the Segments' estimated size passes their budget (half the ceiling; with no ceiling, a default budget whose number is still to choose), merge the oldest so the total falls under it: header `n="1-3"`, one Recap of their Recaps from a merge instruction (to be drafted and approved), the original numbers still readable with `read_segment`. A failed merge Recap leaves the Segments unmerged.
- The default budget makes a merge run with no ceiling too; on an overflow (Stage 9) it merges the oldest Segments when they alone are past the limit in the refusal.
- Runs after a ceiling compaction now, at the idle trigger later.

### Stage 12 — Idle trigger (waits for Phase 6)

- FEATURES and ROADMAP tie it to the warm Session's TTL timer, its queue (a new message cancels the compaction) and `before_unload`. A Runner-level timer built earlier would be thrown away in Phase 6. The due-time function and the compaction exist by then, so Phase 6 adds the timer and the defaults.
- With a short or unknown cache retention the trigger uses the TTL alone (Stage 6's `IdleDue`); what counts as well above the lead is set there.

### Stage 13 — Native forms and notices (waits for G-F29 and G-F32)

- Route Parts marked `MetaTag` the way notices go per wire API (the request bytes change once). With no user Message after a tagged Part (after an idle compaction, a `Continue` without a prompt, a FollowUp), use the native form where there is one; on Chat Completions send a user message on the wire that the Record keeps as a notice, so `roundStart` is not moved. Check it in Stage 0. Inside Segments, keep a service notice word for word and leave out Gotato's own. Fold `settings_changed` into the prefix (G-F32).

### Not confirmed while planning

- The exact overflow error text for each provider.
- Whether Chat Completions providers accept consecutive user Messages, and their cache retention.
- Whether `tool_choice` changes caching on Anthropic.
- Whether Claude reads the Segments before from the cache at a marker at the end of the last Segment.
- How Claude behaves with a mid-Run tail without its thinking.
- How long a Recap takes on real models.
- Whether every overflow path reaches the Loop before any output.
