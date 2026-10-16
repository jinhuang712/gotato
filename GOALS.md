<!-- what-we-build: drafted 2026-10-16 -->
# Gotato — Goals

GOALS holds two kinds of number. A **goal** is a check Gotato passes or fails, and its number is fixed by its nature: 0 failures, or 100%. A **benchmark** is a number Gotato tracks. Alone it means nothing, so each one names what it is compared with: the previous release, the model's own time, or another stack.

Every number is measured on this repository's own workloads:

- the **reference workload**: a few scripted tasks run in CI with fake models (a short lookup, a 10-Turn conversation, a tool error and its recovery, and Sessions with 10, 100 and 1,000 messages), from `testkit.ReferenceTasks`, played through a Runner by `service/workload`;
- the **live run**: the same tasks against a real provider with a fixed model and its price, run on demand with `gotato-workload -live`, because CI has no provider credentials;
- **fault injection** and a **multi-instance harness** for the checks that need a crash or a second instance.

A method marked *(to build)* does not exist yet; building it is roadmap work. Every number below can also be computed from Events and `Usage` alone, so a team gets the same numbers from its own production telemetry ([DESIGNS.md → Events and telemetry](DESIGNS.md#events-and-telemetry)).

## Goals

| # | Goal | Want it serves | Measure | Method | Status |
|---|---|---|---|---|---|
| G-G01 | **Session reconstructable** | No lost conversations | After a crash at any step of a Run, the Session loaded from the store equals the Session at its last committed Turn, and the Context rebuilt from it has the same prefix hash, in 100% of crash points | fault injection at every step of the reference workload *(to build)* | ⬜ Today a crash loses the whole Run. |
| G-G02 | **No silent stream cutoffs** | Common endpoints | 100% of streams end with a final event. 100% of replies cut by the output limit say so. A client that disconnects mid-stream still gets its Run settled and saved. | fault injection on the HTTP and gRPC streams *(to build)* | ⬜ The final event exists and output-limit cutoffs are reported. Disconnects are not tested. |
| G-G03 | **Context fidelity** | You see what the model sees | In 100% of Turns, the Context that inspection shows has the same prefix hash as the request the Turn sends | the reference workload compares an inspection of each Turn's Transcript with the Turn's `context_built` Event (`TestReferenceWorkload`) | ✅ Passes, 2026-10-16. |
| G-G04 | **No failures caused by Gotato** | "Stable", from the tagline | 0 Runs fail because of Gotato or the store, and 0 tool calls fail because of Gotato. Every failed tool call returns its reason to the model. | the reference workload and the live run, with each failure attributed to its cause | ⬜ 0 on the reference workload in CI, 2026-10-16. The live run has not been run. |
| G-G05 | **Effect cap holds** | Safe tools without a sandbox | 0 tool calls above an Agent's Effect cap run | a test that offers every Effect level to Agents with every cap *(to build)* | ⬜ The cap is not built. |
| G-G06 | **Breaking changes are written down** | Stable contracts | 100% of breaking changes have a before-and-after entry in [CHANGELOG.md](CHANGELOG.md), and a contract version bump when they touch HTTP or gRPC | review at each release | ⬜ Not audited. |
| G-G07 | **The cacheable prefix stays stable** | Idle costs almost nothing | Consecutive Turns that only append keep the same prefix hash and the same cache breakpoints | `TestPrefixHashStableAcrossAppendOnlyTurns` | ✅ Passes, 2026-10-15. |
| G-G08 | **One owner across instances** | One pod at a time | With several instances, concurrent messages and random instance kills, every message lands in exactly one Turn: 0 lost, duplicated or reordered Turns, and 0 model calls by an instance that does not own the Session | multi-instance harness over one shared store *(to build)* | ⬜ The lock is local to one process. |

## Benchmarks

| # | Benchmark | Compared with | Method | Last measured |
|---|---|---|---|---|
| G-B01 | **Cache hit rate**: cached input tokens ÷ input tokens, from Turn 2 on | the previous release, and the uncached price: Anthropic charges cached input at 0.1× the base price or less ([Prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)) | live run | Not measured. `Usage` records cache reads and writes. |
| G-B02 | **Token cost per task**: (input + output tokens) × price, per reference task | the previous release, with the same model and task | live run | Not measured. |
| G-B03 | **Tool-call success rate**, split by cause: the model's arguments, the tool, Gotato | the previous release. Only Gotato's share is a goal (G-G04); the rest belongs to the model and the tools. | live run | Not measured. |
| G-B04 | **Error rate**, split by cause: the model, the provider after retries, the store, Gotato | the previous release. Only Gotato's and the store's share is a goal (G-G04). | live run | Not measured. |
| G-B05 | **Gotato's time per Turn**: building the Context, assembling the request, saving | the model's own response time on the same Turn, as a share, and the previous release | reference workload (`BenchmarkReferenceTasks`) for Gotato's time, and the live run for the model's | With a fake model and a memory store: 0.03 ms per Turn for a lookup, 0.9 ms with 1,000 messages. A file store adds about 8 ms per Run to save, most of it fsync. Apple M5 Pro, 2026-10-16. The share needs the live run. |
| G-B06 | **Time added before the first token** | the provider's own time to first token | live run | Not measured. |
| G-B07 | **Cold-load time**: loading a Session from the store before its Turn | the model's own response time, as a share | reference workload, with 10, 100 and 1,000 messages | From a file store: 0.1 ms with 10 messages, 0.3 ms with 100, 1.8 ms with 1,000. Apple M5 Pro, 2026-10-16. The share needs the live run. |
| G-B08 | **Memory per task**: peak heap per Run, and heap per Session in memory | the previous release, and a coding-agent session's 1 GiB starting point ([Hosting the Agent SDK](https://code.claude.com/docs/en/agent-sdk/hosting)) | `BenchmarkAgentFootprint` | About 210 KB with 100 KB of history; 5–7 KB with none. 2026-10-15. |
| G-B09 | **CPU per Turn**, at 10, 100 and 1,000 messages | the previous release | reference workload (`BenchmarkReferenceTasks`) | Gotato's time per Turn with a fake model and a memory store, where it is CPU-bound: 0.04 ms at 10 messages, 0.14 ms at 100, 0.9 ms at 1,000. Apple M5 Pro, 2026-10-16. |
| G-B10 | **Startup time**: from process start to ready to serve | the previous release. kagent measured 2.7 s for a Go runtime and 18.2 s for a Python one ([Go vs Python runtime](https://kagent.dev/blog/go-vs-python-runtime)). | a startup benchmark *(to build)* | Not measured. |
| G-B11 | **Concurrent Runs per instance** before Gotato's time per Turn doubles | the previous release | reference workload under load (`gotato-workload -concurrency N`) | Not measured. |
| G-B12 | **Footprint**: modules and stripped binary of a program that imports the agent, the Runner, a store and the HTTP handler | Google ADK for Go: 128 modules, 14.8 MB | a build script *(to build)* | 3 modules, 3.8 MB. 2026-10-15, Apple M3. |
| G-B13 | **Time to a first agent**: a developer new to Gotato, from `go get` to an agent answering over HTTP in an existing Go service | nothing yet; the first measurement sets the reference | timed sessions with new developers | Not measured. |
