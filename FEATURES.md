<!-- what-we-build: drafted 2026-10-29 -->
# Gotato — Features

What Gotato has today, what it will have, and what it won't, as an inventory. Each feature is described from the side of the developer who embeds Gotato: the Go calls, the HTTP routes and the configuration keys. How a part works is in [specs/](specs/); why it is built that way is in [plan/decisions/](plan/decisions/README.md); the principles are in [DESIGNS.md](DESIGNS.md); when the planned parts arrive is in [ROADMAP.md](plan/ROADMAP.md); the terms are in [GLOSSARY.md](GLOSSARY.md).

## TL;DR

Every feature by group, with its status: ✅ built, *later* an idea with no milestone, *not planned* a thing Gotato will not build. Each feature below has the same shape: a purpose line, tables of its items with their defaults and limits, its routes and calls, and links to its spec and its decisions.

**Defining and running agents**

| Feature | What it gives you | Status |
|---|---|---|
| [G-F01 Agents and Runs](#g-f01--agents-and-runs-) | define an Agent, run it, get a settled result and errors that say what to do | ✅ |
| [G-F26 Steering a Run](#g-f26--steering-a-run-) | Steer, FollowUp, Interrupt and Stop from any endpoint | ✅ |
| [G-F29 Runtime notices](#g-f29--runtime-notices-) | tell the model what is not the user's words | ✅ |
| [G-F02 Sessions](#g-f02--sessions-) | create, fork, per-Session settings | ✅ |

**What the model sees**

| Feature | What it gives you | Status |
|---|---|---|
| [G-F04 Context](#g-f04--context-) | built every Turn in a fixed order | ✅ |
| [G-F05 Context inspection](#g-f05--context-inspection-) | the next request without calling the model | ✅ |
| [G-F06 Compaction](#g-f06--compaction-) | Segments, Recaps, merges, four triggers | ✅ |

**Tools**

| Feature | What it gives you | Status |
|---|---|---|
| [G-F09 Tools and their Effect](#g-f09--tools-and-their-effect-) | a Tool, its Effect and the cap | ✅ |
| [G-F10 Tool sets](#g-f10--tool-sets-) | tools that arrive as a group, resolved when a Run starts | ✅ |
| [G-F34 Tool search](#g-f34--tool-search-) | a catalog, tools load one by one | ✅ |
| [G-F12 The MCP adapter](#g-f12--the-mcp-adapter-) | an MCP server's tools as a ToolSet | ✅ |
| [G-F35 Large Tool Results](#g-f35--large-tool-results-) | spilled, read in pages | ✅ |

**Models**

| Feature | What it gives you | Status |
|---|---|---|
| [G-F07 Models and providers](#g-f07--models-and-providers-) | three wire APIs, retries, reasoning replay, YAML | ✅ |
| [G-F31 Recovering a cut stream](#g-f31--recovering-a-cut-stream-) | a cut model stream is sent again, not failed | ✅ |

**Storage**

| Feature | What it gives you | Status |
|---|---|---|
| [G-F03 Stores](#g-f03--stores-) | memory, file, your own; the append-only Record and the store contract | ✅ |

**Many instances**

| Feature | What it gives you | Status |
|---|---|---|
| [G-F27 Service: many instances](#g-f27--service-many-instances-) | one writer, liveness, warm Sessions, drain, admission | ✅ |
| [G-F33 Prompt cache retention](#g-f33--prompt-cache-retention-) | how long the provider keeps the prompt cache | ✅ |

**Endpoints**

| Feature | What it gives you | Status |
|---|---|---|
| [G-F14 HTTP and streaming](#g-f14--http-and-streaming-) | routes, status codes, SSE, stream resume, the Go API | ✅ |

**Watching and extending**

| Feature | What it gives you | Status |
|---|---|---|
| [G-F13 Events](#g-f13--events-) | one Go type per kind, Subscriptions | ✅ |
| [G-F15 Extensions](#g-f15--extensions-) | six stages around the Loop | ✅ |

**Testing**

| Feature | What it gives you | Status |
|---|---|---|
| [G-F22 Test kit and measurement](#g-f22--test-kit-and-measurement-) | fake models, the reference workload, fault injection | ✅ |

**Later**, **Not planned**

| Part | What it holds |
|---|---|
| [Later](#later) | ten ideas with no milestone |
| [Not planned](#not-planned) | G-N01 to G-N28, things Gotato will not build |

Feature IDs are stable. When features merge, the lower ID stays and the others retire: G-F08 into G-F07, G-F11 into G-F10, G-F23 into G-F22. G-F16 (the CLI, which G-F17 to G-F21 had merged into), G-F25, G-F28 and G-F30 retired (G-F30 became [G-N21](#not-planned)).

## Defining and running agents

### G-F01 — Agents and Runs ✅

You define an Agent once, then run Sessions of it: a message goes in, the Loop works, and a settled result comes out. How it works: [specs/session.md](specs/session.md). Why: decisions [004](plan/decisions/004-an-agent-is-a-definition-and-a-session-is-the-live-unit.md), [016](plan/decisions/016-gotato-session-is-the-live-unit-and-session-record-is-what-the-store-keeps.md), [038](plan/decisions/038-a-limit-guards-a-real-failure.md).

```go
// error handling left out
s, _ := gotato.NewSession(ctx, gotato.WithModel(model), gotato.WithTools(lookup))
defer s.Close(ctx)
result, _ := s.Prompt(ctx, gotato.UserMessage("Where is order 42?"))
fmt.Println(gotato.TextOf(*result.FinalMessage))
```

| Item | What it does |
|---|---|
| `gotato.NewSession(ctx, options…)` | builds a live Session from an Agent's parts: a model, an instruction, tools, a context builder, extensions and limits; its `ctx` covers resolving the ToolSets. In a service, `service.Agent` names the same parts and the Runner builds the Session |
| `gotato.Session` | one concrete type, no interface; its ID is the Session ID (from its Transcript when that is a `session.Record`, else generated); `Status` and `Done` report its life. It lives for one Run: the Runner builds it from the Record, runs it and closes it |
| `Prompt`, `Continue`, `Abort` | `Prompt` sends a message and returns when the Run settles; `Continue` resumes without a new message; `Abort` stops a Run, which still settles and returns its result |
| The Loop | calls the model, runs the tools it asked for, repeats until the model answers, its `ctx` ends, or a `TurnStopper` stops it; the core counts no Turns and no tool calls |
| Parallel tools | one at a time by default; with `MaxParallelTools` above 1, up to that many at once, results committed in the order the model asked |
| Settled result | every Run that starts ends `completed`, `failed`, `canceled` or `deadline_exceeded`, with its final message (carrying the stop reason) and token usage, cache reads and writes included. `completed` never carries an error; every other status always does |
| A write that fails | the Run stops at that step with `store_failure` or `conflict`; a failed model-call write stops it before the model is called. A Run whose reply or end could not be written settles `failed` with `store_failure` and `ErrNotPersisted`, never `completed`, and stores no `run_ended` ([G-F03](#g-f03--stores-)); the next Run starts without that reply. A Run whose only failed write is the release stays `completed`: `session_saved` with `failed: true` and `store_write_failed` warn, and the instance writes the release again on each heartbeat until it lands ([G-F27](#g-f27--service-many-instances-)) |
| A Run refused before it started | an empty `RunResult` with no status, and an error that says it did not run |
| Host code that panics | a panic in a Tool, an extension, a Model (`Stream`, `Recv`), a Journal (`Write`) or a Transcript (`Append`) fails the Run, not the process; the error has a fixed `Message` (`Model panic`, `Journal panic`, `Transcript panic`) and the panic and stack on the `Cause`. A panic in a Recap writer costs the Segment its Recap (`recap_error: model_failure`). A Model's, Journal's or Transcript's own error text stays on the `Cause` unless it is a `PublicError` |
| How a Run ends | the model's final reply, its `ctx`, a `TurnStopper` (its reason is in `run_ended`), the compaction ceiling (`limit_exceeded`, or `context_overflow` when the provider still refuses), or a failure ([Decision 023](plan/decisions/023-a-mistake-the-model-can-fix-is-a-tool-result.md)). A cap on Turns or tool calls is a `TurnStopper` the team writes (`ExampleTurnStopper` in `gotato/runend_test.go`) |
| The Run's deadline | its `ctx`. In a service, `service.Agent.RunTimeout` (30 minutes; negative means none); a request's own timeout (`timeout_ms` on HTTP, a `context` deadline in Go) overrides it |

The limits are passed with `gotato.WithLimits`. A zero field means its default, so `gotato.WithLimits(gotato.CoreLimits{MaxParallelTools: 4})` changes that one field; `gotato.DefaultLimits()` returns the defaults. Each guards a real failure:

| Setting | Default | Why it exists |
|---|---|---|
| `MaxMessages`, `MaxTranscriptBytes` | 10,000 messages, 32 MiB | keeps what the model is sent from growing without bound. They count what a load reads (the last Checkpoint's Messages and what follows, Segment Messages included), so a Session of short messages reaches the ceiling in tokens first ([G-F06](#g-f06--compaction-)) |
| `MaxMessageBytes` | 4 MiB | keeps one reply from filling memory |
| `MaxSteerMessages`, `MaxFollowUpMessages`, `MaxNotices` | 8, 4, 16 | bound what waits for the Run's next boundary; one past the bound is refused with `limit_exceeded` |
| `MaxParallelTools` | 1 | tools run in order unless the team knows they are safe together |
| `ModelCallDeadline`, `ToolCallDeadline` | 2 minutes each | one stuck call ends instead of holding the Run; negative turns a deadline off |
| `MaxTruncatedContinuations` | 0, off | above 0, the Run asks the model to go on after the output limit, up to that many times, in a `continue` notice that starts no Round |

A Tool's progress reports are capped inside the core (1 MiB and 1,000 reports per call; more are dropped and the call goes on). A Tool Result over one page is not a limit but a spill ([G-F35](#g-f35--large-tool-results-)).

**Errors.** Every error a Gotato layer returns is a `*gotato.RuntimeError` and answers three questions, set where it is made and never guessed by a caller or a transport: `Code` says what happened; `gotato.NotRun(err)` is true when nothing with side effects happened, so another instance may run the same request; `gotato.Retryable(err)` is true when the same request, sent later, may succeed. How it works: [specs/errors.md](specs/errors.md). Why: decision [035](plan/decisions/035-the-runtime-says-whether-an-error-ran-and-whether-to-retry.md).

| Code | Not run | Retryable | Meaning |
|---|---|---|---|
| `draining` | yes | yes, on another instance | the instance is draining and takes no new Run |
| `overloaded` | yes | yes, on another instance | the instance is past its limit of active Sessions |
| `limit_exceeded` from `MaxActiveRuns` | yes | yes | the instance is at capacity. The same code ends a Run that reached a limit of its own (the compaction ceiling, a message over `MaxMessageBytes`), which ran and is neither |
| `busy` | yes | yes, after the Run in flight | the Session has a Run, here or elsewhere |
| `not_writer` ([G-F26](#g-f26--steering-a-run-)) | yes | yes, at the writer named in the error | a message or a Stop for a Run another instance writes; the error carries the writer's address (`Details["writer"]`, read with `service.WriterOf`) when it has one |
| `not_found` | yes | no | the Session does not exist (`session.ErrNotFound`) |
| `session_closing`, `session_closed` | yes | no | the live Session takes no more calls |
| `already_exists` | not marked | no | the Session ID the caller chose is taken (`service.ErrSessionExists`); its own code, so a client tells it from other `invalid_state` answers |
| `store_failure` | not known for a write of a Run's step; yes for a load, create, delete, read or Liveness renewal, and for the Claim that opens a Run | yes | the store could not write or read; its error is the `Cause` |
| `model_failure` the provider calls transient (rate limit, overload, broken stream) | no, it ran | yes | the gateway's error says so through `Retryable() bool` |
| `invalid_argument`, `context_overflow`, `conflict` and the rest | as the place that makes them says; a refusal before a Run starts (an empty prompt, an unknown Agent) is not run | no | |

- A code is a `gotato.Code…` constant of type `ErrorCode` whose name matches its string (`gotato.CodeBusy` is `busy`). A sentinel is an `Err…` variable that carries a code (`service.ErrBusy`, `session.ErrExists`, `session.ErrExpired`, `service.ErrNotPersisted`, `modelctx.ErrBadRecap`). `gotato.ErrorCodes()` lists every code, and each surface keeps one table from code to status, so a new code fails a test until every surface maps it.
- `errors.Is(err, sentinel)` matches the sentinel or an error that wraps it, so two sentinels that share a code (`ErrExists` and `ErrExpired` are both `invalid_state`) are told apart; `gotato.IsCode(err, code)` matches any error of a code. Both look through wrapped and joined errors.
- **`Runner.Run` returns a response and an error together.** `err` says whether the request succeeded as a whole; the response describes a Run that started, whatever `err` is. A refusal before the Run started is the zero response with an error marked not run; a non-empty `SessionID` means a Run started and settled.
- **A request can carry the caller's own ID.** `RunRequest.RequestID` and `MessageRequest.RequestID` are recorded in `run_started` (and the Run's `RunInfo`) and echoed in the response. Gotato does not deduplicate in 0.2.0: a repeated ID runs again.
- **Calls that will grow take an options tail.** `AbortSession`, `Sessions`, `Events`, `StreamRun`, `StreamRunAdmitted` and `StreamSession` end with `opts ...Option` (`AbortOption`, `ListOption`, `EventsOption`, `StreamRunOption`, `SessionStreamOption`); only `AbortRun` exists today.
- HTTP only translates ([G-F14](#g-f14--http-and-streaming-)); [RUNBOOK.md](RUNBOOK.md) has the table of every code with its status and exit code.

### G-F26 — Steering a Run ✅

While a Run works, you can change its course without starting over. How it works: [specs/session.md](specs/session.md) (*Steer, FollowUp and Interrupt*) and [specs/multi-instance.md](specs/multi-instance.md) (*A message that misses the Writer*). Why: decisions [021](plan/decisions/021-messages-reach-the-writer-and-take-effect-in-its-process.md), [037](plan/decisions/037-when-the-hash-missed-the-answer-names-the-writer.md).

| Kind | When it takes effect | Limit |
|---|---|---|
| Steer | at the next Turn boundary | 8 waiting (`MaxSteerMessages`) |
| FollowUp | where the Run would otherwise settle; its Turns are Turns of the same Run, so a `TurnStopper` counts them | 4 waiting (`MaxFollowUpMessages`) |
| Interrupt | at once: the model call in flight is canceled and its text kept as a Cut-off reply, a tool in flight finishes (the message follows its result), and the message starts the next Turn of the same Run | shares the Steer queue |
| Notice ([G-F29](#g-f29--runtime-notices-)) | right before the next model call, from the Steer queue; never starts a Run, never keeps one going | `MaxNotices` |
| Abort, Stop | at once: everything in flight is canceled through its `ctx`; the Run settles `canceled` and keeps every Turn it committed | |

- An Interrupt lets a running tool finish, because a write cut in half can leave half a side effect; Stop cancels tools too, and each tool decides how to stop safely.
- A message that reaches a Run already writing its end, or a Session that closed meanwhile, is not queued: its `message_queued` is closed by a `message_dropped` (`run_ended`, `session_closed`) and the call fails with `invalid_state` or `session_closed`; an Interrupt that finds the Run over fails with `invalid_state`.
- A message still waiting when a Run of a bare `gotato.Session` ends is dropped (`message_dropped`). A notice is not dropped; it waits for the next Run. A host that sets `WithQueueHandoff` keeps the messages after a Run that completed or failed (`TakeQueued`, `Requeue`, `DropQueued`); the Runner does.
- A message the writer accepted but has not committed is lost if the writer dies; the caller sees the stream end and sends it again.

**Where messages enter.**

- **Go API:** `Runner.Send(ctx, service.MessageRequest{SessionID, Kind, Text})` with `Kind` `service.MessageKindSteer` (default), `MessageKindFollowUp` or `MessageKindInterrupt`; on a live Session `Steer`, `FollowUp`, `Interrupt` (a `ctx` bounds the store write of `message_queued`; a nil result means the message waits in memory). Stop: `Runner.AbortSession(ctx, id, opts...)`; `service.AbortRun(runID)` stops that Run only.
- **HTTP:** `POST /v1/sessions/{id}/messages` with `{"kind", "text"}`; `POST /v1/sessions/{id}/abort`.
- A Run in flight on this instance takes the message: answered queued with the message ID (HTTP 202). Its Events (`message_queued`, `message_committed`, `message_dropped`) are in the Session's stream ([G-F13](#g-f13--events-)).
- With no Run going, the message is the prompt of a new Run, and **the call holds until that Run settles** (HTTP 200 with `run`; `MessageResult.Run` set; `gotato session message` and a forwarded request do the same). A forwarded request answers when the writer's call returns. A caller that must not wait calls `Send` in a goroutine with a `ctx` that outlives the webhook, reads the reply from the Session's stream ([G-F13](#g-f13--events-)), or sets a deadline only if ending the Run is acceptable (`deadline_exceeded`, or `canceled` when the caller cancels; the message stays in the Record).
- A message that arrives while a Run is ending waits for the release (bounded by the caller's context) and starts the next Run; it is never answered `busy`.
- **Leftovers become a following Run.** A message the instance called queued is not lost because its Run ended: the Runner stops delivering to the settled Run and takes what its queues hold in one step; the following Run starts when the first has saved and released, on the same Session and instance, with the leftovers in the order accepted (the first is its prompt; an Interrupt left over is a Steer). The messages keep their IDs. It takes the Session lock whatever the busy policy and counts as the admitted Run until the last following Run ends, so a drain waits for it and `MaxActiveRuns` does not count it twice. The first caller gets the first Run's result; the following Run is in the Session's and Instance's streams and in the Record.
- **Leftovers are dropped only for a cause that ends the work** (`message_dropped`, with why): the Run was stopped, canceled or past its deadline (`run_ended`); the instance drains (`draining`); the writer failed (`writer_failed`); the following Run's queues are full (`queue_full`); the following Run failed before its live Session took them (`run_failed`); the Session closed with the message in a queue (`session_closed`, for a `WithQueueHandoff` user). A `run_failed` drop is stored when the Run could still write, else it reaches the Instance's stream alone. A writer that dies loses its queued messages, but the instance that takes over writes a `message_dropped` with `writer_gone` for each `message_queued` it left, reading the Record since the last Checkpoint.
- A Stop with no Run anywhere fails with `invalid_state` (`service.ErrNoRun`). A Stop names a Session, so a retry may stop the next Run; name the Run (`service.AbortRun(runID)`) to retry safely, where `ErrNoRun` then means already stopped.

**A message for a Run another instance writes.** Routing by the load balancer's hash comes first; an instance that is not the writer reads the Record's last Claim (the writer's Liveness row must be alive) and answers:

| The Session is | The instance |
|---|---|
| in a Run on another instance's live writer | answers `not_writer`: not run, retryable, with the writer's advertised address (`run_rejected` Instance Event, reason `not_writer`) |
| in a Run here | hands the message to the Run |
| not in a Run | takes the Session over and starts a new Run with the message; a notice is stored and waits for the next Run; a Stop finds nothing to stop |

- The address is the writer's own: `service.Config.Address` (`--advertise`), optional; without it `not_writer` carries none. Gotato builds no network between instances: a forward is the same public request, sent once to an address you configured, and your own authentication applies.
- **HTTP** answers 409 `"code": "not_writer"` from `POST …/messages` with the `Gotato-Writer` header and `writer` in the body (absent when none; a prefix mount such as `http.StripPrefix("/agent", …)` advertises the prefix). The HTTP endpoints forward it themselves when you opt in (`httpapi.WithForwarding`, `--forward`; [G-F27](#g-f27--service-many-instances-)); a forward carries `Gotato-Forwarded: true`, so a receiver that is not the writer answers `not_writer` and never forwards again, and it carries the caller's headers. A forward that fails to connect answers 502 `forward_failed`, unmarked, since the message may have reached the writer.
- **A Go embedder** reads `service.WriterOf(err)` and forwards with its own client and keeps the loop guard (`forwarded` is true when the request it is answering carried `Gotato-Forwarded`; `service/httpapi/forward_snippet_test.go` runs it):

```go
_, err := runner.Send(ctx, req)
if addr, ok := service.WriterOf(err); ok && addr != "" && !forwarded {
	body, _ := json.Marshal(map[string]string{"kind": string(req.Kind), "text": req.Text})
	hr, _ := http.NewRequestWithContext(ctx, http.MethodPost, addr+"/v1/sessions/"+req.SessionID+"/messages", bytes.NewReader(body))
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Gotato-Forwarded", "true") // the writer answers not_writer at most, never forwards again
	resp, err := client.Do(hr)                // your own client, timeout and credentials; 202 queued, or 200 with the Run
}
```
- Only a message for a Run is answered this way: a new prompt for a busy Session keeps the busy policy ([G-F27](#g-f27--service-many-instances-)). A message that reaches the wrong instance as a Run begins or ends is settled by the Claim, so two instances never both run it.

### G-F29 — Runtime notices ✅

Gotato, your service or an extension sometimes has to tell the model something that is not the user's words. A notice is a Message of its own kind (role `notice`, `gotato.RoleNotice`), so the model does not take it for the user and no new Round begins. How it works: [specs/context.md](specs/context.md) (*A Notice joins at the end*). Why: decision [031](plan/decisions/031-a-notice-is-a-message-of-its-own-kind.md); [DESIGNS.md](DESIGNS.md#2-a-request-only-grows-at-its-end).

| Kind | What it tells the model | Caused by |
|---|---|---|
| `continue` | the last reply hit the output limit, go on | `reply_ended` (`MaxTruncatedContinuations`) |
| `time_away` | how long the user was away and what time it is now | the message that starts a Run, when the Session was idle longer than the threshold |
| your own | something outside the conversation | your service or extension |

- `Message.ForModel` writes it as tagged text with kind and text escaped. The constants are `gotato.NoticeContinue` and `NoticeTimeAway`; `gotato.ReservedNoticeKind` says whether a kind is one of them. A Message carries `kind`, `source` (`gotato` or `service`) and, for Gotato's own, `caused_by` (the number of the causing Event).
- It is not counted as a user message, is appended once and never changed (so the cached prefix holds), states a fact, and is kept as sent. A Segment keeps a service's notice word for word and leaves Gotato's two out ([G-F06](#g-f06--compaction-)).
- **When it goes in:** it waits in the Steer queue, in arrival order, and the queue is emptied right before every model call, so a notice follows a user message or a Turn's Tool Results and never sits between a tool call and its result. One waiting when a Run starts goes in after the prompt. It never starts or extends a Run: a Run that settles with one waiting leaves it for the next Run (also after a Stop); a service that wants a reaction calls `Continue` after it.
- `notice_queued` and `notice_committed` report both steps (`queued` on the latter is the number of the former). A full queue (`MaxNotices`, 16; 0 refuses all), or a notice, Steer, FollowUp or Interrupt over `MaxMessageBytes` (`why: "too_large"`), is a `message_rejected` (source `notice` for a notice) and the call gets `limit_exceeded`.
- **A service's queued notice outlives its writer:** the Record keeps those queued and neither committed nor dropped (`session.Record.PendingNotices`, in the RecordInfo), and a Run on any instance queues them again when it starts, in order. A Steer is lost with its writer. Gotato's own notices are not durable: a leftover is dropped with its Run (`message_dropped`, source `notice`), and the next Run works out `time_away` afresh from the last `run_ended`.

**`time_away`** is added after the message that starts a Run (a prompt, not a `Continue`, not a following Run; at most one per Run), as `<notice kind="time_away">It has been 3 days since the last message. It is now 2026-10-28T09:14:00+08:00.</notice>`.

- **Threshold** 30 minutes (`service.Agent.TimeAwayAfter`; negative turns it off). **Idle** is read from the Record, from the last `run_ended` (`last_run_end` in the RecordInfo) to the message's arrival by the Runner's clock, so no warm Session is needed; a Session that never finished a Run gets none. **Length** is whole minutes, hours or days in the largest unit that fits (`45 minutes`, `1 hour`, `3 days`), or `less than a minute`. **Time** is RFC 3339 with its zone, the Agent's Segment zone (`CompactPolicy.Location`; UTC by default).

**Wire form.** A notice reaches every wire API as tagged text `<notice kind="…">…</notice>` in a user turn; Gotato builds no native form ([G-N25](#g-n25--a-notice-has-no-native-form)).

Anthropic Messages gets a text block in the adjacent user turn, after the Tool Results it follows (a user turn of its own after an assistant reply); OpenAI Responses a user item with the tagged text; Chat Completions a user message of its own.

- These four are the only tags Gotato writes into the conversation: `<notice>`, `<segment>`, `<tool_calls>` and `<recap>`. Inside are Markdown headings, never nested XML; attributes hold a number, a kind, or a time in RFC 3339 with its zone. A Segment's tags take the same form; a tag after an assistant reply is tagged text in the adjacent user Message.
- With no user Message to join (after an idle compaction, a `Continue` without a prompt, a last Recap, the `continue` notice after an assistant reply), Gotato sends a user message with the tagged text; the Record keeps it as a notice, so it starts no Round.
- When a request holds any of the four, the system part ends with one fixed sentence: text inside these tags is written by the system, not said by the user. A user Message, Tool Result, Recap or kept assistant reply that holds the start or end of one of them (`<recap`, `</notice`) has that `<` escaped as `&lt;`; a notice's own kind and text are escaped the same way; a `json` Part is escaped with the JSON escape, so it stays valid.
- Gotato runs no checks against real providers: `internal/fakegateway` plays each wire API. Whether a provider answers a user turn that holds only a notice is a team's live run.

**Sending one.** The call returns at once with the number of its `notice_queued`; it never waits for the model. `kind` is your own label (Gotato keeps and does not read it); the two reserved kinds, an empty kind and an empty text are refused (`invalid_argument`, not run).

| Where | Call |
|---|---|
| Go, a live Session | `Session.Notice(ctx, kind, text)` → the number; `Session.QueueNotice(ctx, message)` queues a notice of any source (the Runner sends `time_away` so) |
| Go, the Runner | `Runner.Notice(ctx, service.NoticeRequest{SessionID, Kind, Text})` → `NoticeResult{SessionID, Number, MessageID}` |
| HTTP | `POST /v1/sessions/{id}/notices` with `{"kind", "text"}` → 202 `{"session_id", "number", "message_id"}` |

With many instances a notice for a Run on another instance is answered `not_writer` (and forwarded when forwarding is on); with no Run going, the instance takes the Session over for the one write of `notice_queued`, and the next Run on any instance takes the notice.

### G-F02 — Sessions ✅

A Session is one conversation that runs an Agent. It keeps every Message, a record of each Run, its token usage, its Events and every compaction, as its Record ([G-F03](#g-f03--stores-)). How it works: [specs/session.md](specs/session.md), [specs/record.md](specs/record.md). Why: decisions [004](plan/decisions/004-an-agent-is-a-definition-and-a-session-is-the-live-unit.md), [016](plan/decisions/016-gotato-session-is-the-live-unit-and-session-record-is-what-the-store-keeps.md).

- You create a Session with `POST /v1/sessions` or `Runner.CreateSession`, optionally with your own ID, metadata, instruction and compaction ceiling.
- `session.Record` holds every stored Event as an Entry; a Session over a Record keeps a window of the last 1,000 Events in memory, and a Feed over no stored Record keeps a ring of the last 4,096 ([G-F13](#g-f13--events-)).
- **Fork** starts a new Session from another's Record and records the parent and the position it branches at. It writes the store and activates nothing, so its first request activates it on whichever instance receives it. The fork starts with a Checkpoint of its parent's facts (inherited Segments follow in the same write) and keeps its parent's prompt cache key, so its first Turn reads the parent's cached prefix.
- One Session runs one Run at a time; a second is rejected or waits as the busy policy says ([G-F27](#g-f27--service-many-instances-)).

Per-Session settings live in the Session's metadata:

| Key | Effect |
|---|---|
| `gotato.agent` | the Agent this Session runs (one Runner serves several Agents) |
| `gotato.instruction` | replaces the Agent's instruction for this Session |
| `gotato.compact_ceiling` | this Session's compaction budget, in estimated tokens |
| `gotato.tool.<id>` | a tool switched on or off for this Session |
| `gotato.model` | the model the Agent last asked for; written by Gotato, for information (each reply records the model that answered in `Message.Model`) |
| `gotato.cache_key` | the prompt cache key this Session's requests carry, in place of the hash of its ID; written by a fork, from its parent |

A metadata change is a fact of the Record (`metadata_changed`), and only the Session's writer may write it:

| Surface | How |
|---|---|
| Go API | `service.RunRequest.Update`, a map applied before the Run, under its Claim, as the Run's first write; an empty value deletes the key; needs a Run (`Prompt` or `Continue`); with `SkipSave` it applies to that Run only |
| Go API | `Runner.SetToolActive(ctx, sessionID, toolID, active)` writes only `gotato.tool.<id>` |
| HTTP | none: a run request carries no `metadata`; `POST /v1/sessions` takes it at creation |

There is no call that only changes metadata (`Runner` and HTTP have no `UpdateSession`; `Set` on the `*session.Record` that `CreateSession` and `Runner.Record` return writes nothing). The application's own fields (a chat ID, an owner) are set at creation; a value that changes often belongs in the application's own store, keyed by the Session ID.

## What the model sees

### G-F04 — Context ✅

On every Turn Gotato builds what the model sees from the Session, in a fixed order, so the provider can reuse the cached prefix: instruction, then tools sorted by ID, then the Transcript. How it works: [specs/context.md](specs/context.md). Why: decision [031](plan/decisions/031-a-notice-is-a-message-of-its-own-kind.md).

- Reference text the Agent carries (a schema, a runbook) goes in its instruction (`Agent.Instruction`, `gotato.WithInstruction`). Nothing that changes every Turn is added; a fact the model needs now, such as the time, comes from a tool.
- Cache markers go after the system part, after the tools and on the last message (Anthropic `cache_control`, the last four kept); for a recognized Claude the gateway adds one where the last Round started ([G-F07](#g-f07--models-and-providers-)).
- Every request carries a cache key so a provider serves one conversation from one cache: a hash of the Session ID in the Runner, `gotato.WithCacheKey` with `NewSession`; how it travels is the gateway's configuration.
- Retrieval, a window or any other way to choose what the model sees is your own `ContextBuilder` ([G-N10](#not-planned)).

### G-F05 — Context inspection ✅

You can see what the model would receive now, without calling it. How it works: [specs/context.md](specs/context.md).

- `GET /v1/sessions/{id}/context` returns the request the next Turn would send, built by the live Session a Run would build (so it includes the `ContextTransformer` and `MessageConverter` extensions, tools and model options). In Go: `Session.InspectContext(ctx)` (fails with `ErrBusy` during a Run) and `modelctx.ReportOf(inspection)`.
- The report has the prefix hash, the system part's size and a token estimate (bytes ÷ 4). `compaction_pending` says the next Run compacts first, because the size (last reported usage plus an estimate of what came after) is past the ceiling (`compact_ceiling_tokens` in the report).
- Every Turn emits `context_built` with its prefix hash; the reference workload checks it equals an inspection of that Turn's Transcript ([GOALS.md](plan/GOALS.md) G-G03).
- Limits: inspection builds as a Run's first Turn does, so it cannot know a tool loaded later in a Run, or a `ContextTransformer` that depends on the Run or Turn. Extensions and tool sources run during inspection, so they must not have side effects.

### G-F06 — Compaction ✅

A Session can live for weeks and a long Run can outgrow the window. A compaction closes everything since the last Checkpoint, less a kept tail, into a Segment and appends it with a Checkpoint ([G-F03](#g-f03--stores-)); it never rewrites the Record, so nothing is lost unless the team prunes entries before a Checkpoint. How it works: [specs/compaction.md](specs/compaction.md). Why: decisions [032](plan/decisions/032-compaction-closes-a-segment-and-never-rewrites-one.md), [034](plan/decisions/034-a-segment-is-written-once.md); [DESIGNS.md](DESIGNS.md#3-the-context-is-a-bounded-view).

| Item | What it does |
|---|---|
| On demand | `POST /v1/sessions/{id}/compact` and `Runner.Compact`; `{"keep_messages":N}` (`CompactConfig.KeepMessages`, `--keep-messages`) replaces the kept tail with the last N Messages; `{"min_tokens":N}` (`CompactConfig.MinTokens`, `--min-tokens N`) skips a stretch estimated below N tokens (answer `replaced: false`, `reason: below_min_tokens`; a stretch the tail covers whole is `nothing_to_close`). `Runner.Compact` claims the Session before it checks `min_tokens`; the idle timer checks first and claims only a stretch worth compacting. A draining Runner refuses it as `draining`; a Session running elsewhere is waited for up to `Liveness.Wait`, then `busy`. It uses a warm copy and leaves it warm |
| Automatic | before any model call, mid-Run included, through the `gotato.TurnPreparer` hook (`AutoCompactor`; [G-F15](#g-f15--extensions-)): a compaction that closed a Segment returns `Changed` and the Loop builds the request again from the Checkpoint. A Turn compacts at most once, a dropped compaction counts |
| Kept tail | the latest Run's Turns, up to 20K tokens (`SegmentSettings.TailTokens`) and at most a quarter of the window (of the ceiling when no window is known), cut at a Turn boundary so a tool call keeps its result; always at least the latest Turn. Before a Run's first call the Run that is starting does not count as the latest |
| Segment | a stretch of history that is never rewritten, with a number and its first and last time; written once as its own `segment_closed` Entry (data plus frozen rendered Messages; tag text is a Part marked `gotato.MetaTag`) before the Checkpoint that lists it |
| Checkpoint | the `compaction_ended` Event: lists the Segments the Context uses, holds the kept tail (each Message's ID, Entry number and time, and the Messages, stored because `DropReasoning` rewrites them) and the loaded tools, so every instance builds the same request. `Record.CloseSegmentAndCheckpoint` writes both in one batch; a Checkpoint refuses a number not closed, repeated or out of order. A load reads the facts from the last Checkpoint and every `segment_closed` Entry before it (`Loaded.Segments`), so it grows with the Segments. The Record reads them back with `Segments()`, `Tail()` and `Position(id)` and narrows loaded tools with `Record.SetLoadedTools` |
| Compaction record | the Segment's number, what it replaced, estimated tokens before and after, the tools kept loaded, `recap` (the writer's name, `none`, or `digest` for a merge), `recap_error` (the failure's code, `empty_recap` or `skipped_overflow`; absent when there is no writer) and `recap_usage` (also when the reply failed the rules). A merge is a compaction of its own: `segment` is the merged number, `merged` lists the numbers it stands for. Usage only, no cost; an idle compaction's usage is on its Event, not a Run |
| `read_segment(segment, page?)` | offered from the Checkpoint once a Session has a Segment (also in `Runner.Tools`); declares `read`. Returns the Messages as they were, tool results included and reasoning left out, each first line prefixed `[user]`, `[assistant]`, `[call tool.id]`, `[tool tool.id]` (`[tool x failed]`), `[notice]`; the four tags are escaped and a line starting with a prefix gets a backslash. A page is 32 KiB (`modelctx.DefaultReadSegmentPageBytes`, about 8K tokens), `Page 2 of 5` first and the next call last; a line over half a page is cut. A bad page or number says how many pages or which Segments exist. It reads the Record's log from the Segment's first Entry (cost grows with what came after); a fork's Segment is read from its parent's log, checked against `source.record_epoch`; pruned Entries read as removed |
| Loaded tools | the tools the closed Segment called and the kept tail calls stay loaded, the others unload (`tool_unloaded`); a tool of an active ToolSet never unloads ([G-F34](#g-f34--tool-search-)) |
| Reasoning | when the model is sent only the current Round's reasoning (recognized Claude or OpenAI), the kept tail loses its reasoning (`modelctx.DropsReasoning`; `CompactConfig.DropReasoning` with `modelctx.Compact` alone); DeepSeek and unrecognized providers keep it |

**What a Segment keeps.** Code builds it; only the Recap comes from a model.

| Part | In the Segment |
|---|---|
| the user's messages, each Run's final reply | word for word, cut at 2K tokens each with a line saying `read_segment` returns the rest; an image or file becomes a placeholder naming its kind, name and size |
| each tool call | one line inside its Run: time, tool, arguments cut at 200 characters (escaped), `ok` or the error code; repeats counted; past 100 lines the rest counted by tool name; adjacent assistant Messages become one |
| a Run's other text, reasoning, tool results | left out (facts the tools returned survive only in the Recap) |
| a service's notice | word for word, as `<notice kind="…">`, escaped |
| Gotato's own notices | left out; the Recap covers them |
| the Recap | what happened, key facts, decisions, open items, where it stands |

Messages are grouped by the Run that committed them (`Message.RunID`); a Message with no Run ID is kept or left out by its own rule; a continuation keeps its Run's ID. Times are UTC unless the Agent sets a zone (`CompactPolicy.Location`).

**The Recap.** The Agent's own model writes it, on the Context the next call would send plus one plain user message (never stored), output limit 8K tokens. It is off by default in the library and the service; a team turns it on with `Recap: modelctx.ModelRecap{}` on `CompactPolicy.Recap` (the Runner supplies the model) or `CompactConfig.Recap`, once the payments check ([G-E01](plan/GOALS.md#g-e01--the-recap-keeps-the-facts-)) passes on its own gateway.

- It fails when the reply calls a tool, is cut at the output limit, does not start with one of the five `## ` headings (`What happened`, `Key facts`, `Decisions`, `Open items`, `Where it stands`), uses any other `## ` heading, or lacks `## What happened` (`modelctx.ValidateRecap`). The Segment is then kept without a Recap and its Event says `recap: none` with a `recap_error`.
- `modelctx.RecapWriter` (`Name()`, `WriteRecap(ctx, RecapRequest)`); `ModelRecap{Model, MaxTokens, Label, Timeout}` (`ModelRecap.Timeout` defaults to the two-minute model call deadline; a `ModelRecap` with no Model is refused with `ErrNoRecapModel` by `Compact` and `AutoCompactor` (`CompactPolicy.Validate`) unless a Runner fills it); `TruncateRecap` for tests and use without a model. A `RecapRequest` holds the Segment's number, the closed Messages, when the stretch began, the time zone and `Context`, the request the next call would send (`modelctx.WithRecapContext`; `AutoCompactor` supplies `PendingTurn.Request`, the Runner builds one for on-demand). A `ModelRecap` with no Context fails. `modelctx.WithCompactionWhy` records why it ran.

**When it runs.** Each trigger is recorded as the compaction's `why`.

| Trigger | When | Why |
|---|---|---|
| Idle | a warm Session idle for min(TTL, cache retention) less 5 minutes (the TTL less 5 when the retention is unknown or under twice the lead; the lead is at most half the TTL), and the stretch since the last Checkpoint holds at least 20K tokens | `before_cache_expires`, or `before_unload` when the TTL comes first or the retention is not used |
| Past the ceiling | before any model call, mid-Run included | `past_ceiling` |
| Overflow | the provider refuses a request as too long, before any output; a refusal after output is not retried | `overflow` |
| On demand | the compact endpoint or `Runner.Compact` | `on_demand` |
| Past budget | at the start of a Run, when the Segments pass their budget and there is nothing to close (a merge only) | `past_budget` |

- **Idle** runs on the warm Session's own timer, outside any Run; a message that arrives cancels it and is served first, also when the queue policy rejects a Run on a busy Session. A compaction that cannot be written drops the copy (`session_unloaded` with `failed`). A service that keeps its own timer calls the compact endpoint shortly before the cache expires with `min_tokens` set; that is recorded `on_demand`.
- **Ceiling** = the model's `context_window_tokens` less max(10% of it, 16K tokens), unless the Agent (`CompactPolicy.CeilingTokens`) or Session (`gotato.compact_ceiling`) sets one; a negative `CeilingTokens` turns the trigger off for a model with a window; with no window and none set it is off, and a ceiling set without a window is used as is. The size is the last reported input plus output tokens plus an estimate (bytes ÷ 4) of what came after; after a compaction it is estimated afresh.
- **Overflow:** the Session is compacted and the call sent again, unless the Turn already compacted. The Segment that closes gets no Recap (`skipped_overflow`); the oldest Segments merge too when the Segments alone are past the limit (the limit the refusal names in tokens, else the budget). The gateway reads the limit from the provider's wording (`"maximum context length is N tokens"`, `"N tokens > M maximum"`, `"… + … > M"`, `"context window of M"`) through the error's `ContextLimit()`; `PendingTurn.Refusal` hands it to the preparer. The Runner turns this trigger on for every Agent.
- Still past the ceiling after the Turn's compaction: the Run ends `limit_exceeded` (its error says: still past, nothing to compact, or the compaction failed); still refused as too long: `context_overflow`. `MaxToolResultBytes` is the first guard.

**Merging.** Segments have a token budget: half the ceiling, or 32K tokens with none (`CompactPolicy.SegmentBudgetTokens`; size is the estimate of their rendered Messages). Past it, the oldest merge, down to half the budget and at least two, into the one merged Segment that always opens the Context, named by its range (`<segment n="1-3">` and `<recap n="1-3">`; the next merge gives `<segment n="1-7">`, `1-3,5` when a number is missing). It runs right after any compaction that closes a Segment, at the start of a Run (`past_budget`; a failed merge is tried again there, never between Turns), and on an overflow. A merge is appended as `compaction_started`, a `segment_closed` Entry with the next number (which lists the Segments it stands for, with times and Entry ranges, so a fork reads them from its parent), and `compaction_ended`. The originals stay readable by their own numbers; one Segment alone past the budget is left as it is. When a merge on an overflow fails (its Recap or its write), nothing is merged and the Run ends `context_overflow`, but the Segment that closed stays.

- **Recap of a merge:** written from the Recaps alone (one user message, no instruction, tools or Context), under the same five headings and rules, output limit 8K; a Segment with no Recap contributes its digest. `ModelRecap` is a `modelctx.MergeRecapWriter`. With no model, `TruncateRecap` or a writer that is not one, the merged Segment holds a digest built by code (`digest`): per Segment its times, the start of the user's words (160 characters each, three at most), tool calls counted by tool and the start of its Recap (300 characters; a merged one 1,200), cut at 4,000 bytes dropping the oldest lines. A digest keeps no tool result, so a team that merges often turns the model's Recap on.

**A compaction that fails** stays local:

| Failure | What happens |
|---|---|
| the Recap fails | the Segment is kept without one; the call's usage is still recorded |
| the Record append fails | the compaction is dropped (`compaction_failed`), the Record and loaded tools are as they were, an on-demand call returns the error, the automatic one lets the Run go on; the Writer then stops at its first failed write ([G-F03](#g-f03--stores-)) and the Run's later steps report `ErrNotPersisted`. The size is estimated afresh from the Messages, so a Run under the ceiling goes on |
| the Run is stopped during it | the Run settles `canceled` |

**Settings.** Defaults are given where each item is described; every count carries its unit (`modelctx.SegmentSettings`: `Location` (`CompactConfig.Location`, `CompactPolicy.Location`), `MessageTokens`, `ToolLines`, `ArgChars`, `TailTokens`, carried by `CompactConfig` and `CompactPolicy`, with `WindowTokens`, `CeilingTokens`, `SegmentBudgetTokens`). Idle compaction is on (`WarmPolicy.NoIdleCompaction`, `--no-idle-compaction`; lead `WarmPolicy.IdleLead`, smallest stretch `WarmPolicy.IdleMinTokens`); the ceiling trigger is on when the window is known.

The instruction and tool definitions are built from the current Agent on every request, so a compaction carries neither; the model is not told how much window is left. Gotato keeps no prices, so a compaction records usage only.

**Checked against the providers' behavior.** Gotato runs no checks against real providers; it proves each wire API with unit tests and `internal/fakegateway`. What only a real model shows, a team checks on its own gateway, and the model's Recap stays off until it does: the Recap's quality (`go run ./internal/cmd/gotato-workload -recap-check -live -gateway-config gateway.yaml -min-share 0.8`, [G-E01](plan/GOALS.md#g-e01--the-recap-keeps-the-facts-)); how a model reads a `<tool_calls>` block; what Claude and OpenAI need of the reasoning in a tail kept mid-Run; whether Claude reads the Segments from the cache with the marks of [G-F04](#g-f04--context-) and whether a mark at the end of the last Segment would pay. Where an answer would change what Gotato does, the choice is a per-wire-API default the gateway configuration can override.

## Tools

### G-F09 — Tools and their Effect ✅

A tool is one thing an agent can do. How it works: [specs/tools.md](specs/tools.md). Why: decisions [005](plan/decisions/005-no-approval-system.md), [014](plan/decisions/014-the-effect-cap-hides-tools-above-it.md), [015](plan/decisions/015-only-the-agent-sets-the-effect-cap.md).

```go
lookup, _ := gotato.NewFuncTool("orders.lookup", "Finds an order by its ID", findOrder)
lookup = gotato.ToolWithEffect(lookup, gotato.ToolEffectRead)
s, _ := gotato.NewSession(ctx, gotato.WithModel(model), gotato.WithTools(lookup, refund),
	gotato.WithMaxEffect(gotato.ToolEffectRead)) // refund is never offered
```

| Item | What it does |
|---|---|
| `gotato.NewFuncTool` | a Go function as a tool; the input schema comes from the Go type, and every call is checked against it before the tool runs (`type`, `enum`, `required`, `properties`, `additionalProperties`; other keywords pass unchecked) |
| Effect | `read` < `write_local` < `write_external` < `destructive`; a tool that declares none counts as destructive; `gotato.ToolWithEffect` sets it |
| Tool result | carries a status; a failure carries a safe message. An error a tool returns becomes a failed result whose text reaches the model and subscribers only if it is a `RuntimeError` or implements `gotato.PublicError`, otherwise `tool_execution_failure: tool failed` |
| Bad calls | an unknown tool or arguments that are not valid JSON or fail the schema fail as a result that says what was wrong (for an unknown tool, it lists the tools shown); the Run goes on (G-G04) |
| Effect cap | `gotato.WithMaxEffect`, `service.Agent.MaxEffect`; none by default; a Session can neither raise it nor switch on a hidden tool. A tool above it is in no request, no inspected Context, no tool listing and never in the `search_tools` index; the discovery tools declare `read`, so every cap keeps them. A guessed call of a hidden tool gets a failed result saying the Session does not allow it, listing only visible tools. `GET /v1/agents` shows `max_effect` per Agent (none for an uncapped Agent). Checked by G-G05 |

### G-F10 — Tool sets ✅

The tools a Session offers can change while it runs. How it works: [specs/tools.md](specs/tools.md). Why: decision [039](plan/decisions/039-tools-load-one-by-one-from-a-catalog.md).

A Tool is the unit of loading; a ToolSet is a grouping with a source and a name, a description and the tools of one source (one MCP server is one ToolSet, [G-F12](#g-f12--the-mcp-adapter-)). A Tool's ID is `<set>.<tool>`. Tools come from your own Go code or an MCP server, through `gotato.WithTools` or a `ToolSource` the Session asks again, with the Run's `ctx`, at the start of every Turn (a source that fails keeps the last set and stores an `extension_failed` Event, stage `toolsource`, as a failing ToolSet does).

| Install | What the model sees |
|---|---|
| `WithActiveToolSet(set)` | every tool of the set, always, as a base tool |
| `WithToolSet(set)` (default) | the set's description and all its tool names, in the catalog |
| `WithFoldedToolSet(set)` | the set's description and a count of its tools |

- **Resolution.** `NewSession` resolves each ToolSet once and every later Run, a warm Session's too, resolves them again before its first Turn, never inside a Run, so the catalog is the same on every request of a Run and an MCP `list_changed` takes effect at the next Run. All ToolSets are asked at once and answers kept in install order. A failed ToolSet (error, panic, nil, invalid or duplicate Tool) is skipped: the catalog keeps its description with `(unavailable now; no tools)`, the other tools work, and an `extension_failed` Event with `stage: "toolset:<name>"` and `code: tool_resolution_failure` warns once per Run while it stays down. Only a `ctx` that ends fails the resolution.
- **Loaded tools** stay until the next compaction ([G-F34](#g-f34--tool-search-)); there is no limit on how many are loaded or resolve together (`MaxActiveToolSets` is gone). A loaded tool its ToolSet no longer carries is unloaded by a stored `tool_unloaded` with `why: gone` before the Run's first Turn; a loaded tool of an unavailable ToolSet is only hidden for that Run.
- **Per Session:** `Runner.SetToolActive`, stored as `gotato.tool.<id>` (a tool of the Agent or a qualified ID like `files.read`); a tool switched off is never in the catalog, found or loaded, so the switch wins over loading. To find a ToolSet's tool the Runner builds the live Session before taking the lock, within `Config.ToolSetTimeout` (10 seconds; negative for no bound; `deadline_exceeded` on timeout). A name in an unavailable ToolSet is refused with a message that says so; a tool above the cap stays unknown. In the Go API, `gotato.WithToolFilter`.
- **Listing:** `Runner.Tools` with a Session names every tool of every ToolSet (`id`, effect, `toolset`, `loaded`, `active`); an unresolved ToolSet is one entry with `unavailable: true`; on timeout it returns the Agent's own tools with `deadline_exceeded`. Without a Session it lists the Agent's own tools only. HTTP has no tool listing or switch.

### G-F34 — Tool search ✅

A service that connects MCP servers can offer hundreds of tools. The model reads a catalog and loads the tools it needs, one by one. How it works: [specs/tools.md](specs/tools.md). Why: decision [039](plan/decisions/039-tools-load-one-by-one-from-a-catalog.md); [DESIGNS.md](DESIGNS.md#2-a-request-only-grows-at-its-end).

| Item | What it does |
|---|---|
| Catalog | the description of `search_tools`; lists each ToolSet by its install mode and lists a loaded tool like any other, so its bytes do not change while tools load. No size budget: a team folds a big ToolSet itself |
| `search_tools` | declares `read`. `names[]` loads exactly those tools; `queries[]` searches each query, loads the top 3 and lists 5 more (fixed counts); `toolset` lists a folded ToolSet's tools with descriptions and loads none. The result is short (`Loaded: …. Also found, not loaded: ….`) and never repeats a schema; a name that matches nothing, or a tool the Session may not load, is named as not found |
| Search | BM25 over each tool's ID and description (`k1` 1.2, `b` 0.75, an ID word counts three times a description word). `ToolSearcher` (`Search(ctx, query, tools []ToolSpec) ([]string, error)`, `gotato.WithToolSearcher`) plugs in your own; it gets one query and the specs of tools the Session may still load; an ID not among them is ignored |
| Index | only tools of installed ToolSets the Session may use: not above the Effect cap, not switched off |
| Facts | `tool_loaded` (`names`, `query` or `call`) and `tool_unloaded` (`compaction` or `gone`); a Checkpoint lists `loaded_tools` (also on `session_created`, `compaction_ended` and a Segment), and a Fork carries them |
| A call to an unloaded tool | does not run; the tool is loaded and the failed result says `<id> was not loaded, so this call did not run. It is loaded now; call it again.` |
| How long a tool stays | until the next compaction, which keeps the tools called in the closed Segment or the kept tail |

**The cache.** The tool list comes before the Transcript, so adding a tool re-reads everything after it. The gateway key `deferred_tools` chooses the form:

| Value | What happens | Default |
|---|---|---|
| `native` | every tool is in the request from the start, deferred, so the list has the same bytes on every request; a load only tells the provider which is in use. Anthropic Messages: `defer_loading` plus `tool_reference` blocks (`tool_name`) after the `search_tools` result, `cache_control` on the last non-deferred tool. OpenAI Responses: `search_tools` becomes a `tool_search` entry with `execution: "client"`, catalog tools are functions with `defer_loading: true`, a call is a `tool_search_call` item and its result a `tool_search_output` | `anthropic-messages`, `openai-responses` |
| `list` | a loaded tool is added to the tool list, sorted by ID; costs a cache read after the tools once per load, which is why tools unload at a compaction and nowhere else | `openai-chat-completions` (no native form; `native` there falls back to `list`) |

- The core stays wire-agnostic: a `ModelRequest` carries the visible tools, `ModelRequest.LoadedTools` and `ModelRequest.UnloadedTools`, and a Tool Result lists what it loaded (`ToolResult.LoadedTools`, `loaded_tools`). In `native` the tools outside the catalog come first, then every catalog tool in ID order. A loaded tool no Tool Result references any more (a compaction folded the result) is sent as a plain tool, changing the list once, at that compaction. The prefix hash of `context_built` covers loaded and unloaded tools, so it changes at a load in the native form too.
- A change a service makes in the middle of a Session (a tool switched on for one Session) rewrites the tool list: see [Later](#later), *Changes that keep the cache*.
- **Not checked against a real provider.** The `tool_search` task runs through `anthropic-messages` and `openai-responses` on `internal/fakegateway` (all 8 requests accepted; same tool-list bytes while tools load). What the fake cannot show: how a provider expands a `tool_reference`; whether it takes a `cache_control` mark on a list ending in deferred tools; how OpenAI streams a `tool_search_call` (read from `response.output_item.added` and `.done` as a `function_call` is; the documentation is silent); the `search_tools` text (`Already available`, `Not found`) is not replayed in the `tool_search_output` form; on Anthropic a load caused by a call to an unloaded tool is a failed Tool Result that carries the `tool_reference`, which the documentation shows only for success. A team whose gateway refuses `native` sets `list`.

### G-F12 — The MCP adapter ✅

Tools a team already runs as MCP servers reach a Session as a ToolSet. The adapter is `adapter/mcp`, a Go module of its own on the official `github.com/modelcontextprotocol/go-sdk` (v1.8.0, Apache-2.0); the core and runtime packages never import it. A server is one ToolSet, its tools `<server>.<tool>`. How it works: [specs/tools.md](specs/tools.md) (*MCP as a ToolSet*).

```go
crm, _ := mcp.NewToolSet("crm", mcp.StreamableHTTP("https://crm.internal/mcp"), mcp.WithDescription("Customer records"))
defer crm.Close()
s, _ := gotato.NewSession(ctx, gotato.WithModel(model), gotato.WithFoldedToolSet(crm))
```

| Name | What it does |
|---|---|
| `mcp.NewToolSet(name, transport, options...)` | the ToolSet of one server; connects to nothing, so a server that is down never fails it |
| `mcp.StreamableHTTP(url, WithHTTPClient(c), WithoutNotifications())` | the HTTP transport, used statelessly so any instance can make any call |
| `mcp.StdioCommand(func() *exec.Cmd)` | a subprocess shared by the instance's Sessions, never one per Session; the command sets its environment and directory |
| `mcp.WithDescription(text)` | the catalog description; without it, the first paragraph of the server's `instructions` on one line (empty until the first resolution connected; cut at the paragraph, at no length) |
| `mcp.WithConnectTimeout(d)` | bounds one connect (10 seconds); a server that never answers makes the ToolSet unavailable for that Run only; callers waiting are bounded by their own context; `Close` does not wait |
| `mcp.WithToolEffect(tool, effect)` | a per-tool Effect that replaces the annotations' |
| `mcp.WithListCache(ttl)` | keep the tool list for `ttl` unless `list_changed` arrives; default 0 asks at every resolution |
| `mcp.WithClientInfo(name, version)` | what the client tells the server |
| `(*ToolSet).Skipped()` | tools left out of the server's list (blank name; a name listed twice keeps the first) and why |
| `(*ToolSet).Close()` | ends the connection and subprocess; the service closes it at shutdown, a Session never does |

- A `*ToolSet` is made once and installed in every Session that needs the server; it connects at its first resolution and keeps one connection for all Sessions (a closed one is made again by the next call). Every call takes the Run's `ctx`.
- **Authentication** is the team's `http.Client` (`WithHTTPClient`): a fixed header, a refreshing token (the `Transport` of `golang.org/x/oauth2`), mutual TLS; for stdio the command's `Env`. A `RoundTripper` can send the token of the end user from `req.Context()`: each `tools/call` carries its own caller's token though the connection is shared, while `initialize`, the standalone stream and the closing `DELETE` run in a `ctx` with no values. The MCP authorization flow is not offered ([G-N20](#g-n20--mcp-features-gotato-does-not-build)).
- **Effect from annotations**, with the specification's default for a missing hint: `readOnlyHint` is `read`; not destructive and closed world is `write_local`; not destructive and open world is `write_external`; anything else `destructive`.
- **Schemas are slimmed:** `$schema`, `$id`, `examples` and a `title` equal to the tool's (or property's) name are dropped, only in keywords that hold schemas; bytes are the same on every instance (sorted keys).
- **Content becomes Parts:** text to text; image to image; `structuredContent` to JSON (a text block that only repeats it is dropped); embedded text resource to text under a line with its URI; embedded image blob to image; audio, resource link and other blobs to one line naming the content, type and size or link. `isError` is a failed Tool Result the model reads; a protocol or transport failure is a failed call. `_meta` is copied as strings with the `gotato.MetaTag` key dropped, so a server cannot write tagged text.
- **Progress** (`notifications/progress`) reaches the Tool's `ToolProgressFunc` and so `tool_progress` (a call with a progress function carries a progress token; the text is the server's message or `progress/total`, e.g. `2/3`; one arriving after the result is dropped). A server's `list_changed` takes effect at the next Run; a server down at a Run is skipped with a warning. A large result is spilled ([G-F35](#g-f35--large-tool-results-)).
- **A tool that asks for input mid-call** ends with a failed Tool Result: the client declares neither elicitation nor sampling and turns the SDK's automatic reply off; a multi round-trip request gets a failed result telling the model to call again with everything in the arguments, and a server's own request gets "client does not support …". Nothing waits.
- Tested against the SDK's in-process server (in-memory transport), streamable HTTP on loopback and stdio. Not checked: a server of the specification's multi round-trip revision (none was tried). What is not built: [G-N20](#g-n20--mcp-features-gotato-does-not-build).

### G-F35 — Large Tool Results ✅

A tool can return far more than a Context should hold. A Tool Result over one page is spilled into the Record and read in pages. How it works: [specs/tools.md](specs/tools.md), [specs/record.md](specs/record.md). Why: decisions [040](plan/decisions/040-a-large-tool-result-is-spilled-and-read-in-pages.md), [041](plan/decisions/041-a-read-only-bash-for-large-results-is-another-project.md).

| Item | What it does |
|---|---|
| Page | 32 KiB of text, about 8K tokens (`gotato.SpillPageBytes`); a text result longer than a page is spilled, exactly a page is not. Only text is spilled; an image or file stays in the Message and counts toward `MaxMessageBytes` (4 MiB), which a text result no longer reaches |
| Entry | the full body is a `tool_result_spilled` Entry written before the Message; the Message holds a preview and a ref and records the position in `ToolResult.Spilled` (`seq`, `bytes`, and for a fork `ToolResult.Spilled.session_id`, the parent, a grandchild keeping the first, and `record_epoch`). A load does not read a body until asked |
| Preview | the start of the body, 2K tokens (8,000 bytes, `gotato.SpillPreviewBytes`) on a rune boundary, then `[Result cut: 412 KiB, 13 pages. Read it page by page with read_result(ref="result:call_9a2", page=1); the preview is only the start of page 1.]`; ref is `result:<call_id>` |
| `read_result(ref, page?)` | sibling of `read_segment`, same paging code (`Page 2 of 13`, next call named, past-the-end error says the page count, the four tags escaped); declares `read`; offered whenever the Agent has a tool (so the list does not change when a result first spills; an Agent with no tool is not offered it). Reads only its own Session's spilled results: another ref fails as a failed Tool Result listing the refs the Context holds. A call inside a closed Segment is found by a scan of the log: the Record's own, then each Segment's source Session |
| Warm cache | a warm Session keeps bodies it wrote or read in memory, dropping bodies the store holds oldest first past 16 MiB (never one the store lacks); counted apart from the warm-Session limit |
| Fork | reads bodies from its parent's log; the ref keeps the parent's `record_epoch`, so a parent deleted and created again reads as pruned |
| Pruned | `read_result` says the body was removed |
| Events | subscribers, the retained window and HTTP events see `tool_result_spilled` with `call_id`, `tool_id`, `bytes`, never the body |
| Only over a Record | a Session spills when its Transcript keeps Entries (`gotato.SpillKeeper`: `session.Record` or `session.Writer`); the plain in-memory Transcript commits the result whole. The spill depends on size alone, so every instance builds the same Message |

## Models

### G-F07 — Models and providers ✅

A provider is configuration, not code. Any vendor that speaks a supported wire API works without new code, and you can bring your own client by implementing `gotato.Model`. How it works: [specs/model.md](specs/model.md). Why: decisions [001](plan/decisions/001-one-implementation-per-wire-api.md), [029](plan/decisions/029-reasoning-goes-back-for-the-current-round.md).

Wire APIs (`api:`): `openai-chat-completions` (the default), `openai-responses`, `anthropic-messages`. A provider is a few lines of YAML that `gateway.LoadYAML(path)` reads (`api`, `base_url`, `api_key: ${GOTATO_GATEWAY_API_KEY}`, `model`); [gateway/gateway.example.yaml](gateway/gateway.example.yaml) is the full file to copy, tested so it stays current. The YAML file is the only way to configure the gateway; a secret reaches it through `${NAME}`, and an unknown key is an error that names it.

| Behavior | What happens |
|---|---|
| Retries | a call with no response, or 429 (except a hard quota), 500, 502, 503, 504, 529, or an error that says rate-limited or overloaded; a provider's `Retry-After` or `retry-after-ms` wins over Gotato's backoff. A failed send is retried only when it can pass (timeout, reset, refused connection); an untrusted certificate or unknown host returns at once |
| Broken streams | a stream that breaks is sent again within the retries ([G-F31](#g-f31--recovering-a-cut-stream-)). An error the provider sends mid-stream (`overloaded_error`, `api_error`, `server_error`, `rate_limit_exceeded`) is marked retryable on all three wire APIs but never sent again by the gateway: the stream started, so the caller decides. An unknown finish reason or status, or a Responses function call without a name or `call_id`, ends with `model_protocol_failure` |
| Request | carries only what the Agent and the Config set; Responses is sent no instruction or verbosity of its own. `ModelConfig.ReasoningEffort` and `ReasoningSummary` go on Responses (`reasoning`); on chat and Anthropic a request that sets either is refused `not_supported` before anything is sent |
| Reply | cut by the output limit reports `max_tokens`, even next to tool calls. `Message.Model` records the model that answered (`model` in each chat chunk, Responses' `response.created` and `response.completed`, Anthropic's `message_start`), the fallback behind a gateway; never sent back. Cache reads are counted on every wire API (`cached_tokens`, DeepSeek's `prompt_cache_hit_tokens`) |
| Reasoning | kept in the Transcript and sent back as each wire API carries it: `reasoning_content` (chat, as DeepSeek requires), encrypted items (Responses), signed `thinking`/`redacted_thinking` (Anthropic) |
| Chat extras | a tool call's `extra_content` is kept and sent back as it came (Gemini 3's thought signature; not yet run against Gemini). Parallel tool calls stay apart when the provider sends no index, a null one, or all at 0: a delta naming a new call ID starts a new call |
| Tool names | a tool goes under its own ID when the provider accepts it, else each other character becomes `_` (`demo.echo` is `demo_echo`) |
| Refused as too long | a 400 or 413 whose message, code or type says the request exceeded the context window is an error answering `ContextOverflow()`, ending a Run `context_overflow`; an Anthropic stop `model_context_window_exceeded` mid-reply is the same error and the text received stays as a Cut-off reply; so is an in-stream `{"error": …}`/`error` event on chat and Responses. Phrases are matched without regard to case; `overflow_phrases` adds a team's own. A `max_tokens` alone too large is not an overflow. `internal/fakegateway` plays each provider's form |
| Headers | `gateway.WithRequestHeaders` adds headers to one Run's requests through its context; neither it nor `headers` can replace authentication, content-type or streaming headers; `User-Agent` defaults to `gotato/0.1` |
| Cache key | goes in the body field `cache_key_field` names and the header `cache_key_header` names. Responses defaults to `prompt_cache_key`; chat sends no field unless named; `anthropic-messages` takes no field (it caches by `cache_control`) but a header works. A provider that keys by a session header needs `cache_key_header` such as `session-id`, else every Turn is cold |

**Provider rules.** The gateway follows a provider's rules for reasoning and the cache for the provider `provider` names or, without it, whose own address it calls (`api.anthropic.com`, `api.openai.com`, `api.deepseek.com`), never from a model name; `provider: other` turns this off. Checked against the providers' documentation on 2026-10-18, not against the providers.

| Provider | Reasoning sent back | Added to the request |
|---|---|---|
| `anthropic` | only the current Round's | `cache_control` with `"ttl": "1h"`; on Fable 5.1, Opus 5.5, Sonnet 5.5 and Haiku 5.5, `thinking.block_binding` with `drop_block`, its beta header, and `thinking.type: "adaptive"`, their default |
| `openai` | only the current Round's | on Responses from GPT-5.6, `prompt_cache_options.ttl: "30m"` |
| `deepseek`, any other | all of it | nothing |

- A Round is a user Message and every reply and Tool Result after it; a Steer starts one, a Continue without a prompt stays in its Round; on Responses an earlier Round's function call goes without its item ID too. DeepSeek (chat and Anthropic APIs) refuses a request that leaves out earlier `reasoning_content` once tools are in play, as an unknown provider may; a new model gets the reasoning as it is.
- Block binding: Claude's newest models check a thinking block follows the prompt it was written under; `drop_block` asks Claude to drop a block whose prefix changed rather than refuse; `no_drop_block: true` turns it off; the `input_transformations` Claude reports are not read yet.
- Cache retention: without `cache_retention` a recognized provider gets its longest (a shorter value than its longer option keeps its default; OpenAI before GPT-5.6 keeps its own, 24 hours without Zero Data Retention). `Client.CacheRetention()` reports it: 1 hour or 5 minutes on Anthropic, 30 minutes on Responses when sent, 0 (unknown) elsewhere.

| Key | Default | What it does |
|---|---|---|
| `api` | `openai-chat-completions` | the wire API |
| `endpoint` or `base_url` | — | where the provider listens |
| `api_key` | — | the key; `${NAME}` reads the environment |
| `model` | — | the model to call |
| `context_window_tokens` | none | the model's window in tokens, from which compaction takes its ceiling ([G-F06](#g-f06--compaction-)); Gotato keeps no table of windows. `Client.ContextWindowTokens()` |
| `headers` | none | headers a provider or proxy requires |
| `cache_key_field` | `prompt_cache_key` on responses, none on chat; `none` turns off | the body field for the cache key |
| `cache_key_header` | none | the header for the cache key |
| `deferred_tools` | `native` on `anthropic-messages`/`openai-responses`, `list` on chat | how loaded tools reach the provider ([G-F34](#g-f34--tool-search-)); `Client.DeferredTools()` |
| `max_retries` | 2; `-1` none | retries |
| `retry_backoff` | 200 ms, growing, with jitter | spreads retries |
| `max_retry_wait` | 60 s | caps the wait, a provider's `Retry-After` included (`Retry-After: 3600` does not hold a Run an hour) |
| `connect_timeout` | 10 s | the dial and the TLS handshake, each |
| `response_header_timeout` | 60 s | wait for response headers after the request is sent |
| `provider` | recognized from the address, else none | `anthropic`, `openai`, `deepseek` or `other` |
| `cache_retention` | the recognized provider's longest | e.g. `cache_retention: 5m` keeps Claude's default and its lower write price |
| `no_drop_block` | false | stop asking Claude to drop a block whose prefix changed (the gateway picks `block_binding` by model name, `thinkingPrefixModels` in `gateway/reasoning_replay.go`) |
| `overflow_phrases` | none | the team's own phrases or codes added to the overflow list (`Config.OverflowPhrases`) |

### G-F31 — Recovering a cut stream ✅

A reply cut off partway must not fail the Run. A stream cut after some output is sent again, the way one cut before any output is. How it works: [specs/model.md](specs/model.md) (*A stream cut after output*).

- The identical request is sent again within `max_retries`; each time a `model_retried` Event says so (attempt, wait, why). The reply cut off stays in the Record only as a log, as a Cut-off reply (`StopReasonCutOff`, `reply_cut_off`) that no model is ever sent; it keeps the text received only, not tool-call arguments or reasoning. The new reply is its own, with its own `reply_started`.
- **A stream reader who sees `model_retried` discards what it received of that reply** (the `text_delta` and `reasoning_delta` since the last `reply_started`). Events come in this order: `model_retried`, `reply_cut_off` when text was kept, `reply_started`.
- When the attempts run out the Run fails with `model_failure` and the Record keeps the last Cut-off reply. A refusal of the request (`context_overflow` included), an Interrupt, a canceled `ctx` and a deadline are not cuts. Only a transport interruption restarts (a reset, a close before the stream's end); an invalid chunk never does.
- The gateway sends again after output only when a Session listens (`gotato.ReportModelRetry` returns true); a caller reading `Client.Stream` with no Session gets a retryable error. A Model of your own that retries inside its stream calls `gotato.ReportModelRetry` first and starts the reply over.

## Storage

### G-F03 — Stores ✅

Every Session is saved to a store as its Record (`session.Record`). How it works: [specs/record.md](specs/record.md). Why: decisions [002](plan/decisions/002-the-store-is-the-truth-and-memory-is-a-cache.md), [003](plan/decisions/003-gotato-owns-the-shapes-and-adapters-own-storage.md), [013](plan/decisions/013-a-conformance-suite-ships-with-the-store-contract.md), [017](plan/decisions/017-the-record-is-an-append-only-log.md), [028](plan/decisions/028-every-step-is-written-as-facts-and-a-log.md).

| Store | For |
|---|---|
| In memory | tests and short-lived programs |
| `session.NewFileStore(dir)` (creates `dir` and parents) | one process on local disk: a directory per Record and epoch (`<id>/<epoch>/<position>.json`), one file per batch named by its first position, linked into place (a link fails when the name exists, so two processes never write a position and a crash never tears a write) |
| Your own adapter | the database you run, through the contract below ([G-N08](#not-planned)) |

- A Record ID is at most `session.MaxIDBytes` (100) bytes of valid UTF-8, with no control character, `/`, `\`, `..` or leading `.`; its file name is at most `session.MaxNameBytes` (200); another ID is refused with `session.ErrInvalidID` (`invalid_argument`) by every store. `FileStore` spells uppercase and non-ASCII characters in the directory name.
- The Record is append-only: every step is appended as it happens at a position the store never gives twice; an Entry is a fact (a rebuild reads it; a metadata change is one) or a log entry; a compaction appends a Checkpoint and a load reads the facts from the last one; a Claim is a fact at the position after the head its instance read; an Entry of an unknown kind or a document from a newer `schema_version` is refused; a file the file store did not write is an error naming the path. The Record grows with every step: a team that wants to bound storage deletes entries before a Checkpoint itself.
- A crash can leave `.creating-*` or `.deleted-*` directories or `.tmp-*` files in an epoch directory or in `.instances`; `NewFileStore` (and a read of a Record's directory, for temporaries) removes those a day old or older.
- The epoch is in the file store's path, so a Record deleted and created again has another directory and an old writer gets `ErrNotFound`; the memory store checks and writes under one lock.

**The store contract** is eight calls, each one ordinary conditional write or read:

| Call | What it does |
|---|---|
| `Create` | writes a new Record starting with a Checkpoint; fails when the ID is taken (`ErrExists`). The RecordInfo names the Record's epoch (empty is `invalid_argument`, also in `Append`). Any other error leaves the outcome unknown, so a retry must tolerate `ErrExists`; `CreateWriter` and `OpenWriter` return it as `store_failure` |
| `Append` | writes a batch after a position, all or nothing, only when that position is still the last and the epoch is the Record's (a writer of a Record deleted and created again gets `ErrNotFound`) |
| `Load` | the facts from the last Checkpoint, the `segment_closed` Entries before it (`Loaded.Segments`), the last position, the RecordInfo |
| `Log` | the Entries after a position, facts and log entries, at most a limit (all when 0) |
| `List`, `Delete` | list Summaries, delete a Record; an append to a deleted Record fails |
| `Beat`, `Instance` | renew, read and remove one Liveness row per instance, timed by the store's clock |

`Beat` and `Instance` are the `session.Liveness` interface that `Store` embeds. `Store` gains no methods after 0.2.0 (a later capability is an optional interface found by type assertion). A caller does not modify what `Load`, `Log` and `List` return, and an adapter keeps every field of `RecordInfo`, `Entry` and `InstanceRow`, also later ones. A writer passes the RecordInfo with every write, so `List` never reads a log. `session.Writer` makes the writes, `session.Open` reads the facts a Run needs, `session.View` reads everything.

**Conformance suite.** `session/storetest.Run(t, backing)` (a package of its own, so `session` does not link `testing`) runs every check on a new backing store with several handles as several instances: create, load, list and a refused second create; appends that continue the log and a load from the last Checkpoint; a stale writer and a non-continuing append refused; a race of eight writers with one winner; a writer of a deleted Record refused with `ErrNotFound` when the Record was created again, at the same length or longer; a write with no epoch refused; every `RecordInfo`, `Entry` and `InstanceRow` field kept (by reflection); deletes, unknown Records, a canceled context; Liveness rows that expire by the store's clock and never come back, and a row its instance ended reading as not alive from every handle. Its own tests check it fails an adapter that moves a stale write onto the head, overwrites a position, compares only the length, keeps ended rows, or reads a removed row as alive.

## Many instances

### G-F27 — Service: many instances ✅

A Runner hosts Sessions inside your service on as many instances as you run; any instance answers a request as if it were the only one ([DESIGNS.md](DESIGNS.md#8-no-instance-is-special)). How it works: [specs/multi-instance.md](specs/multi-instance.md). Why: decisions [002](plan/decisions/002-the-store-is-the-truth-and-memory-is-a-cache.md), [019](plan/decisions/019-one-writer-per-session-claimed-in-the-store.md), [024](plan/decisions/024-gotato-builds-no-infrastructure.md), [036](plan/decisions/036-a-warm-copy-is-a-cache-not-a-lock.md), [037](plan/decisions/037-when-the-hash-missed-the-answer-names-the-writer.md).

| Item | What happens |
|---|---|
| Busy policy | a second Run on a busy Session is rejected with 409 (`reject`, default) or waits (`wait`) |
| Capacity | past `MaxActiveRuns` (off by default) a new Run is rejected with 429 |
| Save every step | the prompt, a model call before it is made, a streaming reply every second or 4 KiB, the reply, tools before they run, each tool result, each continuation; a crash loses at most the step in flight (G-G01) |
| One writer | the writer holds a Claim: a fact naming its instance and an Epoch, written at the position after the head it read, which also marks the Session busy until released. A writer that lost its Claim always writes to a taken position and is refused; a second instance stops before it calls the model (G-G08). A writer that already holds the Claim writes it again (`session_claimed`, same Epoch) when it opens the Session again, taking the next position, so of two instances opening an idle Session at once exactly one write lands and the other waits. A write that fails because its position is taken drops the writer's copy; any other failure leaves the outcome unknown, so the writer stops and the Session is opened again from the store |
| Liveness rows | one row per instance, renewed every third of its TTL, expiry set and compared by the store's clock; when it expires every Claim of that instance ends with no write; a row that expired never comes back; the row of an instance that died stays, expired. An instance that cannot show its row renewed within the TTL less a margin writes nothing, publishes `liveness_lapsed`, and refuses a Run or a new Session as not run; `Runner.Lapsed` says so and `/readyz` answers 503 with `"lapsed": true` |
| Instance name | `Config.Instance` is a label and the store identity is unique per start: a random suffix is appended (`pod-0` becomes `pod-0-3fa9c1d2`) in the row, every Claim and `session_claimed`; `Runner.Instance()` returns that form, so a StatefulSet name or a label two live processes share is safe (`service/instance_reuse_test.go`). Empty generates a whole name |
| Recovery | the first renewal after an outage longer than the TTL is refused with `session.ErrExpired`; the Runner takes a new name, writes a new row, removes the expired one, drops old Writers and warm copies (`session_unloaded`, `liveness_lapsed`); no restart |
| Close | `Runner.Close` removes the row so Sessions move at once; a closed Runner writes nothing more |
| Advertised address | optional `Config.Address` in the row; the only thing about another instance Gotato reads (`Store.Instance`); `not_writer` takes it from the row the Claim names; never connected to or checked |
| Any instance answers | the table below |
| Activation limit | `Admission.MaxActiveSessions` (`--max-sessions`, off by default) counts Sessions in use, each once. A Run that would activate one more, creating or waking, is refused before anything is loaded, claimed or created: `overloaded` (`service.ErrOverloaded`), HTTP 503 with `Gotato-Not-Run: true` and `"not_run": true`, a `run_rejected` Instance Event (`activation_limit`); a stream is refused the same way before it starts. Not refused: a Run on a Session active here (busy policy), create or fork, and the short changes under the lock (compact, tool activation: wait up to the Liveness wait, then `busy`; delete answers `not_writer` with the address and a Session whose writer is gone is deleted). A fork taken during a Run copies the Record as it is, answering unfinished calls "had not finished when the Session was forked". A Session deleted under a Run ends it `not_found`. `/readyz` does not fail at the limit; it reports `active_sessions` and `active_runs` |
| Warm Session and TTL | after a Run the Record and its Writer stay in memory with no Claim; the TTL is 60 idle minutes (`WarmPolicy.TTL`, `service.Agent.Warm`, `--warm-ttl`; negative keeps none); the clock is the Runner's, so no test waits. Before each Run the instance reads the Entries after the copy's head (`Writer.Stale`): unchanged, it resumes (`Writer.Resume`, a Claim at the next position only when the head is still the copy's) and sends no `session_loaded`; changed, deleted or created again, it drops the copy and loads from the last Checkpoint. Its own timer compacts it before the cache expires ([G-F06](#g-f06--compaction-)). Compact, tool activation and delete use the copy; delete drops it |
| `session_unloaded` | a copy leaves memory with why: `ttl`, `taken_over`, `deleted`, `drain`, `closed`, `failed` (a write of the copy failed), `liveness_lapsed`, `evicted` |
| Memory limit | warm Sessions count toward `MaxActiveSessions`: a Run that would exceed it evicts the longest-idle warm Session first (ties to the copy idle first; one with an admitted Run never; an idle compaction in progress is canceled first, and the evicted Session is not compacted), and is refused (`overloaded`, not run) only when every live Session is running. A Run on a warm Session adds nothing to the count. `Admission.MaxWarmSessions` bounds the warm copies |
| Not writer | a message for a Run on another instance is answered `not_writer` ([G-F26](#g-f26--steering-a-run-)) |

**A request that reaches an instance that is not the writer:**

| The Session is | The instance |
|---|---|
| idle, or its writer is gone | claims it, loads from the last Checkpoint, serves the request |
| in use by its live writer, also while that writer drains | waits for the release up to a bound (`Liveness.Wait`, whatever the busy policy), then claims it; past the bound 409 with `Retry-After` |
| in a Run on its live writer, and the request is a message for that Run | answers `not_writer`, not run, retryable, with the writer's address; does not wait |

A takeover leaves the old writer's warm copy stale; it finds that at its next Run and reloads, or drops it at its TTL.

**Where many instances differ from one:**

| Place | What differs |
|---|---|
| The writer dies mid-Run | the step in flight is lost, a running tool is answered with an unknown outcome, the stream ends, and the Session waits for the dead row to expire |
| Two requests reach two instances at once | whichever opens it first runs; the other waits up to `Liveness.Wait`, then 409 |
| The hash changes or a caller does not route by Session | a message for a running Run is answered `not_writer`; one with no Run going starts a new Run there |
| Several instances | `MaxActiveRuns` and `MaxActiveSessions` count per instance |
| A stream on an instance that is not the writer | stored Events arrive at its next look (`StreamPoll`, 1 second), no deltas |
| A rolling deploy | an instance on an older contract version refuses what it does not know |

**Drain** (SIGTERM, or `Runner.Drain` from your endpoint): mark draining in the row, `/readyz` 503, refuse new Runs and takeovers, every Run finishes, saves and releases; at the drain timeout the Runs still going are stopped, saved and released; each stopped Run's stream ends with its result and then a reconnect code (`RunResult.Reconnect` in Go); `Close` removes the row.

- Another instance still waits for a Session the draining instance uses, since taking it would fence a Run about to be saved. A refusal is `ErrDraining` (`draining`, not run): 503 with `Gotato-Not-Run: true`. Runs admitted before the drain still run, also one queued behind a busy Session; create and fork still work; a compaction is refused as `draining`; a message that finds no Run is refused as a new Run.
- `Runner.Draining` is true from the first moment, before the mark is written. After the deadline `Drain` waits for stopped Runs at most 15 seconds (a Run that ignores its context returns after 5, its last write bounded by 10); a Run not settled by then goes on in the background and is fenced by the next Claim. An idle warm copy holds no Claim; once Runs settled, every copy drops (`session_unloaded`, `drain`).

**Your load balancer** routes; Gotato builds no network between instances. It needs: a consistent hash on the `session-id` header (every request for a Session reaches its writer); one retry on another instance after a 503 marked "not run"; readiness from `/readyz`. `POST /v1/sessions` needs no header and answers with the new ID in `session-id`. The endpoints read `session-id` as a check: it must agree with the ID in the path or body (else 400); missing or blank agrees with everything. `Gotato-Not-Run: true` marks a request that never ran (any status, a 404 or 400 too); `Gotato-Retryable: true` marks one that may succeed later; a load balancer sends to another instance only when both are present, except `not_writer`, which goes to its `Gotato-Writer` address; a connection that broke after sending may have run and is not retried. The harness has such a balancer; examples for common balancers come later.

Settings, on `service.Config` and the HTTP handler:

| Setting | Default | Why |
|---|---|---|
| `MaxActiveRuns` | 0, no bound | the team sizes its instances |
| drain timeout (`Config.DrainTimeout`, `--drain-timeout`) | 10 s (`service.DefaultDrainTimeout`), negative none | most Runs finish in a rolling deploy |
| write bound (`session.WriteTimeout`) | 10 s | the Writer's writes and a Run's last write (save and release) outlive a canceled caller; a slower store fails the write as unknown |
| Liveness TTL (`Config.Liveness.TTL`) | 10 s | rides out a slow store; a dead instance's Sessions wait this long |
| Liveness wait (`Config.Liveness.Wait`) | 5 s | how long a request for a Session running elsewhere waits before 409 |
| warm Sessions (`Admission.MaxWarmSessions`) | 10000 (`service.DefaultMaxWarmSessions`), negative none | past it the longest-idle is evicted (`evicted`); a warm Session takes about 8 KB for ten Messages and 570 KB for a thousand |
| forwarding (`httpapi.WithForwarding`; HTTP only) | off | takes an `allow` function and forwards only to addresses it accepts; a nil `allow` forwards nowhere. An embedding service forwards with its own client |
| request body cap (`httpapi.WithMaxBody`) | 1 MiB | |
| stream keep-alive (`httpapi.WithKeepAlive`) | 15 s | |
| stream look (`Config.StreamPoll`) | 1 s; negative off | how often an idle Session stream reads the store for another instance's Events, one `Log` read per stream per look |

### G-F33 — Prompt cache retention ✅

The gateway asks a recognized provider for its longest cache retention: an hour on Claude (which doubles the price of a cache write; `5m` keeps the default), 30 minutes on OpenAI's Responses API from GPT-5.6. It is set where the provider is configured with `cache_retention` ([G-F07](#g-f07--models-and-providers-)), not per Agent. Compaction reads the retention in effect to time an idle compaction ([G-F06](#g-f06--compaction-)). Gotato does not refresh a warm Session's cache (a keepalive is in [Later](#later)).

## Endpoints

### G-F14 — HTTP and streaming ✅

A bot, a job or another service talks to Sessions like any other API. Mount the handler from `service/httpapi` on your own mux. How it works: [specs/events.md](specs/events.md), [specs/errors.md](specs/errors.md). Why: decisions [028](plan/decisions/028-every-step-is-written-as-facts-and-a-log.md), [030](plan/decisions/030-every-entry-is-an-event.md).

```text
GET    /healthz  /readyz  /v1/agents
POST   /v1/sessions      GET /v1/sessions      GET|DELETE /v1/sessions/{id}
POST   /v1/sessions/{id}/fork    GET /v1/sessions/{id}/events    GET /v1/sessions/{id}/context
GET    /v1/sessions/{id}/stream?after=N&record_epoch=E
POST   /v1/sessions/{id}/compact
POST   /v1/sessions/{id}/runs    POST /v1/sessions/{id}/runs/stream
POST   /v1/sessions/{id}/messages    POST /v1/sessions/{id}/notices    POST /v1/sessions/{id}/abort
```

- A run request carries `prompt`, or `continue: true`, and may set `timeout_ms` and an `agent` that binds the Session to that registered Agent (how a Session whose stored Agent was dropped is repaired; without it, 400 `invalid_argument` "unknown agent").
- A message request carries `kind` (`steer`, `follow_up`, `interrupt`) and `text`: 202 `{"session_id", "message_id", "queued": true}` when a Run takes it; with no Run going it starts one and holds until it settles, then 200 `{"session_id", "queued": false, "run": {…}}`; 409 `not_writer`, or forwarded with `httpapi.WithForwarding`. `…/abort` answers 202, 409 `invalid_state` when no Run is going, and forwards the same way. A notice request answers 202 `{"session_id", "number", "message_id"}` at once ([G-F26](#g-f26--steering-a-run-), [G-F29](#g-f29--runtime-notices-)).
- `GET /v1/agents` answers `{"agents": […], "max_effect": {name: cap}}`. `GET …/events` answers every stored Event as JSON Lines, filtered with `?kind=`. `GET /readyz` answers 200 with `active_runs` and `active_sessions`, 503 with `"draining": true` or `"lapsed": true`. `GET /healthz` reports the contract version, `"1"` at the first release.
- A Run that settles failed, canceled or past its deadline still answers 200 with the outcome in the body (a request refused as too long is such a Run: 200 with `context_overflow` in `result.error.code`). Only request-level problems use these statuses:

| Status | When |
|---|---|
| 400 | the request is invalid |
| 404 | the Session does not exist (`not_found`, marked not run) |
| 408 | the caller canceled the request |
| 409 | busy; the Run is not active; a stream cannot resume (`cannot_resume`, with the current `record_epoch`); or a message or abort for a Run another instance writes (`not_writer`, not run, `Gotato-Writer` header and `writer` in the body) |
| 413 | the body is too large (`body_too_large`) |
| 429 | the Runner is at capacity (marked not run) |
| 500 | a settled Run could not be saved |
| 502 | a forward to the writer failed, so the message may have reached it (`forward_failed`, not marked) |
| 503 | draining (`draining`) or past the activation limit (`overloaded`), both marked not run; or the store failed |
| 504 | the request's deadline passed |

Every code has its status in one table (`statusOfCode`). `Gotato-Not-Run: true` is set whenever `gotato.NotRun(err)`, `Gotato-Retryable: true` whenever `gotato.Retryable(err)`; the error body carries `code`, `message`, `not_run` and `retryable`, and no field repeating the status. The marks are facts, not a retry policy: retry elsewhere only when both, except `not_writer`. [RUNBOOK.md](RUNBOOK.md) holds the table of every code with status and exit code; a test fails when it drifts.

**Streaming.** `/runs/stream` and `/stream` send each Event as server-sent events (`event: <kind>`, `data:` one flat JSON object) and end a Run with `event: result`. The handler reads a Subscription, so a slow client never slows the Run.

- A run stream answers 200 once the Run is starting (admitted, holding its Session, Agent known); a bad body or a request refused before that (503 draining or activation limit, 429, 409 busy here or elsewhere, 404, 400 unknown Agent) gets its status with the run route's headers. Only a failure after the Run started arrives as `event: error` or in the result.
- A Run the drain stopped ends with `event: result` then `event: reconnect` (`retry:` and `retry_after_ms`); a result with no `run_id` means nothing ran, so send it again.
- **Stream resume.** `GET /v1/sessions/{id}/stream` sends the stored Events after a number, from the Record, then the live ones across Runs, on any instance. Every stored Event has its number as the SSE `id:` (its Entry's position); a delta has none and is not sent again after a reconnect. `Last-Event-ID` (a browser's `EventSource` sends it) or `after` (wins) says where to start; 0 or neither starts at the first.
- **Record epoch.** An identity chosen when the Record is created and changed only if it is deleted and created again; it is the `record_epoch` of `session_created`, every Session summary, a Run's result, and the first frame `event: stream` `{session_id, record_epoch, after, head}` (also the `Record-Epoch` header). A client may send it as `record_epoch` or `Record-Epoch`; if it is not the Record's, or `after` is past the last Event, 409 `cannot_resume` with the current epoch comes before the stream starts and the client starts over at 0. An unknown Session is 404. A Run's stream carries the same ids, so a lost one resumes through the Session's stream. A store adapter keeps `RecordInfo.RecordEpoch` (the contract test checks).
- **Ends.** A drain ends a Session's stream after the stored Events with `event: reconnect` `{"retry_after_ms", "why": "draining"}` and `retry:`, a wait spread from 0.5 to 1.5 seconds; the first frame sets a different `retry:` for each client. Any other end is `event: error` with no reconnect. An idle stream sends a comment every 15 seconds. A stream follows the Record it opened and never delivers another Record's Event: if the Session is deleted or created again it ends with `event: error` (404 `not_found` marked not run; `cannot_resume`). On the instance that deletes or recreates, opens or creates the new Record, the old stream ends at once and one that read the old epoch is refused before it starts; on any other instance it ends at the first look that finds the Session gone or reads Events of the new Record (a new Record no longer than what the stream has seen is noticed once it grows past that). Each Record has a Feed of its own named by Session ID and epoch.
- **Which instance.** The writer's stream receives Events as written; on any other instance they arrive from the Record at the next look (`Config.StreamPoll`, 1 second): one `Log` read per idle stream per look, `Load` only after a look that returned Events.

**Go API.** Call the Runner directly and subscribe: `Runner.StreamRun` for one Run, `Runner.StreamSession` from any number, `Runner.SubscribeInstance` ([G-F13](#g-f13--events-)). Authentication, logging and access control are yours: wrap the handler ([G-N22](#g-n22--access-control-belongs-to-applications)).

## Watching and extending

### G-F13 — Events ✅

Every step of a Session is an Event, so you can watch, log or measure it; a stored Event is an Entry of the Record, and a Session never waits for a reader. How it works: [specs/events.md](specs/events.md). Why: decisions [028](plan/decisions/028-every-step-is-written-as-facts-and-a-log.md), [030](plan/decisions/030-every-entry-is-an-event.md).

- **The Record is the Event log.** A stored Event's number is its Entry's position, counting the whole Record, so every instance reads the same number. It reaches subscribers only after its write; most ride their step's write, the few between steps (a retry, an Abort request, a queued message) get their own. Deltas are sent at once, numbered with the Event they follow. A Session without a store still numbers and sends.
- **A Go type for each kind**, each embedding a `gotato.Header` (time, Session ID, and number, Run and Turn where it has them; an Instance Event has no number). In JSON an Event is one flat object whose `kind` names its type (`gotato.MarshalEvent`, `gotato.UnmarshalEvent`); a kind a later version adds comes back as `gotato.UnknownEvent`.
- Three places: the **Record** (a subscriber can read it again), **sent only** (deltas), and the **Instance** (an Event from an Instance that holds no Claim). A duration is a number of milliseconds in a key ending `_ms` (`elapsed_ms`, `wait_ms`); a Tool is named by `tool_id` (its qualified ID); the tools the model has loaded are `loaded_tools`.

| Event | When | Kept |
|---|---|---|
| `run_started`, `run_ended` | a Run starts (with the caller's `request_id`), and settles with status, cause and stop reason | Record |
| `turn_started`, `turn_ended`, `context_built` | a Turn starts, ends with its usage, and has its Context built (prefix hash and sizes) | Record |
| `reply_started`, `reply_ended` | the reply starts, and ends with the Message, the model that answered and why it stopped; a call that fails or is refused before any output has no `reply_ended` | Record |
| `text_delta`, `reasoning_delta`, `tool_progress` | a piece of the reply's text or reasoning; a Tool reports its own progress | sent only |
| `reply_part_kept`, `reply_cut_off`, `model_retried` | the text and reasoning streamed since the last part, kept every second or few KiB so a crash loses at most the last part; the stream broke and the text so far is kept as a Cut-off reply; the gateway calls again, with attempt, wait and why ([G-F31](#g-f31--recovering-a-cut-stream-)) | Record |
| `tool_started`, `tool_ended` | a Tool starts, and ends with status and duration; a call blocked before it ran has a `tool_ended` and no `tool_started`; `tool_ended` rides the write that commits its group's results, so in a parallel group it can wait behind the slowest sibling (its `elapsed_ms` and time are its own) | Record |
| `tool_result_committed`, `tool_result_spilled` | a Tool Result enters the Transcript, in the order the model asked; one over a page is stored whole, subscribers get it without the body ([G-F35](#g-f35--large-tool-results-)) | Record |
| `tool_loaded`, `tool_unloaded` | with `tool_id` and `why` (`names`, `query`, `call`, `compaction`, `gone`; [G-F34](#g-f34--tool-search-)) | Record |
| `run_requested`, `run_waiting`, `run_rejected` | a request for a new Run arrives, waits for a busy Session, or is refused with why: busy, at capacity, draining, `activation_limit`, or `not_writer` for a message | Instance |
| `message_queued`, `message_committed` | a Steer, FollowUp or Interrupt waits for its point; a user Message enters the Transcript with its source (prompt, Steer, FollowUp, Interrupt) | Record |
| `message_dropped`, `message_rejected` | a waiting message (source `notice` for a notice) is dropped with why (`run_ended`, `draining`, `writer_failed`, `run_failed`, `queue_full`, `session_closed`, `writer_gone`), or refused because the queue is full (`queue_full`) or it is over `MaxMessageBytes` (`too_large`) | Record |
| `notice_queued`, `notice_committed` | a notice waits (`notice_kind`, `source`, `text`), and enters the Transcript right before a model call (`message`, and `queued`, absent for Gotato's own, committed at once) | Record |
| `abort_requested` | an Abort arrives; `run_ended` follows | Record |
| `session_claimed`, `session_released` | an Instance claims the Session with why (new, idle, writer gone, waited) and the previous holder, re-opening a held Session writes it again with the same Epoch; the Writer lets it go | Record |
| `session_fenced`, `liveness_lapsed` | a write is refused because another Instance holds the Claim; this Instance's row was not renewed in time | Instance |
| `session_created`, `metadata_changed` | a Session is created, with `record_epoch` (a fork names `forked_from`: Session and position), the first Entry of every Record; a setting in the metadata changes | Record |
| `compaction_started`, `compaction_ended`, `compaction_failed`, `segment_closed` | a compaction starts, appends its Checkpoint, or fails, with why: `on_demand`, `past_ceiling`, `before_cache_expires`, `before_unload`, `overflow`, `past_budget` (a merge has the `why` of the compaction it follows); a Segment is closed, written once before the Checkpoint that lists it ([G-F06](#g-f06--compaction-)) | Record |
| `session_unloaded`, `session_deleted` | the Session leaves this Instance's memory with why ([G-F27](#g-f27--service-many-instances-)); a live Session in the Go API sends `close` when it closes; the Record is deleted | Instance |
| `session_loaded`, `session_saved`, `store_write_failed`, `events_skipped` | how long the store took to load the Session and to write a Run's steps (a Run on a warm copy sends no `session_loaded`, and its `session_saved` counts its own writes, the Claim included); a write failed for another reason than a taken position; an Instance's stream fell behind and skipped Events, with how many | Instance |
| `extension_failed` | an advisory extension failed, or a ToolSet did not resolve (`stage: "toolset:<name>"`), and the Run went on | Record |

**Facts and log entries.** A rebuild reads these facts: `session_created`, `compaction_ended` (the two Checkpoints), `tool_loaded`, `tool_unloaded`, `segment_closed`, `message_committed`, `reply_ended`, `reply_cut_off`, `tool_result_committed`, `tool_started`, `metadata_changed`, `session_claimed`. Every other stored Event is a log entry. A load starts at the last Checkpoint and reads only the facts after it, except when the last Run never ended: it also reads the log since, rebuilds the Cut-off reply from the `reply_part_kept` Entries, and the Instance that takes over stores it as `reply_cut_off`.

**Subscriptions.** A Session puts each Event in its subscribers' queues and never waits; each subscriber has one goroutine that forwards in order.

| Subscription | For | Carries |
|---|---|---|
| A Run's stream (`Session.SubscribeRun`, `Runner.StreamRun`, `/runs/stream`) | the caller of a Run | the Session's stream from the Run's first Event (the claim or creation it caused) to its last, with the same numbers |
| A Session's stream (`Session.Subscribe(ctx, gotato.SubscribeAfter(n))`, `Runner.StreamSession`, `GET …/stream`) | an application that shows a conversation; a client that reconnects | every Event across Runs after any number: first from the Record, then live; checks the epoch and number first and follows the Record when another instance writes |
| An Instance's stream (`Runner.SubscribeInstance`, optionally `gotato.SubscribeFilter(func(e gotato.Event) bool { return e.Head().SessionID == gotato.SessionID(id) })` to follow one Session; `service.ForAgents("name")` keeps some Agents) | the service's telemetry | every Event of every Session on the Instance plus Instance Events; no single number, so no replay; a filter runs on the publisher's goroutine and must be fast |

- A subscriber that falls too far behind catches up from the Record by number and rejoins live, so it never misses a stored Event; only deltas can be skipped. An Instance's stream skips Events and says how many in `events_skipped`; each stored Event names its Session and number, so the subscriber can read what it missed. A stream whose source cannot read Events again (a Session over a Record that is not stored, past its window) ends with `gotato.ErrEventsLost` (`cannot_resume`). A Feed over no stored Record keeps a ring of the last 4,096 Events; a Session over a Record keeps a window of the last 1,000.
- Removed in the Events change (see [CHANGELOG.md](CHANGELOG.md)): the map of fields in each Event, the per-Run sequence number, the `events` Log entry with `session.Recorder`, `session_compacted` (now `compaction_ended`), `EventObserver` and `SubscribeLifecycle` (now `session_created` and `session_unloaded`), and the names `run_start`, `message_update`, `tool_execution_end` (now `run_started`, `text_delta`, `tool_ended`).
- A Run not saved (`SkipSave`) has only its Run's stream, numbered from 1, not Record positions. An Instance Event is not in a Run's stream. `Runner.StreamRun` runs the Run on its own goroutine and calls its sink from the caller's.
- **Following one Session** (a chat bot): read the head before the message or Run is sent, so nothing after it is missed, then follow `Runner.StreamSession`:

```go
record, _ := runner.Record(ctx, id)
sub, _ := runner.StreamSession(ctx, id, service.StreamFrom{After: record.Head()}) // only Events after now
defer sub.Close()
for { // io.EOF at a drain: reconnect with the last number seen
	event, err := sub.Next(ctx)
	if err != nil { return err }
	switch e := event.(type) {
	case gotato.ReplyEnded:
		if e.ToolCalls == 0 { reply = gotato.TextOf(e.Message) } // a reply that asks for no tool is the answer; the last one wins
	case gotato.RunEnded:
		return deliver(reply, e.Status)
	}
}
```

A caller of `Run` needs none of this (`RunResponse.FinalText`). A message left over by a Run that was settling is answered by a following Run with its own `run_ended`; read on to the Run whose `message_committed` names the message's ID.

### G-F15 — Extensions ✅

Extensions add behavior at fixed points of the Loop, without forking it: `gotato.WithExtension`. How it works: [specs/session.md](specs/session.md).

| Stage | Runs |
|---|---|
| `TurnPreparer` | before every model call, with no Tool call open; may rewrite the Transcript. `PrepareTurn(ctx, PendingTurn) (TurnPrepared, error)`: called before a Run's first call, before each call after a Tool batch, and once more when the Model refused a call as too long before any output (`PendingTurn.Overflow`). `PendingTurn` carries the Session and Run IDs, the Turn, the Transcript, `RunStart`, the request about to be sent, `Refusal` and `Flush`. `TurnPrepared{Changed: true}` makes the Loop build the request again and switch off ToolSets the Transcript no longer lists. An error ends the Run (a Gotato error keeps its code, any other is `extension_failure`); a refusal no preparer mended ends it `context_overflow`, and a second in the Turn is never retried. `AutoCompactor` is one |
| `ContextTransformer` | after the Context is built, before it is sent |
| `MessageConverter` | when Messages are converted for the model |
| `PreToolUse` | before a tool runs; may refuse the call |
| `PostToolUse` | after a tool runs |
| `TurnStopper` | at the end of a Turn; may stop the Run |

Each stage runs on the Run's goroutine, so a slow extension slows the Run. An extension marked advisory may fail without failing the Run, stored as `extension_failed` with the stage. There is no observer stage: a service reads Events from a Subscription.

## Testing

### G-F22 — Test kit and measurement ✅

You test agents without a network or a paid model, and measure Gotato on a fixed workload.

- `testkit`: `FakeModel` (scripted), `ReplayModel` (recorded replies), `FuncModel` (answers from the request), a fake tool, an Event recorder, a ready-made Session, and the `echo` and `demo` models.
- `internal/cmd/gotato`, the CLI, drives a Runner by hand and in end-to-end tests that assert on JSON; our test tool, a thin client with no runtime logic and no contract ([G-N28](#not-planned)); see `internal/cmd/gotato/README.md`.
- **Reference workload.** `reftasks.ReferenceTasks` (`internal/reftasks`; our test tool, not public) returns the tasks [GOALS.md](plan/GOALS.md) measures, each with a fake model and a check: `lookup`, `conversation` (ten prompts, three lookups, a last question only earlier Turns answer), `tool_error` (a refused call, a failing lookup, a recovery), `support_case` (an agentic loop in one Run), `history_10`, `history_100`, `history_1000` (a lookup in a Session of that many messages), `long_session` and `long_session_recap` (100 Runs of 20 Turns, each reading a 20 KB log, against a 128K-token fake window, G-G10, G-B14, G-B16; 2,000 Turns, played only when named: `-tasks long_session`), and `tool_search`.
- `internal/workload` plays tasks through a Runner and reports: plays passed; every failed Run, tool call and check with its cause (model, provider, tool, store, limit, extension, abort or Gotato); token usage, cost at your price, cache hit rate; Gotato's time per Turn next to the model's and the store's load and save time; the time added before the first request; whether each Turn's Context matches an inspection; for a task with a window, request sizes against it (requests refused for being over it, the largest, sizes at Turns 1, 10, 100, 1000, 2000, compactions, merges, the Recap's spend). All numbers come from the Run's Events. `go test ./internal/workload` runs it in CI; `BenchmarkReferenceTasks` measures it.
- **Live run.** `go run ./internal/cmd/gotato-workload -live -gateway-config gateway.yaml -price 3,15,0.3,3.75` (US dollars per million tokens: input, output, cache read, cache write); `-tasks`, `-plays`, `-concurrency`, `-store DIR` shape any run; exit 3 when Gotato or the store caused a failure or a Context differed from its inspection. It calls a paid model, so on demand, never in CI. `-recap-check` runs the payments check of G-E01 (`reftasks.PaymentsRecapCheck`, `workload.RunRecapCheck`): three prompts, five questions answerable only from Tool Results, a compaction with the model's Recap, the same questions again (unsaved, tools off); exits 3 when no Recap passed the rules or the share after is below `-min-share`; without `-live` it plays a fake model.
- **Reference service.** `bench/refservice` is the smallest service around Gotato (agent, Runner, file store, HTTP handler, gateway model; a Go module of its own): `(cd bench/refservice && go test -run '^$' -bench Startup -benchtime 20x .)` times start to the first 200 from `/healthz`; `bench/refservice/footprint.sh` prints modules and stripped binary size as JSON.

**Fault injection** (all in `internal/workload` and the service tests): each reference task is played with an instance dying after its n-th write, for every n (244 points); another instance opens exactly what that write left, every tool call answered, a Context whose prefix hash matches a clean play, and the next Run completes. A client leaving mid-stream still gets its Run settled `canceled` and saved. Three Runners over one memory store and over one file store take concurrent prompts at random instances, one killed: each completed Run's prompt lands once followed by its reply, and no instance calls the model for a Session it does not hold. A stream cut after every Event of every task and resumed by `Last-Event-ID` or `after` with the epoch equals an uncut one (no gap, no repeat, no resumed delta before the cut). Three instances behind a proxy hashing on `session-id`, one variant with an instance killed, give the Transcript and Events one instance gives, a non-writer's stream included. Messages to one Session land once each and are never interleaved; a killed instance loses only the step in flight, and the proxy does not retry a request that broke after it was sent. A full instance refuses as not run, and the proxy retries once elsewhere.

## Later

Ideas worth building once they are needed. None has a milestone yet.

| Idea | Note |
|---|---|
| Telemetry adapters | a package mapping an Instance's Events onto OpenTelemetry; teams can write their own ([G-N11](#not-planned)) |
| Model catalog | model names and prices next to the providers; a model's window comes from `context_window_tokens`, never looked up |
| Provider rate-limit information | limits, remaining calls and reset time reported with usage |
| Idempotency keys | a repeated request ID is not run twice (a `RequestID` is recorded and echoed already, and nothing looks for a repeat) |
| Examples (G-F24) | one-shot use, a persistent Session, compaction, forks, dynamic tools |
| Token budget per Run | a cap on tokens one Run spends; a `TurnStopper` does it today |
| Paging and filtering the Session list | for services with many Sessions |
| Loading only the recent Transcript | a load already starts from the last Checkpoint; loading less waits until needed |
| Prompt cache keepalive (G-F33) | refresh a warm Session's cache before it expires; each refresh pays a cache read of the whole prefix, so only if measurements show users returning inside the retention often enough |
| Changes that keep the cache (G-F32) | a changed instruction, or a tool switched on for one Session mid-Session, appended after the Transcript (a notice, the tool's definition) instead of rewritten into the prefix; a compaction folds it back. Build it when a service really does this and the cache cost shows; tools the model loads itself are already covered ([G-F34](#g-f34--tool-search-)) |
| Stopping a group of Runs across Instances | abort every Run whose Session has some Metadata, on all Instances. Today: list and abort each; a Run elsewhere answers `not_writer`. It must not become a group, role or supervisor in the runtime ([G-N01](#not-planned)) |

## Not planned

What Gotato will not build, and why. Look here before proposing a feature; each belongs to the application, an MCP server, or the tools themselves.

| ID | Not built | Why |
|---|---|---|
| G-N01 | agent organization: roles, teams, hierarchies | Gotato provides peer agents; applications compose roles |
| G-N02 | task graphs, scheduling, orchestration, pipelines, project planning | they live in the application |
| G-N03 | global resource scheduling: concurrency, CPU and memory, quotas, provider quotas | Gotato provides cancelable, lightweight Runs; the Runner only bounds concurrent Runs and keeps one per Session |
| G-N04 | product UI | Gotato has no human-facing surface; the CLI is a test tool |
| G-N05 | a required service | library use stands on its own; the service is the Runner behind `service/httpapi`, and core packages never depend on it |
| G-N06 | memory architecture | applications choose long-term memory |
| G-N07 | a workflow language | ordinary Go is the default; a workflow package, if one exists, leaves the Loop unchanged |
| G-N08 | storage engines | Gotato defines the store contract and ships memory and file stores; an application backs it with its own database through an adapter that owns schema and migrations |
| G-N09 | tool catalogs | filesystem, Git, HTTP tools and skill-format adapters (SKILL.md) come from MCP servers or the application |
| G-N10 | context organization beyond full history | retrieval, windowing and summarization pipelines are your own `ContextBuilder` |
| G-N11 | telemetry backends | Events have a stable shape; mapping them is a subscriber in the application |
| G-N12 | tool approval | tools are written by the team and allowed by name, the Effect cap hides what is above it, a risky tool refuses its own arguments, a `PreToolUse` extension adds a check |
| G-N13 | a cloud coding agent | coding stays on developers' machines ([PHILOSOPHY.md, question 2](PHILOSOPHY.md#2-what-belongs-in-a-cloud-session)) |
| G-N14 | a remote dev environment, shell or sandbox | work that needs a machine runs on a laptop, in CI or in a coding product; reading a large result is paging ([G-F35](#g-f35--large-tool-results-); [Decision 041](plan/decisions/041-a-read-only-bash-for-large-results-is-another-project.md)) |
| G-N15 | a general-purpose assistant | Gotato is for purpose-built agents ([PHILOSOPHY.md, question 3](PHILOSOPHY.md#3-how-is-a-cloud-session-different-from-a-local-one)) |
| G-N16 | a separate platform | a platform beside your services is a second system to run |
| G-N17 | infrastructure | discovery, routing, autoscaling and deployment already exist where you run |
| G-N18 | ready-made clients | bots, schedulers and front ends are the callers' code |
| G-N19 | structured output | a tool of the Agent's own (below) |
| G-N20 | MCP features beyond tools | below |
| G-N21 | a last Turn that answers | the deadline is the caller's `ctx`, so the caller sends a notice before it (`Runner.Notice` or a Steer: `{Kind: "deadline_near", Text: "The time for this task is almost over. Answer now with what you have."}`, e.g. ten seconds ahead via `time.AfterFunc`); Gotato does not guess the lead or force `tool_choice: none` |
| G-N22 | access control | below |
| G-N23 | withdrawing a waiting message | the caller holds the message ID and can send the next message or a Stop; a waiting message already ends as committed, dropped or refused; a `message_withdrawn` Event would never be sent |
| G-N24 | notices for a settings change (`settings_changed`) and an interrupted Run (`run_interrupted`) | an interrupted Run is ended in the Record as `run_ended` `failed` with cause `interrupted` and its started calls are answered with an unknown-outcome result the model reads; the instruction is built from the current Agent on every request (keeping the cache across a change is in [Later](#later)); the names are free for a service, as `Session.Notice` takes any kind but `continue` and `time_away` |
| G-N25 | a native form for a notice | below |
| G-N26 | a load signal in the Liveness row | the row stays an address and a draining mark; `/readyz` reports `active_sessions` and `active_runs` (also `Runner.ActiveSessions`, `Runner.ActiveRuns`); routing is the load balancer's, an instance at its limit refuses as not run |
| G-N27 | a gRPC adapter | HTTP is the one wire transport; every operation would repeat HTTP's; a service that wants gRPC maps `service.Runner` itself, as `service/httpapi` shows |
| G-N28 | a shipped CLI | it would be a third contract needing versions; `internal/cmd/gotato` stays as a test tool that promises nothing; a service writes its own `main` |

The feature test says no to G-N23, G-N26, G-N27 and G-N28: a Go service embedding an agent has not asked for them; a request should name the case.

### G-N19 — Structured output is a tool of the Agent's own

Give the Agent a tool whose arguments are the shape, such as `submit_ticket`, and say in its instruction to call it with the result. Gotato checks the arguments against the schema before the tool runs ([G-F09](#g-f09--tools-and-their-effect-)); arguments that do not fit go back as a failed Tool Result and the model corrects them. The tool receives the value decoded and keeps it where the service keeps the Run's result; a tool that implements `Tool` itself also reads the Run's ID from its `ToolUse`. The model usually answers once more after the call, so an instruction such as "after submitting, reply only 'Done'" keeps it short. `gotato.NewFuncTool` with a Go struct argument (`json`, `description` and `enum` tags) is the whole tool.

### G-N20 — MCP features Gotato does not build

The MCP adapter ([G-F12](#g-f12--the-mcp-adapter-)) maps a server's tools onto Tools and stops there.

| Not built | Why |
|---|---|
| Code mode | each step of a Run would be a program that cannot be replayed, and a tool that runs code is a bound nobody can read ([Decision 026](plan/decisions/026-gotato-ships-no-machine-no-client-and-no-tool-catalog.md)) |
| MCP resources | a Session reads data through tools the team chooses; a resource has no Effect and no schema check |
| Prompts | the instruction is the Agent's definition, where a reviewer reads it |
| Sampling | a Run's cost and bounds would depend on a server the team may not control |
| Elicitation | a person's answer needs a waiting state in the Loop, which Gotato does not have ([Decision 005](plan/decisions/005-no-approval-system.md)) |
| Gotato as an MCP server | callers reach Sessions through the HTTP handler and the Go API |
| OAuth | every service already authenticates its callers and holds its credentials ([Decision 025](plan/decisions/025-callers-credentials-and-quotas-belong-to-the-service.md)); a server's headers come from the application |

### G-N22 — Access control belongs to applications

Who the caller is, and who may touch which Session, are the application's. Gotato builds no identity type, "not allowed" error or hook. It passes the request's `context.Context`, with all its values, to every store and extension call for that request ([Decision 025](plan/decisions/025-callers-credentials-and-quotas-belong-to-the-service.md)).

- **A refusal is `not_found`:** a store wrapper returns `session.ErrNotFound` for a Session the caller does not own (404, and it does not reveal the Session exists).
- **Every request-driven call reads through the store:** a read by `Log`, a Run by `Load` and `Append`; `Runner.AbortSession`, `Send` and `Notice` read the Record with the request's `ctx` before acting on a live Run. A wrapper guards `Load`, `Log`, `Append`, `Delete` and filters `List`; another caller's cancel, steer, follow-up, interrupt and notice answer `not_found` too.
- **Work Gotato starts on its own has no caller:** an idle compaction and a Liveness renewal carry none; a follow-on Run keeps the values of the request that started the Run before it, without its cancellation. What an empty caller means is the team's rule.

```go
type ownerStore struct{ session.Store } // Load reads loaded.Info.Metadata["owner"] and compares it with ctx.Value(callerKey{});
// a mismatch returns session.ErrNotFound; Log, Append and Delete check the same way.
```

[service/httpapi/caller_ctx_test.go](service/httpapi/caller_ctx_test.go) and [caller_live_test.go](service/httpapi/caller_live_test.go) keep this behavior, also while the owner's Run is live.

### G-N25 — A notice has no native form

A notice could go in a provider's own form (a `system` message mid-conversation on Anthropic Messages, a `developer` message on Responses). Gotato does not build it: a notice is tagged text, `<notice kind="…">…</notice>`, in a user turn on every wire API. One form needs no model list that changes with each release; the user turn follows the Tool Results or the next prompt, so the cached prefix and the reasoning in flight stay; a fixed sentence in the system part already says tagged text is the system's; and Anthropic documents the mid-conversation system message while OpenAI does not document a developer message there, so a native form would rest on documentation alone. A team that wants one writes its own Model, which receives the notice as a Message of role `notice`.
