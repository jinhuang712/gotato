<!-- what-we-build: drafted 2026-10-26 -->
# Gotato — Goals

What Gotato measures, so that each principle in [DESIGNS.md](DESIGNS.md) is a promise that can fail. Every item serves one principle; ⭐ marks the core ones, which serve the four guarantees. An item keeps its number wherever it is listed.

**Checks.** A test passes or fails them, and their number is fixed by their nature: 0 failures, or 100%.

| # | Check | Serves | Status |
|---|---|---|---|
| G-G01 | ⭐ Session reconstructable | The store is the only truth | ✅ |
| G-G09 | ⭐ The same Record gives the same request | The store is the only truth | ✅ |
| G-G07 | ⭐ No unplanned change to the prefix | A request only grows at its end | 🚧 |
| G-G10 | ⭐ A long Session stays within its window | The Context is a bounded view | ✅ |
| G-G11 | ⭐ A failure leaves the Session usable | A failure stays local | ✅ |
| G-G08 | One owner across instances | No instance is special | ✅ |
| G-G02 | No silent stream cutoffs | A failure stays local | ✅ |
| G-G04 | No failures caused by Gotato | A failure stays local | ✅ |
| G-G03 | Context fidelity | A Session can be stopped and seen | ✅ |
| G-G05 | Effect cap holds | Serve purpose-built agents | ✅ |

**Benchmarks.** Numbers Gotato tracks. Alone a number means nothing, so each one names what it is compared with.

| # | Benchmark | Serves | Status |
|---|---|---|---|
| G-B01 | ⭐ Cache hit rate | A request only grows at its end | ✅ |
| G-B14 | ⭐ Context size and cost per Turn by the Session's age | The Context is a bounded view | 🚧 |
| G-B15 | ⭐ The first request after an idle return | A request only grows at its end | ⬜ |
| G-B16 | Cost of a compaction | The Context is a bounded view | 🚧 |
| G-B02 | Token cost per task | A request only grows at its end | ✅ |
| G-B03 | Tool-call success rate, split by cause | A failure stays local | ✅ |
| G-B04 | Error rate, split by cause | A failure stays local | ✅ |
| G-B05 | Gotato's time per Turn | Keep subtracting | ✅ |
| G-B06 | Time added before the first token | Keep subtracting | ✅ |
| G-B07 | Cold-load time | The store is the only truth | ✅ |
| G-B08 | Memory per task | Keep subtracting | ✅ |
| G-B09 | CPU per Turn, at 10, 100 and 1,000 messages | Keep subtracting | ✅ |
| G-B10 | Startup time | Keep subtracting | ✅ |
| G-B11 | Concurrent Runs per instance before Gotato's time per Turn doubles | Keep subtracting | ✅ |
| G-B12 | Footprint | Keep subtracting | ✅ |

**Evaluations.** A person or a real model judges them, on demand and not in CI.

| # | Evaluation | Serves | Status |
|---|---|---|---|
| G-E01 | ⭐ The Recap keeps the facts | The Context is a bounded view | ⬜ |
| G-B13 | Time to a first agent | Keep subtracting | ⬜ |
| G-G06 | Breaking changes are written down | Stay inside the boundary | ⬜ |

## How Gotato measures

Every number is measured on this repository's own workloads:

- the **reference workload**: a few scripted tasks run in CI with fake models (a short lookup, a 10-Turn conversation, a tool error and its recovery, a support case the model works through in five Turns of its own choosing, a Session whose tools load and unload, and Sessions with 10, 100 and 1,000 messages), from `testkit.ReferenceTasks`, played through a Runner by `service/workload`;
- the **live run**: the same tasks against a real provider with a fixed model and its price, run on demand with `gotato-workload -live`, because CI has no provider credentials;
- **fault injection** and a **multi-instance harness** for the checks that need a crash or a second instance.

A method marked *(to build)* does not exist yet; building it is roadmap work. Every number about a Run can also be computed from Events and `Usage` alone, so a team gets the same numbers from its own production telemetry ([DESIGNS.md → Every number about a Run comes from Events and `Usage`](DESIGNS.md#every-number-about-a-run-comes-from-events-and-usage)). Memory, startup and footprint describe the process, and come from benchmarks on the **reference service** in `bench/refservice`: the smallest service a team would write around Gotato. Until the first release is cut, "the previous release" means the numbers of Phase 1 in [ROADMAP.md](ROADMAP.md).

The live run of 2026-10-16 played every task five times, and `support_case` ten times, against `gpt-6.1-sol`, with the gateway sending each Session's cache key. Its cost uses the list price of $2 input, $10 output and $0.10 cached input per million tokens. The other numbers of that date come from an Apple M3 with 8 cores and Go 1.27.2.

## Checks

Each check is a test or a harness that passes or fails.

### G-G01 — Session reconstructable ⭐

- **Serves:** [The store is the only truth](DESIGNS.md#1-the-store-is-the-only-truth)
- **Measure:** After a crash at any step of a Run, the Session loaded from the store equals the Session at its last written step, with every tool call answered, and the Context rebuilt from it has the same prefix hash, in 100% of crash points
- **Method:** fault injection after every write of the reference workload (`TestCrashAtEveryWriteRebuildsTheLastStep`)
- **Status:** ✅ 244 of 244 crash points over the eight tasks, in CI. Each write holds at most one more stored Message than the one before. 2026-10-17. The `tool_search` task found a window the earlier tasks had no way to reach: a `search_tools` result was written before its `tool_loaded` Event, so a crash between them left a result saying "Loaded" for a tool the Record did not hold as loaded. The Event is now written in the same write as the result, 2026-10-25.

### G-G09 — The same Record gives the same request ⭐

- **Serves:** [The store is the only truth](DESIGNS.md#1-the-store-is-the-only-truth)
- **Measure:** Two instances, and one instance after a reload, build byte-identical requests from the same Record and Agent, in 100% of the reference workload's Turns.
- **Method:** `TestSameRecordSameRequest` plays each reference task as several Runs on one Session, moving between instances and reloads. Then, for every request the model got, it writes the Record as it stood at that Turn to a new Session, and builds the request again on instance a, on instance b and on a new Runner of instance a. It compares the JSON of the whole `ModelRequest` (instruction, Messages, Tools, options, cache breakpoints and cache key), not the prefix hash. G-G01 also compares the prefix hash after a crash.
- **Status:** ✅ 77 of 77 Turns over the eight tasks, each built identically by the three views, in CI. A check on the `ModelRequest`, not on a provider's wire bytes. 2026-10-25. The Record that is copied at each Turn includes the tools the model loaded, which a copy of the Messages alone leaves out. `tool_search` loads tools by name, by a query and by listing a folded ToolSet, and a call to an unloaded tool gets the "loaded now" failure. `TestSameRecordSameRequestAcrossLoadsAndUnloads` plays it with a compaction after Run 5 that unloads `carrier.track` and has Run 6 load it again, and inspects the Session at the end of each of the 7 Runs on the instance that ran it, on the other instance and on a new Runner after a reload: all 7 end with the same request from the three views. Every Run resolves the ToolSets again at its start, so each of those Runs is also a re-resolution; a loaded tool that its ToolSet drops (`gone`) is not in the reference task, and is covered by the core's tests (G-F10). Where the Session compacts, `TestSameRecordSameRequestAcrossCompactions` inspects `long_session` at the end of each of its 30 Runs on the instance that ran it, on the other instance and on a new Runner after a reload, with and without the model's Recap: at all 60 Run ends (30 in each play) the three views build the same request, the last Checkpoint being a compaction, a merge or neither, 2026-10-25.

### G-G07 — No unplanned change to the prefix ⭐

- **Serves:** [A request only grows at its end](DESIGNS.md#2-a-request-only-grows-at-its-end)
- **Measure:** Every request starts with the bytes of the request before it in the same Session, across Turns, across Runs, after a reload and on another instance, in 100% of request pairs. The only exceptions are the moments [DESIGNS.md → A request only grows at its end](DESIGNS.md#2-a-request-only-grows-at-its-end) names: a new Round leaving out the last Round's reasoning, a compaction, and a merge of the oldest Segments. A tool the model loads is not an exception where the wire API has a native form of deferred tools: the tool list keeps its bytes, and the load appears as a reference in the Tool Result that made it, at the end. Where it has none (the list form, which `openai-chat-completions` always uses), a load adds the tool to the tool list, and that is a planned change of the tool list, allowed only in a request pair where a tool loaded, and only by adding tools.
- **Method:** `TestPrefixHashStableAcrossAppendOnlyTurns` for consecutive Turns of one Run; `TestRequestOnlyGrowsAtItsEnd` plays each reference task as several Runs on one Session, with a reload and a change of instance between Runs, and compares every request with the one before it: the same instruction, Tools, options, cache key and system and Tools breakpoints, and the earlier request's Messages first, byte for byte. It plays every task twice, the second time with reasoning in every reply. For a request with a catalog of tools, the Tools of two requests are compared by what the Model can reach: every catalog tool has the same spec in both, the tools outside the catalog are the same, and the loaded tools are the earlier ones plus those the Tool Results between the two list as loaded. `TestToolSearchRequestOnlyGrowsAtItsEnd` and `TestToolSearchRequestAcrossACompaction` check this on the `tool_search` task, the second with a compaction after Run 5, where the loaded tools may lose the ones the compaction unloads and `read_segment` may join, in one pair only. `TestWireRequestOnlyGrowsAtItsEnd` encodes the requests of every task with the gateway, for `anthropic-messages`, `openai-responses` and `openai-chat-completions`, in the native and the list form, and checks on the JSON bodies that the instruction keeps its bytes, that the earlier request's Messages come first byte for byte but for the cache marks, that in the native form the tool list keeps its bytes, and that in the list form it changes exactly in the pairs where a tool loaded, by adding tools.
- **Status:** 🚧 Consecutive Turns of one Run keep the same prefix hash and the same cache breakpoints, 2026-10-15. 69 of 69 request pairs in each of the two plays over the eight tasks, across Runs, reloads and instances, 2026-10-25. The reference play has no exception: no compaction, and the last Round's reasoning is left out in the gateway's encoder, after the request is built, so the `ModelRequest` keeps it. Of the 69 pairs, 19 are in `tool_search`, and 5 of those are across a load. On the wire, all 69 pairs hold in each wire API and form: the native form never changes the tool list, and the list form changes it in the 5 pairs with a load and in no other. The wire is checked without reasoning and without a compaction. Where the Session compacts, `TestRequestOnlyGrowsAtItsEndAcrossCompactions` plays `long_session` as 30 Runs across instances and reloads, with and without the model's Recap: of 299 request pairs, about 108 straddle a compaction and 3 to 5 a merge, and at each of them the Segments before keep their bytes and the merged Segment comes first, and the instruction, the options and the cache key keep theirs, apart from the instruction's closing sentence about the tags, 2026-10-25. The remaining gap: the gateway's bytes are not checked where the Session compacts (the Segments' and `read_segment`'s encoding at a compaction or a merge), nor with reasoning in the replies, where the encoder leaves out the last Round's reasoning by design. Until those two are checked on the wire, G-G07 stays 🚧.

### G-G10 — A long Session stays within its window ⭐

- **Serves:** [The Context is a bounded view](DESIGNS.md#3-the-context-is-a-bounded-view)
- **Measure:** A Session played for 2,000 Turns, each with a 20 KB Tool Result, against a fake model with a 128K-token window, sends 0 requests over the window and fails 0 Runs for its size. These sizes are a starting point, to tune once the task exists.
- **Method:** the reference task `long_session` (`testkit.LongSessionTask`), played by `gotato-workload -tasks long_session` and by `TestALongSessionStaysWithinItsWindow`. One Session, 100 Runs of 20 Turns each: a prompt, 19 Turns that read a 20 KB log, and an answer. The fake Model has a 128K-token window, which gives the Runner a ceiling of 112K; the driver counts every request against the window by the estimate the Model reports as its input (the bytes of the instruction, Messages and Tools ÷ 4), and refuses one over it as a provider would. The estimate counts a Tool Result twice, in its Message and in its result, so it overstates a Context full of tool results. Under the race detector the test plays 300 Turns, since 2,000 take over a minute.
- **Status:** ✅ 2,000 Turns, 2,000 requests, 0 over the window (the largest, 112,010 tokens), 0 failed Runs, 100 of 100 answers right, with 260 compactions, one of them a merge, in 3.6 s, 2026-10-25. With the model's Recap on (`long_session_recap`) it passes too: 0 over the window, the largest Recap request 122,630 tokens (the Context plus the ask). The check can fail: a Tool Result larger than the window leaves every later request over it, and the report says so (`TestALongSessionPastItsWindowIsReported`). Merging the oldest Segments is not what keeps this size within the window: with the merge turned off, the same play passes. It is what keeps a smaller window: at 32K tokens, 300 Turns of 4 KB results fail 12 Runs with `limit_exceeded` without it, and pass with it (`TestALongSessionMergesTheOldestSegments`).

### G-G11 — A failure leaves the Session usable ⭐

- **Serves:** [A failure stays local](DESIGNS.md#4-a-failure-stays-local)
- **Measure:** After each injected failure (a provider error, a Tool that panics or hangs, a store write that fails, a subscriber that stops reading, a client that disconnects, an instance that dies), every step committed before it is still in the Record, and the Session's next Run succeeds, in 100% of fault points.
- **Method:** `TestFailureLeavesTheSessionUsable` injects each fault through a Runner into a Run of a Session that already holds a finished Run, then checks the Record, the failed Run's status and cause, and that the next Run's first request holds the history a load from the store gives. G-G01 (`TestCrashAtEveryWriteRebuildsTheLastStep`) covers an instance that dies and G-G02 (`TestStreamDisconnectSettlesAndSavesTheRun`) a client that disconnects. The faults are not yet injected over the reference workload's tasks.
- **Status:** ✅ Passes for every fault, 2026-10-25: a provider error (a refused call and a stream cut mid-reply, each on the first call and after a tool step), a Tool that panics, a Tool that hangs (ended by the Run's deadline, and by a cancel), a store write that fails once (all 10 write points of a Run), a subscriber that stops reading, and a stream sink that stops reading. A crash and a disconnect pass through G-G01 and G-G02. On a failed write, the Run settles as failed, or returns the store error, and is never reported as a success.

### G-G08 — One owner across instances

- **Serves:** [No instance is special](DESIGNS.md#8-no-instance-is-special)
- **Measure:** With several instances, concurrent messages and random instance kills, every message lands in exactly one Turn: 0 lost, duplicated or reordered Turns, and 0 model calls by an instance that does not own the Session
- **Method:** multi-instance harness over one shared store (`TestManyInstancesOneWriter` for prompts, `TestMessagesMidRunLandOnceOnTheWriter` and `TestManyInstancesOneWriterWithMessages` for Steers, FollowUps and Interrupts that arrive mid-Run); a harness behind a hashing proxy is still to build
- **Status:** ✅ Three instances over one memory store, and over one file store, take concurrent prompts sent to random instances, with one instance killed partway. Every completed Run's prompt lands once, followed by its reply, nothing is duplicated or interleaved, and 0 model calls come from an instance that does not hold the Session. A Run refused with 409 writes nothing. 2026-10-17. With messages, 2026-10-25: a Run held on one instance takes 18 messages sent to three instances at once; the 6 sent to the writer land once each, in queued order per kind, and the 12 sent elsewhere are refused `not_writer` with the writer's address and land nowhere. With prompts and messages sent at random to three instances, one killed, a message lands at most once, one a live instance accepted is committed or dropped and never lost without a word, and 0 model calls come from an instance that does not hold the Session.

### G-G02 — No silent stream cutoffs

- **Serves:** [A failure stays local](DESIGNS.md#4-a-failure-stays-local)
- **Measure:** 100% of streams end with a final event. 100% of replies cut by the output limit say so. A client that disconnects mid-stream still gets its Run settled and saved.
- **Method:** fault injection on the HTTP and gRPC streams (`TestStreamDisconnectSettlesAndSavesTheRun`)
- **Status:** ✅ The final event exists, and output-limit cutoffs are reported. A client that disconnects mid-stream gets its Run settled as cancelled and saved with its prompt, over HTTP and gRPC. 2026-10-17.

### G-G04 — No failures caused by Gotato

- **Serves:** [A failure stays local](DESIGNS.md#4-a-failure-stays-local)
- **Measure:** 0 Runs fail because of Gotato or the store, and 0 tool calls fail because of Gotato. Every failed tool call returns its reason to the model.
- **Method:** the reference workload and the live run, with each failure attributed to its cause
- **Status:** ✅ Passes: 0 on the reference workload in CI, and 0 in the live run (85 Runs, 85 tool calls), 2026-10-16.

### G-G03 — Context fidelity

- **Serves:** [A Session can be stopped and seen](DESIGNS.md#9-a-session-can-be-stopped-and-seen)
- **Measure:** In 100% of Turns, the Context that inspection shows has the same prefix hash as the request the Turn sends
- **Method:** the reference workload compares an inspection of each Turn's Transcript with the Turn's `context_built` Event (`TestReferenceWorkload`)
- **Status:** ✅ Passes, 2026-10-16.

### G-G05 — Effect cap holds

- **Serves:** [Serve purpose-built agents](DESIGNS.md#7-serve-purpose-built-agents)
- **Measure:** 0 tools above an Agent's Effect cap reach the model, and 0 calls to them run
- **Method:** a test that gives tools of every Effect to Agents with every cap, and has the model call each one by name (`TestEffectCapHidesToolsAboveIt`)
- **Status:** ✅ Passes, 2026-10-16.

## Benchmarks

Each benchmark is a number tracked against what it names.

### G-B01 — Cache hit rate ⭐

Cached input tokens ÷ input tokens, from Turn 2 on.

- **Serves:** [A request only grows at its end](DESIGNS.md#2-a-request-only-grows-at-its-end)
- **Compared with:** the previous release, and the uncached price: Anthropic charges cached input at 0.1× the base price or less ([Prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching))
- **Method:** live run
- **Last measured:** `history_100` 0.93, `history_1000` 0.99, `support_case` 0.17, 2026-10-16. The other tasks send requests under 1,024 tokens, which the provider never caches, and `support_case` averages 1,080 tokens a request, so only its later Turns are cached. Without the cache key, every task measured 0.

### G-B14 — Context size and cost per Turn by the Session's age ⭐

Input tokens per request and cost per Turn at Turn 10, 100, 1,000 and 2,000.

- **Serves:** [The Context is a bounded view](DESIGNS.md#3-the-context-is-a-bounded-view)
- **Compared with:** the same Session's numbers at Turn 10: they should level off, not grow with the Session's age
- **Method:** the `long_session` task of G-G10 for size (`gotato-workload -tasks long_session`, field `window.tokens_at_turn`); the live run for cost *(to build)*
- **Last measured:** Size, from the fake Model's estimate, 2026-10-25: the request holds 159 tokens at Turn 1, 93,786 at Turn 10, 54,824 at Turn 100, 90,127 at Turn 1000 and 85,080 at Turn 2000. With the model's Recap on: 159, 93,786, 55,519, 99,521 and 95,990. The compactions hold it under the ceiling, 112K, and the Turns sampled hold 55K to 100K, depending on where each falls between two compactions; none grows with the Session's age. Turn 10 is high because the first Run has read its Tool Results and nothing is compacted yet. The cost per Turn is not measured: it needs a live run.

### G-B15 — The first request after an idle return ⭐

Its cached input tokens and its cost, when the user comes back after the cache retention has passed.

- **Serves:** [A request only grows at its end](DESIGNS.md#2-a-request-only-grows-at-its-end)
- **Compared with:** the same Session without idle compaction
- **Method:** live run with a pause longer than the cache retention *(to build)*
- **Last measured:** Not measured. It needs a provider's cache, which a fake model has none of, and the idle compaction, which waits for the warm Session ([ROADMAP.md](ROADMAP.md) Phase 8); until then a service calls the compact endpoint from its own timer. No number comes from a fake.

### G-B16 — Cost of a compaction

The tokens and the time of the call that writes the Recap.

- **Serves:** [The Context is a bounded view](DESIGNS.md#3-the-context-is-a-bounded-view)
- **Compared with:** the tokens the compaction takes out of each later request
- **Method:** the `long_session_recap` task for the tokens (`gotato-workload -tasks long_session_recap`, field `window.recap`); the live run for the time and the cost *(to build)*
- **Last measured:** Tokens, from the fake Model's report, 2026-10-25: 272 Recap calls over 2,000 Turns, 271 of them for a Segment and 1 for a merge. A call reads 116,889 tokens on average (31.8M in all), which a provider reads from its cache, and writes 44. Each compaction takes 72,509 tokens out of the Context on average (19.7M in all), so a later request is that much smaller until the Context grows back, about every 7 Turns here. The time and the cost need a live run.

### G-B02 — Token cost per task

(input + output tokens) × price, per reference task.

- **Serves:** [A request only grows at its end](DESIGNS.md#2-a-request-only-grows-at-its-end)
- **Compared with:** the previous release, with the same model and task
- **Method:** live run
- **Last measured:** Per play: `lookup` $0.0008, `conversation` $0.0099, `tool_error` $0.0015, `support_case` $0.0096, `history_10` $0.0017, `history_100` $0.0054, `history_1000` $0.030. 2026-10-16. Without the cache key, `history_100` cost $0.0096 and `history_1000` $0.087.

### G-B03 — Tool-call success rate, split by cause

The model's arguments, the tool, Gotato.

- **Serves:** [A failure stays local](DESIGNS.md#4-a-failure-stays-local)
- **Compared with:** the previous release. Only Gotato's share is a goal (G-G04); the rest belongs to the model and the tools.
- **Method:** live run
- **Last measured:** 80 of 85 succeeded. The 5 failures were the tool's: `tool_error` asks for an order that does not exist. 0 from the model's arguments, 0 from Gotato. In `support_case` the model refunded the right order every time. 2026-10-16.

### G-B04 — Error rate, split by cause

The model, the provider after retries, the store, Gotato.

- **Serves:** [A failure stays local](DESIGNS.md#4-a-failure-stays-local)
- **Compared with:** the previous release. Only Gotato's and the store's share is a goal (G-G04).
- **Method:** live run
- **Last measured:** 0 of 85 Runs failed. 2026-10-16.

### G-B05 — Gotato's time per Turn

Building the Context, assembling the request, saving.

- **Serves:** [Keep subtracting](DESIGNS.md#5-keep-subtracting)
- **Compared with:** the model's own response time on the same Turn, as a share, and the previous release
- **Method:** reference workload (`BenchmarkReferenceTasks`) for Gotato's time, and the live run for the model's
- **Last measured:** Every step is now written as it happens. With a fake model and a memory store, which stands for a fast database: 0.072 ms per Turn for a lookup and 1.1 ms with 1,000 messages, against 0.058 and 0.86 ms when a Run saved once, in the same measurement. A file store syncs each write twice, about 8 ms a write on macOS: 64 ms of saving per lookup Run (8 writes), against 8 ms for the single save before. In the live run of 2026-10-16, which saved once per Run: 0.25–0.7 ms per Turn (3.7 ms with 1,000 messages) against 2–6 s of the model's own time, under 0.1%; with the file store's writes it would be about 1%. 2026-10-17.

### G-B06 — Time added before the first token

- **Serves:** [Keep subtracting](DESIGNS.md#5-keep-subtracting)
- **Compared with:** the provider's own time to first token
- **Method:** live run: `before_request_per_run_ms` against `model_first_token_ms`
- **Last measured:** With a memory store, 0.05–0.2 ms from a Run's start to its first request, and 1.6 ms with 1,000 messages. A file store adds the two writes that come first, the prompt and the model call: about 16 ms on macOS. In the live run of 2026-10-16: 1.8–4.4 s for the model's first text token, and 10.7 s in `support_case`, whose answer comes after its reasoning. 2026-10-17.

### G-B07 — Cold-load time

Loading a Session from the store before its Turn.

- **Serves:** [The store is the only truth](DESIGNS.md#1-the-store-is-the-only-truth)
- **Compared with:** the model's own response time, as a share
- **Method:** reference workload, with 10, 100 and 1,000 messages
- **Last measured:** A load reads the facts from the last Checkpoint. From a file store: 0.18 ms with 10 messages, 0.25 ms with 100, 0.82 ms with 1,000, against 0.10, 0.39 and 1.36 ms for the whole document before, in the same measurement; under 0.1% of the model's 2–6 s per Turn in the live run. 2026-10-17. With the warm Session, a Run on a copy whose head is unchanged loads nothing: the 10-Run `conversation` task, played 20 times on a file store, loads 0.031 ms per Run, against 2.5 ms in the same measurement before the warm Session (one load per play instead of one per Run). A task of one Run still loads once. The check costs one read of the Entries after the head, which is none for a current copy, and the Claim written again is the write the load path made too. 2026-10-25.

### G-B08 — Memory per task

Peak heap per Run, and heap per Session in memory.

- **Serves:** [Keep subtracting](DESIGNS.md#5-keep-subtracting)
- **Compared with:** the previous release, and a coding-agent session's 1 GiB starting point ([Hosting the Agent SDK](https://code.claude.com/docs/en/agent-sdk/hosting))
- **Method:** `BenchmarkSessionFootprint`, and the reference service's resident memory
- **Last measured:** Heap per live Session: 4.5–8 KB idle, 190–210 KB after 25 tool calls with 100 KB of history. The reference service holds 12 MB resident when idle, and 16 MB after 200 health checks and 200 new Sessions. 2026-10-16. A warm Session holds its Record and Writer between Runs (`BenchmarkWarmSessionFootprint`, 100 Sessions of about 200-byte Messages, the difference between a Runner that keeps them warm and one that keeps none): 3.7 KB with 10 Messages, 65 KB with 100 and 573 KB with 1,000, about 570 bytes a Message, nearly three times the Message's own size, so the Record holds more than its Messages; what it keeps beyond them is the first thing to look at if this number needs to come down. 2026-10-25.

### G-B09 — CPU per Turn, at 10, 100 and 1,000 messages

- **Serves:** [Keep subtracting](DESIGNS.md#5-keep-subtracting)
- **Compared with:** the previous release
- **Method:** reference workload (`BenchmarkReferenceTasks`)
- **Last measured:** Gotato's time per Turn with a fake model and a memory store, where it is CPU-bound: 0.045 ms at 10 messages, 0.15 ms at 100, 1.1 ms at 1,000, against 0.038, 0.14 and 0.86 ms before every step was written, in the same measurement (`gotato-workload`, 30 plays). 2026-10-17.

### G-B10 — Startup time

From process start to ready to serve.

- **Serves:** [Keep subtracting](DESIGNS.md#5-keep-subtracting)
- **Compared with:** the previous release. kagent measured 2.7 s for a Go runtime and 18.2 s for a Python one ([Go vs Python runtime](https://kagent.dev/blog/go-vs-python-runtime)).
- **Method:** `BenchmarkStartup` on the reference service: from process start to the first 200 from `/healthz`
- **Last measured:** 25 ms on average over 20 starts. The first start after a build took 0.66 s. 2026-10-16.

### G-B11 — Concurrent Runs per instance before Gotato's time per Turn doubles

- **Serves:** [Keep subtracting](DESIGNS.md#5-keep-subtracting)
- **Compared with:** the previous release
- **Method:** reference workload under load (`gotato-workload -concurrency N`)
- **Last measured:** Between 4 and 8, with fake models on 8 cores: Gotato's time per Turn rises 1.6× at 4 concurrent Runs and 3× at 8. Fake models never wait, so this is the CPU-bound floor; a real model's wait leaves the CPU free for more Runs. 2026-10-16.

### G-B12 — Footprint

Modules and stripped binary of a program that imports the agent, the Runner, a store and the HTTP handler.

- **Serves:** [Keep subtracting](DESIGNS.md#5-keep-subtracting)
- **Compared with:** Google ADK for Go: 128 modules, 14.8 MB
- **Method:** `bench/refservice/footprint.sh` on the reference service, which also imports the gateway
- **Last measured:** 3 modules linked (the service, Gotato and `yaml.v3`; `go list -m all` adds `yaml.v3`'s test dependency), 7.6 MB. Without the gateway, 7.0 MB. 2026-10-16. The 3.8 MB recorded on 2026-10-15 had no script behind it and could not be reproduced.

## Evaluations

Each evaluation needs a person or a real model, so it runs on demand.

### G-E01 — The Recap keeps the facts ⭐

- **Serves:** [The Context is a bounded view](DESIGNS.md#3-the-context-is-a-bounded-view)
- **Measure:** After a compaction, a real model answers questions whose answers appeared only in Tool Results before it, such as an ID, a number or an error code. The share it answers correctly, compared with the same questions before the compaction.
- **Method:** a check on the payments example that Gotato ships and a team runs on demand against its own gateway ([FEATURES.md → G-F06](FEATURES.md#g-f06--compaction-)): `gotato-workload -recap-check -live -gateway-config gateway.yaml`, built on `workload.RunRecapCheck`. It plays three prompts on one Session, asks five questions before and after the compaction (unsaved, with the tools switched off), and prints the share answered. Its scoring is tested on a fake model. Gotato runs no checks against real providers itself.
- **Status:** ⬜ Not measured. The check ships (2026-10-25); no gateway has run it yet, and the model's Recap stays off by default until a team has.

### G-B13 — Time to a first agent

- **Serves:** [Keep subtracting](DESIGNS.md#5-keep-subtracting)
- **Measure:** a developer new to Gotato, from `go get` to an agent answering over HTTP in an existing Go service. Compared with nothing yet; the first measurement sets the reference.
- **Method:** timed sessions with new developers
- **Status:** Not measured.

### G-G06 — Breaking changes are written down

- **Serves:** [Stay inside the boundary](DESIGNS.md#6-stay-inside-the-boundary)
- **Measure:** 100% of breaking changes have a before-and-after entry in [CHANGELOG.md](CHANGELOG.md), and a contract version bump when they touch HTTP or gRPC
- **Method:** review at each release, and an API-compatibility check in CI *(to build, ROADMAP Phase 10)*
- **Status:** ⬜ Not audited.
