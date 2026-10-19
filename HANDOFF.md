# Handoff — compaction with Segments (G-F06)

Written 2026-10-19, for the next session that picks this work up. Delete this file once the build lands. The design itself lives in the project documents; this page says where things stand, what the maintainer still has to decide, and a plan for the build.

## Where things stand

| What | State |
|---|---|
| Design | Written into the docs on `main`: [DESIGNS.md](DESIGNS.md) → *Compaction closes a Segment, before the cache goes cold*, [FEATURES.md](FEATURES.md) → G-F06 (🚧), G-F10, G-F29 and the gateway `context_window` row, [GLOSSARY.md](GLOSSARY.md) → Segment and Recap, [ROADMAP.md](ROADMAP.md) → Phase 6 |
| Review marks | DESIGNS, FEATURES and ROADMAP are `drafted 2026-10-19`; they need the maintainer's approval |
| Docs review | An independent review found 1 High, 6 Medium and 17 Low findings. The clear ones are fixed; the rest are open decisions in [TODO.md](TODO.md) → Decisions. The fix commit was link-checked, not reviewed again |
| Build plan | Below. Written from reading the code at main 52e9786; **not reviewed**, nothing built or run |
| Code | None written |

Not in the repository: notes on how four other agent harnesses compact their context, in `investigation/harness-compaction/` of the main checkout, kept untracked on purpose.

## Before building

1. **The maintainer settles the nine items in [TODO.md](TODO.md) → Decisions.** The most important is the first: with no `context_window` and no ceiling, Segments are never merged, so a long conversation can grow until every request is refused.
2. **The maintainer approves the drafted docs.**
3. **Agree the plan's four cross-stage choices** (D1–D4 below), and review the plan with an independent reviewer before Stage 1.
4. **Stage 0 needs real provider keys** (a gateway config); its checks never run in CI.

Other items still open from earlier work, unrelated to compaction:

- Whether a run refused while the service is draining should be marked "not run".
- `MaxActiveToolSets` stays 16; a service with hundreds of MCP servers may want it higher.
- The designs for G-F30 and G-F31, a check of the notice forms against real APIs, and whether the queue is rebuilt after a crash (Phase 6).
- Two defects in [TODO.md](TODO.md) → Defects (a writer of a deleted Record, a stream cut by an open).

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
| 1 | Tags and escaping in every request | now |
| 2 | Gateway: `context_window`, the cache retention in effect, overflow detection | now |
| 3 | ToolSet activation becomes Session state | now |
| 4 | The Record keeps Segments and message positions | now |
| 5 | Segments built by code, on demand and at Run start | after check 2 |
| 6 | The Agent's model writes the Recap on the cached prefix | now; default after check 1 |
| 7 | A compaction keeps only the ToolSets the Segment used | after 3, 5 and check 3 |
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

### Choices that cut across stages (to agree before Stage 4)

- **D1 — How a Segment is stored.** `CompactionEnded.Messages` keeps its meaning: the Messages the Session goes on with, which are the rendered Segments followed by the kept tail. The Segment's data goes in a new field, `CompactionEnded.Segments []gotato.Segment` (number, times, the Record range it covers, tool-call lines, Recap, ToolSets used, how many Messages render it). Messages kept word for word are stored once, inside `Messages`, with their original IDs; tag text is its own Part, marked `ContentPart.Metadata["gotato.tag"]`. Why: an older binary in a rolling deploy still loads a correct Context, and custom ContextBuilders, the limits and inspection keep working. This departs from a literal "stored as data, rendered at request time".
- **D2 — A new root hook.** `gotato.TurnPreparer`, called by the Loop on the Session goroutine before every model call, with no Tool call open: `PrepareTurn(ctx, PendingTurn) (TurnPrepared, error)`, where `PendingTurn` carries the Session and Run IDs, the Turn, the Transcript, where the Run started, the request, whether this is an overflow retry, and a `Flush` that writes what is staged. `AutoCompactor` moves from `RunPreparer` to it; its `PrepareRun` goes (breaking, CHANGELOG before/after).
- **D3 — Names.** `modelctx.RecapWriter{ Name() string; WriteRecap(ctx, RecapRequest) (Recap, error) }`, with `ModelRecap{Model, MaxTokens, Label}` and `NoRecap{}`. `TruncateSummarizer` keeps its name and implements `RecapWriter`. `Summarizer` and `ModelSummarizer` stay one release as `// Deprecated:` with an adapter. `CompactOptions.Summarizer` and `CompactPolicy.Summarizer` become `Recap` (breaking).
- **D4 — Runs inside a Segment are grouped by Round** (a user Message and what follows it), because a Message carries no RunID. The final reply is the Round's last assistant Message without tool calls. An output-limit continuation is left out, and the replies on either side of it are joined.

### Stage 0 — Provider checks

- A file of live checks, either `gateway/livecheck_test.go` behind `//go:build live` (a new convention here) or a `-check` mode in `cmd/gotato-workload`. It reads the gateway config from the environment and never runs in CI.
- The four checks FEATURES G-F06 lists (the maintainer still has to approve the list, see TODO):
  1. Recap quality on the payments example: a follow-up needs an ID that appeared only in a tool result.
  2. The model reads `<tool_calls>` lines as calls already made: it neither repeats them nor copies the text.
  3. A request whose history holds a call to a tool no longer defined, on all three wire APIs.
  4. The reasoning of a tail kept mid-Run, on Claude and OpenAI.
- Also measure whether Chat Completions providers accept two user Messages in a row, and the exact overflow error bodies (Stage 2).
- Results go into FEATURES G-F06.

### Stage 1 — Tags and escaping

- New root `tags.go`: constants for the four tag names, `MetaTag = "gotato.tag"`, `EscapeTags(string) string`, and the fixed sentence (wording for the maintainer).
- `ForModel` escapes `<` as `&lt;` where it starts one of the four tags or their closing form, case-insensitively, in user text, Tool Result content and `SafeError`, and in the Recap and kept assistant replies; it skips Parts marked `MetaTag`. `AssembleRequest` adds the sentence to the system part only when a marked Part is present.
- Tests: escaping cases (`<Recap`, `</notice`, not `<recapture`), marked Parts untouched, the sentence only when a tag is present, the prefix unchanged for a plain transcript, the workload context check (G-G03) still passing.

### Stage 2 — Gateway

- `YAMLConfig.ContextWindow` (`context_window`) and `Config.ContextWindow`; `(*Client).ContextWindow()` and `(*Client).CacheRetention()` (1h or 5m on Anthropic Messages, as `anthropicCacheTTL` decides; 30m on Responses when set; unknown otherwise).
- `Error.Overflow` and `ContextOverflow() bool`, set for a 400 or 413 whose body matches a short list of phrases, in every wire path; `model_context_window_exceeded` maps to a cut-off reply.
- Root `ErrContextOverflow ErrorCode = "context_overflow"`; `classifyModelError` maps it. `modelctx` gets `ContextWindower`, `CacheRetainer`, `WindowOf` and `RetentionOf`. gRPC maps the new code.
- Tests with `httptest` servers per wire API. CHANGELOG: an overflowing Run's cause changes from `model_failure` to `context_overflow`.
- Risk: the phrase list drifts with providers; Stage 0 checks it.

### Stage 3 — ToolSet activation is Session state

- `KindToolSetActivated` joins the facts (`session/entry.go`); the Record keeps the active names (`ActiveToolSets()`), set by `CompactionEnded.ToolSets` and `SessionCreated.ToolSets`, copied by `Fork`.
- `newToolRegistry` restores them from the Transcript, respecting `MaxActiveToolSets`; a ToolSet that no longer resolves stays off. ToolSets installed with `WithActiveToolSet` are marked as configured and are never switched off.
- Tests: Run 1 activates a ToolSet and Run 2's first request carries its tools, also through a second Runner on the same store; a Checkpoint round trip; old Records restore nothing.
- This settles *ToolSet activation is not a fact* in [TODO.md](TODO.md) once the maintainer agrees.

### Stage 4 — Segments and message positions in the Record

- Root types `Segment`, `SegmentSource` (which Record range a Segment number covers, with the Session ID for a fork's inherited Segments) and `ToolCallLine`. `Compaction` gains the Segment number, tokens before and after, the ceiling, the Recap writer (or `none`), the Recap call's usage and the ToolSets kept. `CompactionWhy` gains `before_cache_expires` and `overflow`.
- The Record keeps a Message ID → {number, time} index since the last Checkpoint, `Segments()`, and `CheckpointSegments(...)`; `Fork` copies Segments; the gRPC proto gains a typed `Segment` (regenerate with `buf generate`).
- All fields are additions; the storetest suite must still pass.

### Stage 5 — Segments built by code

- `modelctx/segment.go`: the builder (user text and each Round's final reply word for word; reasoning, intermediate text, Tool Results and cut-off replies left out; one tool-call line per call as `HH:MM tool {args cut at 200 chars} → ok|failed|blocked|cancelled[: code]`, repeats as one line `×N`, at most 100 lines then counts per tool) and the renderer (header Part, `<tool_calls>` Parts, final replies as assistant Messages with their IDs, the `<recap>` in a trailing user Message).
- Times in RFC 3339 with seconds; tool-call lines `HH:MM`, with the date when it changes. Time zone UTC by default, set per Agent.
- An old summary Checkpoint becomes Segment 1 holding only a Recap with its text.
- `Compact`, `Runner.Compact`, and the HTTP, gRPC and CLI compact surfaces close a Segment; `--summarizer truncate|model|none` (CLI default stays `truncate`); `keep` stays as the tail's size, cut at a Turn boundary (pending *What `--keep N` and `CompactPolicy.Floor` mean with Segments* in [TODO.md](TODO.md)).
- Tests: a golden render of the payments example, escaping inside Segments, repeated calls, the 100-line cap, a tool call never split from its result, Segment 1's bytes unchanged after Segment 2, an old Record compacting. Update the compaction tests in `modelctx`, `service`, `httpapi`, `cmd/gotato` and `session`.
- CHANGELOG: breaking, the summary Message becomes a Segment and `Summarizer` is renamed.
- Risk: Chat Completions encoders must join consecutive user Messages, which changes requests that already have two in a row.

### Stage 6 — The model writes the Recap

- `ModelRecap`: the Context the next model call would send, plus one plain user message holding the instruction in FEATURES G-F06, `MaxTokens` 8192. It fails on a tool call, a cut-off reply, empty text or the heading rule; on failure the Segment has no Recap and the record says `none`. A cancelled context fails the whole compaction.
- The Recap's usage goes to the compaction record and, mid-Run, to the Run's usage.
- Built here for later: `modelctx.IdleDue(lastCall, ttl, retention, lead)` and the 20K-token minimum, tested with a fake clock.
- `CompactPolicy` gains the Recap writer, its token limit, the time zone, the tool-line limits and the idle settings.
- Risks: a model-written Recap can take a minute or more, against `ModelCallDeadline` (2 min) and `RunDeadline` (5 min). The tools stay in the request so the cache is reused, so the model may call one; `tool_choice: none` (G-F30) would help if it keeps the cache (unverified).

### Stage 7 — ToolSets kept by a compaction

- Kept: model-activated ToolSets whose tools are called in the Segment just closed or in the kept tail, plus those installed with `WithActiveToolSet`. None are switched off by count. Recorded in the Checkpoint and the compaction record; mid-Run, the registry syncs after the rewrite (Stage 8).
- Tests: used, unused, used only in the tail, Agent-installed; after a reload the tools match; `activate_toolset` reappears.

### Stage 8 — Past the ceiling before any model call

- The Loop calls the `TurnPreparer` after assembling a Turn and before `context_built`, bounded by `ModelCallDeadline`. After a rewrite it re-measures, syncs ToolSets, rebuilds the request and adds the Recap's usage. At most one rewrite per Turn.
- `AutoCompactor`: the ceiling from the policy or Session metadata, else window − max(10%, 16K) when the window is known, else off. Size: the last usage after the Checkpoint plus an estimate of what follows; with none, the request bytes ÷ 4. Cut: the earliest Turn boundary in this Run whose tail is at most min(20K tokens, a quarter of the window or of the ceiling); with no finished Turn, the cut falls before the prompt. Still past after compacting: `limit_exceeded` with `Details{"limit":"context"}`.
- `buildSession` always installs the AutoCompactor (for overflow). `CompactPolicy.Floor` gives way to a tail size (breaking).
- Tests: a mid-Run compaction with large Tool Results (cut between Turns, the prompt inside the Segment, *Where it stands* in the Recap, the next Turn sends the tail, `context_built` matches inspection), once per Turn, `limit_exceeded`, Abort during the Recap call.

### Stage 9 — Overflow

- When the provider refuses a request as too long before any output and the Turn has not compacted, the Loop calls the hook with the overflow flag and sends again once. A second refusal, or no rewrite, ends the Run with the new code.
- The model Recap is skipped on overflow, since its request would overflow too; the Segment is kept without one.
- Tests: one refusal, two refusals, an overflow after a ceiling compaction in the same Turn.

### Stage 10 — `read_segment`

- `modelctx/readsegment.go`: a built-in tool offered only while the Record has Segments; Effect `read`; arguments `{n, page?}`; pages of 32 KiB (configurable). It reads the stored Messages of that Segment (the parent's Record for a fork's inherited Segments), prefixes lines with `[user]`, `[assistant]`, `[tool name]` and escapes a user line that starts like one; it says the Messages were removed when the team pruned them.
- Tests: paging, Tool Results included, a fork, a deleted parent, a merged number, the Effect cap.

### Stage 11 — Merge the oldest Segments

- When the Segments' estimated size passes half the ceiling, merge the oldest so the total falls under it: header `n="1-3"`, one Recap of their Recaps from a merge instruction (to be drafted and approved), the original numbers still readable with `read_segment`. A failed merge Recap leaves the Segments unmerged.
- What triggers a merge with no ceiling is *Without a ceiling, Segments are never merged* in [TODO.md](TODO.md).
- Runs after a ceiling compaction now, at the idle trigger later.

### Stage 12 — Idle trigger (waits for Phase 6)

- FEATURES and ROADMAP tie it to the warm Session's TTL timer, its queue (a new message cancels the compaction) and `before_unload`. A Runner-level timer built earlier would be thrown away in Phase 6. The due-time function and the compaction exist by then, so Phase 6 adds the timer and the defaults.
- A short or unknown cache retention is *Idle compaction with a short or unknown cache retention* in [TODO.md](TODO.md).

### Stage 13 — Native forms and notices (waits for G-F29 and G-F32)

- Route Parts marked `MetaTag` the way notices go per wire API (the request bytes change once). Inside Segments, keep a service notice word for word and leave out Gotato's own. Fold `settings_changed` into the prefix (G-F32).

### Not confirmed while planning

- The exact overflow error text for each provider.
- Whether Chat Completions providers accept consecutive user Messages, and their cache retention.
- Whether `tool_choice` changes caching on Anthropic.
- How Claude behaves with a mid-Run tail without its thinking.
- How long a Recap takes on real models.
- Whether every overflow path reaches the Loop before any output.
