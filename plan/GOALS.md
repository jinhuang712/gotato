# Gotato — Goals

GOALS.md turns the wants in [PROPOSAL.md](../PROPOSAL.md) into checks, and keeps the last result of each with its date. Every goal serves a want of the proposal and a principle of [DESIGNS.md](../DESIGNS.md); ⭐ marks a core goal, one that serves a guarantee of the design. A goal stands all the time, and its check runs at the end of a checkpoint or before a release, never for a small change. [ROADMAP.md](ROADMAP.md) says when each goal first runs, and [TODO.md](TODO.md) holds what is open about a check.

An ID never changes. Its letters come from an older scheme and do not name the kind: G-G is a goal that passes or fails, G-B a number, G-E a result a real model judges, and G-R a constraint. The kind of a goal is the heading it sits under.

## How the goals are checked

Every number is measured on this repository's own workloads.

| Workload | What it is |
|---|---|
| Reference workload | eight scripted tasks with fake models, run in CI: `lookup`, `conversation` (10 Runs), `tool_error`, `support_case` (5 Turns of the model's own choosing), `tool_search` (tools that load and unload), and `history_10`, `history_100` and `history_1000` (a lookup in a Session of that many messages). They come from `reftasks.ReferenceTasks` and are played through a Runner by `internal/workload` |
| Long session | `long_session` and `long_session_recap`: 100 Runs of 20 Turns on one Session, each Turn reading a 20 KB log, against a fake model with a 128K-token window. It plays only when named, with `go run ./internal/cmd/gotato-workload -tasks long_session` |
| Live run | the reference tasks against a real provider with a fixed model and its price, on demand with `go run ./internal/cmd/gotato-workload -live`, because CI has no provider credentials |
| Fault injection | a store that fails or an instance that dies at a chosen write, and several instances over one store with random kills, in the tests of `internal/workload`, `host` and `adapter/http` |
| Reference service | `bench/refservice`, the smallest service a team would write around Gotato; it gives startup, footprint and resident memory |

- A benchmark is tracked against the previous release, and its 0.2.0 value is the reference.
- Every number about a Run can also be computed from Events and `Usage` alone ([DESIGNS.md → 9](../DESIGNS.md#9-a-session-can-be-stopped-and-seen)), so a team gets the same numbers from its own telemetry.
- All numbers come from an Apple M3 with 8 cores and Go 1.27.2; those dated 2026-11-03 were taken while other work ran on it (load average about 5), so their times are noisy.
- The numbers dated 2026-10-16 and 2026-10-17 come from the live run, which played every task five times (`support_case` ten) against `gpt-6.1-sol`, with the model bridge sending each Session's cache key.
- The live run's cost uses the list price of $2 input, $10 output and $0.10 cached input per million tokens.

## Summary

```
Goals
├── Objectives
│   ├── ✅ G-G01: Session reconstructable ⭐ — a crash at any step loses nothing but the step in flight
│   ├── ✅ G-G09: The same Record gives the same request ⭐ — two instances build identical requests
│   ├── ✅ G-G07: No unplanned change to the prefix ⭐ — a request starts with the bytes of the one before
│   ├── ✅ G-G10: A long Session stays within its window ⭐ — 2,000 Turns, 0 requests over the window
│   ├── ✅ G-G11: A failure leaves the Session usable ⭐ — the next Run succeeds after each injected fault
│   ├── ✅ G-G08: One owner across instances — no lost, duplicated or reordered Turn
│   ├── ✅ G-G02: No silent stream cutoffs — every stream ends with a final event
│   ├── ✅ G-G04: No failures caused by Gotato — 0 Runs and 0 tool calls
│   ├── ✅ G-G03: Context fidelity — inspection shows the request a Turn sends
│   ├── ✅ G-G05: Effect cap holds — no tool above the cap reaches the model
│   └── ⬜ G-G06: Breaking changes are written down — each has a before and after entry
├── Measures
│   ├── ✅ G-B14: Context size and cost per Turn by the Session's age ⭐ — size measured, cost not
│   ├── ⬜ G-B15: The first request after an idle return ⭐ — not measured
│   ├── ✅ G-B16: Cost of a compaction — tokens measured, time and cost not
│   └── ⬜ G-B13: Time to a first agent — not measured
├── Benchmarks
│   ├── ✅ G-B01: Cache hit rate ⭐ — 0.93 to 0.99 on long Sessions
│   ├── ✅ G-B02: Token cost per task — $0.0008 to $0.030 a play
│   ├── ✅ G-B03: Tool-call success rate, split by cause — 80 of 85
│   ├── ✅ G-B04: Error rate, split by cause — 0 of 85 Runs
│   ├── ✅ G-B05: Gotato's time per Turn — 0.15 ms for a lookup
│   ├── ✅ G-B06: Time added before the first token — 0.1 to 0.5 ms
│   ├── ✅ G-B07: Cold-load time — 1.6 ms at 1,000 messages
│   ├── ✅ G-B08: Memory per task — 12 MB resident when idle
│   ├── ✅ G-B09: CPU per Turn at 10, 100 and 1,000 messages — 0.13, 0.46 and 3.4 ms
│   ├── ✅ G-B10: Startup time — 30 ms
│   ├── ✅ G-B11: Concurrent Runs per instance — between 4 and 8
│   └── ⬜ G-E01: The Recap keeps the facts ⭐ — not measured
├── Comparisons
│   └── ✅ G-B12: Footprint — 9.0 MB against 14.8 MB
└── Constraints
    ├── ✅ G-R01: Each package imports only the layers below it
    └── ✅ G-R02: A Session runs in a small main with no server or database
```

## Goals

### Objectives

#### G-G01 — Session reconstructable ⭐

After a crash, another instance rebuilds what the dead one had written.

**Want:** [No lost conversations](../PROPOSAL.md#it-runs-on-many-pods), under [The store is the only truth](../DESIGNS.md#1-the-store-is-the-only-truth).

**Acceptance:** each reference task is played with the instance dying after its n-th write, for every n (`TestCrashAtEveryWriteRebuildsTheLastStep`). In 100% of crash points the Session another instance loads equals the Session at its last written step, every tool call is answered, the Context rebuilt from it has the prefix hash of a clean play, and the next Run completes. Each write adds at most one stored Message to the one before.

**Status:** ✅ 2026-11-03. 244 of 244 crash points over the eight tasks.

#### G-G09 — The same Record gives the same request ⭐

**Want:** [Any pod answers like the only one](../PROPOSAL.md#it-runs-on-many-pods), under [The store is the only truth](../DESIGNS.md#1-the-store-is-the-only-truth).

**Acceptance:** each reference task is played as several Runs on one Session, moving between instances and reloads (`TestSameRecordSameRequest`). For every request the model got, the Record as it stood at that Turn is copied to a new Session and the request is built again on instance a, on instance b and on a new Runner of instance a. The JSON of the whole `ModelRequest` (instruction, Messages, tools, options, cache breakpoints and cache key) is the same in 100% of Turns. The check is on the `ModelRequest`, not on a provider's wire bytes. It also runs on `tool_search`, with a compaction that unloads a tool and a Run that loads it again, and on `long_session` with and without the model's Recap.

**Status:** ✅ 2026-11-03. 77 of 77 Turns over the eight tasks, each built identically by the three views. The loaded tools at the end of the 7 Runs of `tool_search` are 1, 3, 4, 5, 5, 6 and 6, and the check holds across them.

#### G-G07 — No unplanned change to the prefix ⭐

**Want:** [Idle costs almost nothing](../PROPOSAL.md#it-runs-on-many-pods), under [A request only grows at its end](../DESIGNS.md#2-a-request-only-grows-at-its-end).

**Acceptance:** every request starts with the bytes of the request before it in the same Session, across Turns, across Runs, after a reload and on another instance, in 100% of request pairs. The exceptions are the moments the design names: a new Round leaving out the last Round's reasoning, a compaction, and a merge of the oldest Segments. A tool the model loads is no exception where the wire API has a native form of deferred tools: the tool list keeps its bytes and the load appears as a reference at the end. Where it has none (the list form, which `openai-chat-completions` always uses), a load may add tools to the tool list, and only that. Four checks run:

- The reference workload plays each task twice, the second time with reasoning in every reply.
  - Each task is several Runs with a reload and a change of instance between them.
  - Each request is compared with the one before it, field by field, with the earlier Messages first byte for byte (`TestRequestOnlyGrowsAtItsEnd`).
- The same requests are encoded for the three wire APIs in the native and the list form (`TestWireRequestOnlyGrowsAtItsEnd`).
  - The JSON bodies are compared, and the cache marks are the one allowed difference.
- `internal/fakemodel` is played with the `reasoning` quirk on every reply.
  - Consecutive requests are compared as the fake recorded them.
- `long_session` as 30 Runs is compared across compactions and merges.
  - It runs on the `ModelRequest` and on the encoded bodies of the three wire APIs.

**Status:** ✅ 2026-11-03. 69 of 69 request pairs in each of the two plays over the eight tasks, with no exception; 19 of them are in `tool_search`, and 5 of those cross a load. `long_session` holds in 299 request pairs, of which 104 to 119 cross a compaction and 5 or 6 a merge, with and without the model's Recap. The fake model accepted every request. Not covered: reasoning together with a compaction in the middle of a Run, and a real provider, since the fake plays the documented formats.

#### G-G10 — A long Session stays within its window ⭐

**Want:** [No lost conversations](../PROPOSAL.md#it-runs-on-many-pods), under [The Context is a bounded view](../DESIGNS.md#3-the-context-is-a-bounded-view).

**Acceptance:** `long_session` plays 2,000 Turns on one Session, each with a 20 KB Tool Result, against a fake model with a 128K-token window, and sends 0 requests over the window and fails 0 Runs for its size. The Runner's ceiling is 112K tokens. The driver counts each request by the estimate the model reports (bytes of the instruction, Messages and tools divided by 4) and refuses one over the window as a provider would. The estimate counts a Tool Result twice, so it overstates a Context full of results. The check can fail: a Tool Result larger than the window leaves every later request over it, and the report says so. Under the race detector CI plays 300 Turns, since 2,000 take over a minute.

**Status:** ✅ 2026-11-03. 2,000 Turns and 2,000 requests, 0 over the window (the largest 112,007 tokens), 0 failed Runs, 272 compactions and 1 merge, in 6.0 s. With the model's Recap on (`long_session_recap`) it passes too: 0 over the window, 279 compactions and 2 merges, and the largest request, which is a Recap's, 122,618 tokens.

#### G-G11 — A failure leaves the Session usable ⭐

**Want:** [No lost conversations](../PROPOSAL.md#it-runs-on-many-pods), under [A failure stays local](../DESIGNS.md#4-a-failure-stays-local).

**Acceptance:** each fault is injected through a Runner into a Run of a Session that already holds a finished Run (`TestFailureLeavesTheSessionUsable`). After it, every step committed before it is still in the Record, the failed Run's status and cause are right, and the next Run succeeds with the history a load from the store gives, in 100% of fault points. The faults are a provider error (a refused call and a stream cut mid-reply, each on the first call and after a tool step), a Tool that panics, a Tool that hangs (ended by the Run's deadline and by an abort), a store write that fails once, a subscriber that stops reading, and a stream sink that stops reading. An instance that dies is G-G01 and a client that disconnects is G-G02. A failed write settles the Run as failed or returns the store error, and is never reported as a success.

**Status:** ✅ 2026-11-03. Every fault passes. Not covered: the faults are not injected over the reference tasks.

#### G-G08 — One owner across instances

**Want:** [One pod at a time](../PROPOSAL.md#it-runs-on-many-pods), under [No instance is special](../DESIGNS.md#8-no-instance-is-special).

**Acceptance:** three instances over one memory store, and over one file store, take concurrent prompts and messages (Steers, FollowUps and Interrupts that arrive mid-Run) sent to random instances, with one instance killed partway (`TestManyInstancesOneWriter` and `TestManyInstancesOneWriterWithMessages` in `host`). Every message lands in exactly one Turn: 0 lost, duplicated or reordered, and 0 model calls by an instance that does not hold the Session. A Run refused with 409 writes nothing, and a message sent to a non-writer is refused `not_writer` with the writer's address.

**Status:** ✅ 2026-11-03.

#### G-G02 — No silent stream cutoffs

**Want:** [Common endpoints](../PROPOSAL.md#it-fits-into-your-service), under [A failure stays local](../DESIGNS.md#4-a-failure-stays-local).

**Acceptance:** over HTTP, 100% of streams end with a final event, 100% of replies cut by the output limit say so, and a client that disconnects mid-stream still gets its Run settled as canceled and saved with its prompt (`adapter/http/handler_conn_test.go`, and `TestStreamDisconnectSettlesAndSavesTheRun` in `internal/workload`).

**Status:** ✅ 2026-11-03.

#### G-G04 — No failures caused by Gotato

**Want:** [A library, not a new stack](../PROPOSAL.md#it-fits-into-your-service), under [A failure stays local](../DESIGNS.md#4-a-failure-stays-local).

**Acceptance:** the reference workload and the live run attribute every failure to its cause: the model, the provider after retries, a tool, the store, a limit, an extension, an abort or Gotato. 0 Runs fail because of Gotato or the store, 0 tool calls fail because of Gotato, and every failed tool call returns its reason to the model. `gotato-workload` exits 3 when this fails.

**Status:** ✅ 2026-11-03 on the reference workload: 0 of the failures in 30 plays of the eight tasks are Gotato's or the store's. The live run, 2026-10-16: 0 in 85 Runs and 85 tool calls.

#### G-G03 — Context fidelity

**Want:** [You see what the model sees](../PROPOSAL.md#it-stays-simple), under [A Session can be stopped and seen](../DESIGNS.md#9-a-session-can-be-stopped-and-seen).

**Acceptance:** the reference workload compares an inspection of each Turn's Transcript with that Turn's `context_built` Event. In 100% of Turns the Context that inspection shows has the prefix hash of the request the Turn sent. `gotato-workload` exits 3 when this fails.

**Status:** ✅ 2026-11-03. 0 mismatches in 1,500 Turns (30 plays of the eight tasks).

#### G-G05 — Effect cap holds

**Want:** [Safe tools without a sandbox](../PROPOSAL.md#it-stays-simple), under [Serve purpose-built agents](../DESIGNS.md#7-serve-purpose-built-agents).

**Acceptance:** Agents with every cap are given tools of every Effect, and the model is made to call each one by name (`TestEffectCapHidesToolsAboveIt`). 0 tools above an Agent's Effect cap reach the model, and 0 calls to them run.

**Status:** ✅ 2026-11-03.

#### G-G06 — Breaking changes are written down

**Want:** [Stable contracts](../PROPOSAL.md#it-stays-stable), under [Stay inside the boundary](../DESIGNS.md#6-stay-inside-the-boundary).

**Acceptance:** at each release, 100% of the breaking changes since the first release have a before-and-after entry in CHANGELOG.md, and each one that touches HTTP raises the contract version. The review runs `apidiff` on the shipped packages against the previous tag, and compares the HTTP routes and bodies, the Event kinds and fields, and the stored shapes.

**Status:** ⬜ Not checked since 0.2.0, the first release, which had no earlier form to break. The check is due at 0.3.0: the Unreleased entries carry before-and-after notes, and `apidiff` has not been run. The HTTP contract version is still `"1"` while Unreleased changes the answer of `GET /v1/sessions` ([TODO.md](TODO.md)).

### Measures

#### G-B14 — Context size and cost per Turn by the Session's age ⭐

Input tokens per request, and cost per Turn, at Turn 10, 100, 1,000 and 2,000. The size should level off, not grow with the Session's age.

**Want:** [No lost conversations](../PROPOSAL.md#it-runs-on-many-pods), under [The Context is a bounded view](../DESIGNS.md#3-the-context-is-a-bounded-view).

**How:** the size comes from `long_session` (field `window.tokens_at_turn` of `gotato-workload -tasks long_session`); the cost from the live run, which cannot play this task yet.

**Formula:**

```
size(n) = the model's estimate of the request at Turn n        tokens, fake model
cost(n) = the request's price at Turn n, from its Usage         US dollars, live run
```

**Last value:** size on 2026-11-03: 374 tokens at Turn 1, 93,951 at Turn 10, 55,748 at Turn 100, 47,391 at Turn 1,000 and 72,597 at Turn 2,000. With the model's Recap on: 374, 93,951, 56,418, 67,684 and 74,477. The compactions keep it under the 112K ceiling and none grows with the age. Turn 10 is high because the first Run has read its Tool Results and nothing is compacted yet. The cost is not measured.

#### G-B15 — The first request after an idle return ⭐

Its cached input tokens and its cost, when the user comes back after the cache retention has passed.

**Want:** [Idle costs almost nothing](../PROPOSAL.md#it-runs-on-many-pods), under [A request only grows at its end](../DESIGNS.md#2-a-request-only-grows-at-its-end).

**How:** a live run with a pause longer than the cache retention, once with the idle compaction and once without. No task or driver plays this pause yet.

**Formula:**

```
cached(first request) = cache read tokens of the first request after the pause      tokens
cost(first request)   = its price from its Usage                                     US dollars
```

**Last value:** not measured. A fake model has no cache, so it needs a provider's, through a team's own endpoint.

#### G-B16 — Cost of a compaction

The tokens and the time of the call that writes the Recap, against what the compaction takes out of each later request.

**Want:** [No lost conversations](../PROPOSAL.md#it-runs-on-many-pods), under [The Context is a bounded view](../DESIGNS.md#3-the-context-is-a-bounded-view).

**How:** the tokens from `long_session_recap` (`gotato-workload -tasks long_session_recap`, field `window.recap`); the time and the cost from the live run.

**Formula:**

```
read per call   = Recap input tokens / Recap calls               tokens
taken out       = tokens removed from the Context / compactions  tokens
```

**Last value:** tokens on 2026-11-03, from the fake model's report: 279 Recap calls over 2,000 Turns, 277 for a Segment and 2 for a merge. A call reads 116,416 tokens on average (32.5M in all), which a provider reads from its cache, and writes 44 (12,276 in all). Each of the 277 compactions takes 70,874 tokens out of the Context on average (19.6M in all), so a later request is that much smaller until the Context grows back. The time and the cost are not measured.

#### G-B13 — Time to a first agent

**Want:** [A library, not a new stack](../PROPOSAL.md#it-fits-into-your-service), under [Keep subtracting](../DESIGNS.md#5-keep-subtracting).

**How:** timed sessions with developers new to Gotato, each from `go get` to an agent answering over HTTP in an existing Go service.

**Formula:**

```
G-B13 = the time from `go get` to the first answer over HTTP    minutes, per developer
```

**Last value:** not measured. The first measurement sets the reference.

### Benchmarks

#### G-B01 — Cache hit rate ⭐

**Want:** [Idle costs almost nothing](../PROPOSAL.md#it-runs-on-many-pods), under [A request only grows at its end](../DESIGNS.md#2-a-request-only-grows-at-its-end).

**Problem set:** the live run of the reference tasks. A provider charges cached input at a fraction of the base price; Anthropic's is 0.1× or less ([Prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)).

**Formula:**

```
G-B01 = cache read tokens / input tokens, from Turn 2 on        a ratio per task
```

**Last score:** `history_100` 0.93, `history_1000` 0.99 and `support_case` 0.17 on 2026-10-16. The other tasks send requests under 1,024 tokens, which the provider never caches, and `support_case` averages 1,080 tokens a request, so only its later Turns are cached. Without the cache key every task measured 0.

#### G-B02 — Token cost per task

**Want:** [Idle costs almost nothing](../PROPOSAL.md#it-runs-on-many-pods), under [A request only grows at its end](../DESIGNS.md#2-a-request-only-grows-at-its-end).

**Problem set:** the live run of the reference tasks, with the same model and price from one release to the next.

**Formula:**

```
G-B02 = uncached input × input price + cache read × read price + cache write × write price + output × output price     US dollars per play
```

**Last score:** per play on 2026-10-16: `lookup` $0.0008, `conversation` $0.0099, `tool_error` $0.0015, `support_case` $0.0096, `history_10` $0.0017, `history_100` $0.0054 and `history_1000` $0.030.

#### G-B03 — Tool-call success rate, split by cause

The causes are the model's arguments, the tool and Gotato. Only Gotato's share is a goal (G-G04); the rest belongs to the model and the tools.

**Want:** [Safe tools without a sandbox](../PROPOSAL.md#it-stays-simple), under [A failure stays local](../DESIGNS.md#4-a-failure-stays-local).

**Problem set:** the live run of the reference tasks.

**Formula:**

```
G-B03 = tool calls that succeeded / tool calls, with each failure counted under its cause     a ratio
```

**Last score:** 80 of 85 on 2026-10-16. The 5 failures were the tool's: `tool_error` asks for an order that does not exist. 0 came from the model's arguments and 0 from Gotato. In `support_case` the model refunded the right order every time.

#### G-B04 — Error rate, split by cause

The causes are the model, the provider after retries, the store and Gotato. Only Gotato's and the store's share is a goal (G-G04).

**Want:** [No lost conversations](../PROPOSAL.md#it-runs-on-many-pods), under [A failure stays local](../DESIGNS.md#4-a-failure-stays-local).

**Problem set:** the live run of the reference tasks.

**Formula:**

```
G-B04 = Runs that failed / Runs, with each failure counted under its cause     a ratio
```

**Last score:** 0 of 85 Runs failed on 2026-10-16.

#### G-B05 — Gotato's time per Turn

Building the Context, assembling the request, the Loop and saving, against the model's own time on the same Turn.

**Want:** [A small footprint](../PROPOSAL.md#it-fits-into-your-service), under [Keep subtracting](../DESIGNS.md#5-keep-subtracting).

**Problem set:** the reference workload with a fake model for Gotato's time (`BenchmarkReferenceTasks`, and 30 plays of `gotato-workload`), and the live run for the model's. G-B09 holds the same time at three sizes.

**Formula:**

```
G-B05 = (Run time − model time − tool time − load time) / Turns        ms per Turn
share = G-B05 / the model's time per Turn                               live run
```

**Last score:** on 2026-11-03, with a memory store, which stands for a fast database, 0.15 ms per Turn for `lookup`. A file store syncs each write twice: 8 writes make a `lookup` Run, and a play took 102 ms to save them, about 13 ms a write. On 2026-10-17 the live run measured 0.25 to 0.7 ms per Turn (3.7 ms with 1,000 messages) against 2 to 6 s of the model's own time, under 0.1%.

#### G-B06 — Time added before the first token

**Want:** [A small footprint](../PROPOSAL.md#it-fits-into-your-service), under [Keep subtracting](../DESIGNS.md#5-keep-subtracting).

**Problem set:** the reference workload for Gotato's part, and the live run for the provider's own time to its first token.

**Formula:**

```
G-B06 = time from a Run's start to its first model request       ms per Run (`before_request_per_run_ms`)
```

**Last score:** on 2026-11-03, with a memory store, 0.09 to 0.24 ms for the small tasks, 0.51 ms with 100 messages and 4.7 ms with 1,000; with a file store, 16 to 22 ms, from the two writes that come first, the prompt and the model call. The live run, 2026-10-17: 1.8 to 4.4 s for the model's first text token, and 10.7 s in `support_case`, whose answer comes after its reasoning.

#### G-B07 — Cold-load time

Loading a Session from the store before its Run, against the model's own time.

**Want:** [Any pod answers like the only one](../PROPOSAL.md#it-runs-on-many-pods), under [The store is the only truth](../DESIGNS.md#1-the-store-is-the-only-truth).

**Problem set:** the history tasks with 10, 100 and 1,000 messages on a file store (`gotato-workload -store DIR`), and `conversation`, whose 10 Runs reuse a warm Session.

**Formula:**

```
G-B07 = store load time / Runs        ms per Run
```

**Last score:** on 2026-11-03, from a file store: 0.66 ms with 10 messages, 0.59 with 100 and 1.6 with 1,000, under 0.1% of the model's 2 to 6 s per Turn in the live run of 2026-10-17. On 2026-10-25 the same loads were 0.18, 0.25 and 0.82 ms. With a warm Session, a Run on a copy whose head is unchanged loads nothing: the 10-Run `conversation`, played 20 times on a file store, loads 0.062 ms per Run (0.031 on 2026-10-25).

#### G-B08 — Memory per task

Heap per live Session, and the resident memory of the reference service.

**Want:** [A small footprint](../PROPOSAL.md#it-fits-into-your-service), under [Keep subtracting](../DESIGNS.md#5-keep-subtracting).

**Problem set:** `BenchmarkSessionFootprint` (100 Sessions, idle and after 25 tool calls with 100 KB of history each), `BenchmarkWarmSessionFootprint` (100 warm Sessions of about 200-byte Messages) and the reference service's resident memory. A coding-agent session starts at 1 GiB ([Hosting the Agent SDK](https://code.claude.com/docs/en/agent-sdk/hosting)).

**Formula:**

```
heap per Session   = (heap with N Sessions − heap with none) / N        bytes
resident memory    = the `ps` RSS of the stripped binary                KB
```

**Last score:** on 2026-11-03, heap per live Session 4 to 9 KB idle and 253 to 264 KB after 25 tool calls (190 to 210 KB on 2026-10-16). A warm Session holds its Record and Writer between Runs: 8 to 15 KB with 10 Messages, 49 to 73 KB with 100 and 575 to 586 KB with 1,000, about 570 bytes a Message, nearly three times the Message's own size; what the Record keeps beyond the Messages is the first thing to look at if this needs to come down. The reference service holds 12.1 MB resident when idle and 19.7 MB after 200 health checks and 200 new Sessions (12 MB and 16 MB on 2026-10-16); that run was made by hand, since no script plays it ([TODO.md](TODO.md)).

#### G-B09 — CPU per Turn, at 10, 100 and 1,000 messages

**Want:** [A small footprint](../PROPOSAL.md#it-fits-into-your-service), under [Keep subtracting](../DESIGNS.md#5-keep-subtracting).

**Problem set:** `history_10`, `history_100` and `history_1000` with a fake model and a memory store, where Gotato is CPU-bound (`BenchmarkReferenceTasks`, and `gotato-workload` with 30 plays).

**Formula:**

```
G-B09 = (Run time − model time − tool time − load time) / Turns        ms per Turn
```

**Last score:** on 2026-11-03, 0.13, 0.46 and 3.4 ms. Against 0.2.0 built and run alternately on the same busy machine, three times each, the current code is about 10 to 35% slower: 0.12 to 0.13, 0.40 to 0.46 and 2.9 to 3.0 ms, against 0.10, 0.31 to 0.39 and 2.2 to 2.7 ms ([TODO.md](TODO.md)). On 2026-10-17 it was 0.045, 0.15 and 1.1 ms.

#### G-B10 — Startup time

From process start to ready to serve.

**Want:** [A small footprint](../PROPOSAL.md#it-fits-into-your-service), under [Keep subtracting](../DESIGNS.md#5-keep-subtracting).

**Problem set:** `BenchmarkStartup` on the reference service. kagent measured 2.7 s for a Go runtime and 18.2 s for a Python one ([Go vs Python runtime](https://kagent.dev/blog/go-vs-python-runtime)).

**Formula:**

```
G-B10 = the time from process start to the first 200 from /healthz, averaged over 20 starts      ms
```

**Last score:** 30 ms on 2026-11-03, 25 ms on 2026-10-16.

#### G-B11 — Concurrent Runs per instance before Gotato's time per Turn doubles

**Want:** [A small footprint](../PROPOSAL.md#it-fits-into-your-service), under [Keep subtracting](../DESIGNS.md#5-keep-subtracting).

**Problem set:** `lookup`, `conversation` and `support_case` with fake models, 64 plays each, at 1, 2, 4, 8 and 16 plays at once (`gotato-workload -concurrency N`), on 8 cores.

**Formula:**

```
G-B11 = the largest N where Gotato's time per Turn at N plays at once ≤ 2 × its time at 1 play        concurrent Runs
```

**Last score:** between 4 and 8 on 2026-11-03: the time per Turn rose 1.5 to 2.0× at 4, 2.7 to 3.0× at 8 and 5.3 to 5.9× at 16, in two runs. On 2026-10-16 it rose 1.6× at 4 and 3× at 8. A fake model never waits, so this is the CPU-bound floor; a real model's wait leaves the CPU free for more Runs.

#### G-E01 — The Recap keeps the facts ⭐

A real model answers questions whose answers appeared only in Tool Results before a compaction, such as an ID, a number or an error code.

**Want:** [No lost conversations](../PROPOSAL.md#it-runs-on-many-pods), under [The Context is a bounded view](../DESIGNS.md#3-the-context-is-a-bounded-view).

**Problem set:** the payments check, which Gotato ships and a team runs on demand against its own model endpoint ([FEATURES.md → F5.1.4: The Recap](../FEATURES.md#f514-the-recap)): three prompts on one Session, a compaction with the model's Recap, and five questions asked before and after it (unsaved, with the tools switched off). The command is `go run ./internal/cmd/gotato-workload -recap-check -live -gateway-config gateway.yaml`, with [internal/cmd/gatewayconf/gateway.example.yaml](../internal/cmd/gatewayconf/gateway.example.yaml) as the file to copy. Its scoring is tested on a fake model. Gotato runs no checks against real providers itself.

**Formula:**

```
G-E01 = questions answered correctly after the compaction / 5        a share, set against the share before it
```

**Last score:** not measured. No endpoint has run the check yet, and the model's Recap stays off by default until one has.

### Comparisons

#### G-B12 — Footprint

Modules and stripped binary of a program that imports the agent, the Runner, a store and the HTTP handler.

**Want:** [A small footprint](../PROPOSAL.md#it-fits-into-your-service), under [Keep subtracting](../DESIGNS.md#5-keep-subtracting).

**Against:** Google ADK for Go v2.6.0.

**How:** the same kind of program is built with each library, with its agent, runner, session store and HTTP server and nothing else. Gotato's is the reference service, run with `bench/refservice/footprint.sh`, which also imports the model bridge; the count of packages is `go list -deps` outside the standard library, the service's own included.

**Formula:**

```
G-B12 = Gotato's value / ADK's value, for modules linked, packages outside the standard library, and stripped binary bytes
```

**Last result:** on 2026-11-03, Gotato 3 modules linked (the service, Gotato and its HTTP adapter; `go list -m all` also lists `yaml.v3`, which the binary does not link), 15 packages and 9.0 MB (9,030,338 bytes), with Go 1.27.2 on darwin/arm64 (Apple M3). ADK for Go 128 modules, 370 packages and 14.8 MB, on 2026-10-15 on the same machine. So 0.02 of the modules, 0.04 of the packages and 0.61 of the size. The binary was 10.6 MB before a fix of 2026-11-03 that stopped the linker keeping every exported method.

### Constraints

#### G-R01 — Each package imports only the layers below it

**Want:** [A small footprint](../PROPOSAL.md#it-fits-into-your-service), under [Each layer does one job, and each default can be replaced](../DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced).

**Condition:** the core and the shared package of `internal/` import only the standard library. Every other shipped package imports only what its layer allows, in the order `host`, `profile`, `store`, `record`, `gotato`, `ref`; `model`, `testkit` and the adapters sit beside them and import down. The module root holds no Go code.

**How:** `TestLayering` in `gotato/layering_test.go`, which fails on an edge the table does not list and on an entry no code uses.

**Status:** ✅ 2026-11-03.

#### G-R02 — A Session runs in a small main with no server or database

**Want:** [A library, not a new stack](../PROPOSAL.md#it-fits-into-your-service), under [Stay inside the boundary](../DESIGNS.md#6-stay-inside-the-boundary).

**Condition:** `gotato.NewSession` with a model and nothing else runs a Prompt to a settled result in the same process, with the conversation in memory; no server, daemon, store or network is needed.

**How:** the examples in `gotato/example_test.go`, which run with `testkit.EchoModel` and a fake model endpoint.

**Status:** ✅ 2026-11-03.
