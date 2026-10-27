<!-- what-we-build: drafted 2026-10-27 -->
# Gotato — Roadmap

What gets built next, in what order, and how we know each step is done. The roadmap adds no new feature. It schedules what [GOALS.md](GOALS.md) measures, what [FEATURES.md](../FEATURES.md) marks as planned (⬜) or names as a gap (🚧), and the design questions still open, which it places at the start of the phase that needs them. The principles that answer them are in [DESIGNS.md](../DESIGNS.md). Terms are in [GLOSSARY.md](../GLOSSARY.md).

Gotato has made no release yet. The first release is named 0.1.0, and it is cut when every phase below is done, so there are no point releases before it. A phase is done when its exit criteria hold: goals that pass, benchmarks that have a number, decisions written down, and the checks the phase names. No phase has a date.

## The order

The phases, in order, with the design discussion that comes before the store changes:

```text
Phase 1  Measure             the reference workload and the live run
Phase 2  Effect cap          an Agent hides the tools above its highest Effect
Phase 3  Names               the Agent, the live Session and its Record take their glossary names
         ── design discussion: how a Turn is saved, who writes, where messages go (answered) ──
Phase 4  Durable steps       every step is appended as it happens, and every append is fenced
Phase 5  One writer          any instance answers like the only one, with one writer at a time, and resumes any stream
Phase 6  Compaction          the Context stays within the model's window, and a Session that comes back after a pause starts small
Phase 7  MCP, tool search    tools load one by one from a catalog, MCP servers supply tools, a large Tool Result
                             is spilled into the Record, and the counting caps leave the core
Phase 8  Warm Session        a Session stays in memory while in use, is told runtime facts,
                             and takes Steer, Interrupt and Stop from any endpoint
Phase 9  Before release      fake gateways, a local cluster with a chat bot on it
Phase 10 Pruning             what 0.1.0 ships is cut to what a service needs, then the code is made clear
```

Why this order:

- **Measurement comes first,** so each later phase is measured against a baseline. Saving every step adds a write per step, and the benchmarks show what that costs.
- **The Effect cap and the rename depend on nothing else.** The cap adds a setting to the Agent, not a type, so it does not wait for the rename.
- **The rename comes before the store and writer work,** so that code is written with the final names. The live Session and its Record both take their names in Phase 3, so Phase 8 grows the Session without renaming it.
- **The design discussion comes before the store changes.** The store contract that Phase 4 ships already carries the Claims, the Epoch and the Liveness rows that Phase 5 uses, so store adapters change once.
- **Durable steps, one writer and the warm Session each build on the one before.** One writer needs Turns that are appended and fenced, and a warm Session needs a writer to hold it.
- **Compaction comes before the warm Session,** because it does not need it. A cloud Session is compacted often, before its cache goes cold, so that a user who comes back later finds a small Context and the conversation they left. Until the warm Session has an idle timer, the service compacts an idle Session itself: its own timer calls the compact endpoint before the provider's cache expires, and `min_tokens` skips a stretch too short to be worth a model call. The warm Session's timer later calls the same compaction.
- **The Event design is built in Phase 5,** because a stream that resumes on any instance needs an Event number that every instance shares, and the Claim Events are Events of that design.
- **Fault injection and the multi-instance harness come with the phases whose goals they check.** The store conformance suite comes with Phase 4, because it is written against the contract that phase ships.
- **Tools at scale come before the warm Session.** Tool search, the MCP adapter and the spilled results do not need it: while a Run is live the Session already holds a spilled body in memory, a later Run reads it from the Record, and the warm Session (next phase) keeps it across Runs. They also take the counting caps out of the core, which the compaction ceiling and the Run's `ctx` replace, so Compaction has to be built first.
- **The tests a team's own setup would run come after the features:** fake gateways and a local cluster with a chat bot on it. Each team runs its own gateway, so Gotato adapts to the wire APIs and proves it with unit tests along the way, and the fake gateways and the cluster test the whole once the features are in.
- **Pruning comes last,** once every feature is in, and before the release, because until then a removed or renamed export breaks no one. Afterwards each one is a breaking change.

## Phase 1 — Measure ✅

Built.

Builds the tools that most goals need, and gives every benchmark a number. Each item is marked ✅ built, 🚧 started, or ⬜ to build.

- ✅ the reference workload: its tasks and fake models in `internal/reftasks` (`ReferenceTasks`), and a driver in `internal/workload` that runs them through the Runner. The tasks are a short lookup, a 10-Turn conversation, a tool error and its recovery, a support case the model works through in five Turns, and Sessions with 10, 100 and 1,000 messages, run alone and under load;
- ✅ the live run: the same tasks against a real provider, with a fixed model and its price, run on demand with `go run ./internal/cmd/gotato-workload -live`. G-B05 and G-B07 take the model's own time from it. First run on 2026-10-16, against `gpt-6.1-sol`;
- ✅ Events that time each store load and save: the Runner emits `session_loaded` and `session_saved`, and `internal/workload` reads the store's time from them. A Run now loads its Session once, not twice;
- ✅ a startup benchmark, and a build script that measures the footprint, both on a reference service in `bench/refservice`;
- ✅ a test that inspects each Turn's Context through the Go API, from that Turn's own Transcript, and compares its prefix hash with the Turn's `context_built` Event;
- ✅ Context inspection that applies the extensions, says when an auto-compaction is pending, and shows the model options, so its prefix hash matches the request (the G-F05 gap);
- ✅ each failed Run and tool call attributed to its cause: the model, the provider, the tool, the store or Gotato;
- ✅ a prompt cache key per Session, so the live run measures cache hits at all (G-B01).

Every number about a Run is computed from recorded Events and `Usage`, so a team gets the same numbers from its own telemetry ([DESIGNS.md → Every number about a Run comes from Events and `Usage`](../DESIGNS.md#every-number-about-a-run-comes-from-events-and-usage)). Memory, CPU, startup and footprint come from Go benchmarks and the build script.

Exit:

- ✅ G-G03 passes in CI.
- ✅ G-G04 passes on the reference workload in CI, and the live run counts no failure caused by Gotato.
- ✅ Every benchmark but G-B13 has a number.

## Phase 2 — Effect cap ✅

Built.

- ✅ An Agent can name the highest Effect its tools may have, with `gotato.WithMaxEffect` or `service.Agent.MaxEffect`. There is no cap by default.
- ✅ A tool above the cap is hidden from the model, including a tool of a ToolSet, which is never in the index and is never loaded, and a call to it fails as a call to an unknown tool ([FEATURES.md → The Effect cap hides tools](../FEATURES.md#the-effect-cap-hides-tools-and-only-an-agent-sets-it--2026-10-16)).
- ✅ The cap belongs to the Agent: a Session can neither change it nor switch on a hidden tool.
- ✅ `gotato tools list` and `GET /v1/agents` report the cap.

Exit:

- ✅ G-G05 passes.

## Phase 3 — Names ✅

Built.

- ✅ `service.AgentSpec` becomes `service.Agent`.
- ✅ The old `gotato.Agent` becomes `gotato.Session`, the live Session: one concrete type, with the interfaces it was split into removed. The Loop stays a concept ([FEATURES.md → The live Session is `gotato.Session`](../FEATURES.md#the-live-session-is-gotatosession-and-its-record-is-sessionrecord--2026-10-16)).
- ✅ `session.Session`, the stored record, becomes `session.Record`.
- ✅ A live Session's ID is its Session ID, and Events carry it as `session_id`. `agent_start` and `agent_end` become `run_start` and `run_end`.
- ✅ The rename ships as one planned break, with no `// Deprecated:` aliases, and a [CHANGELOG.md](../CHANGELOG.md) entry.

Exit:

- ✅ No exported identifier, Event kind or field carries the old meaning of Agent.
- ✅ G-G06 holds for the break: it has its before-and-after entry in [CHANGELOG.md](../CHANGELOG.md).

## Design discussion ✅

The three store questions are answered as decisions dated 2026-10-16: the first two in DESIGNS.md, and the third with its feature in FEATURES.md. The maintainer approved them on 2026-10-17.

- ✅ How a Turn is saved: [The Record is an append-only log](../DESIGNS.md#the-record-is-an-append-only-log--2026-10-16).
- ✅ How one instance runs a Session at a time, and what happens to a request that reaches another instance: [One writer per Session, claimed in the store](../DESIGNS.md#one-writer-per-session-claimed-in-the-store--2026-10-16) and [Gotato builds no infrastructure, and stays within its boundary](../DESIGNS.md#gotato-builds-no-infrastructure-and-stays-within-its-boundary--2026-10-16).
- ✅ What happens to a message that arrives during a Run: [Messages that arrive during a Run](../FEATURES.md#messages-that-arrive-during-a-run--2026-10-16).

Runtime notices (FEATURES.md G-F29), which follow from the third answer, were designed on 2026-10-18 ([DESIGNS.md → What is not the user's words goes to the model as a notice](../DESIGNS.md#what-is-not-the-users-words-goes-to-the-model-as-a-notice--2026-10-18)), and are built in [Phase 8](#phase-8--warm-session). The warm Session, and the answer for a message that reaches an instance that is not the writer, were designed on 2026-10-25 ([DESIGNS.md → A warm copy is a cache, not a lock](../DESIGNS.md#a-warm-copy-is-a-cache-not-a-lock--2026-10-25), [FEATURES.md → G-F26](../FEATURES.md#when-the-hash-missed-the-answer-names-the-writer--2026-10-25)), and are built in the same phase. The Events on a message's way into a Session were designed with every other Event on 2026-10-18 ([DESIGNS.md → Every step is an Event, and a stored Event is an Entry](../DESIGNS.md#every-step-is-an-event-and-a-stored-event-is-an-entry--2026-10-18)).

Exit: the maintainer approves the three Decisions.

## Phase 4 — Durable steps ✅

Built.

- ✅ The Record becomes an append-only log of facts and log entries ([DESIGNS.md → The Record is an append-only log](../DESIGNS.md#the-record-is-an-append-only-log--2026-10-16) and [The Record keeps every step](../DESIGNS.md#the-record-keeps-every-step-as-facts-and-a-log--2026-10-17)). Every step is appended as it happens, at a position the store never gives twice, so a crash loses at most the step in flight. A reply cut off mid-stream is kept, and each reply records the model that answered it.
- ✅ A compaction appends a Checkpoint, and a load reads the facts from the last one.
- ✅ A Claim is an Entry, so a writer that lost its Claim always writes to a taken position, and the store refuses it. The store contract also carries the Liveness rows that Phase 5 uses, so adapters change once.
- ✅ A store conformance suite in `session/storetest`, written against that contract. An adapter runs it to prove it keeps what Gotato relies on, and the memory and file stores run it too ([FEATURES.md → Adapters prove the store contract](../FEATURES.md#adapters-prove-the-store-contract-with-a-conformance-suite--2026-10-16)).
- ✅ Fault injection: a crash after every write of the reference workload, and a client that disconnects mid-stream on HTTP.

Exit:

- ✅ G-G01 and G-G02 pass.
- ✅ The memory and file stores pass the conformance suite. A test adapter that moves a stale write onto the head fails it, and so does one that lets two copies write one position.
- ✅ A test compacts, appends, reloads and compares the Context with the one before the reload.
- ✅ G-B05, G-B07 and G-B09 show what writing every step costs, compared with a Run that saved once.

## Phase 5 — One writer ✅

Built. The part that keeps two instances from running one Session is built, and so is the drain. The Event design, decided on 2026-10-18, is built too, because stream resume and the Claim Events stand on it, and so are the routes that resume a stream, the load balancer's part and the harness behind a hashing proxy.

- ✅ One writer at a time: an instance claims a Session in the store before it calls the model, and its Claim is valid while its Liveness row is ([DESIGNS.md → One writer per Session](../DESIGNS.md#one-writer-per-session-claimed-in-the-store--2026-10-16)). An instance that cannot show its row was renewed in time writes nothing.
- ✅ Any instance answers: it takes over an idle Session, and waits, up to a bound, for one its live writer is using, then answers 409 with `Retry-After`.
- ✅ The load balancer's part: the HTTP endpoints read the `session-id` header as a check and answer with it when they create a Session, an instance past its hard limit (`MaxActiveSessions`, `--max-sessions`) refuses new Sessions with a 503 marked "not run", and `/readyz` reports a drain and the active Sessions ([FEATURES.md → G-F27](../FEATURES.md#g-f27--service-many-instances-)).
- ✅ Drain in order, on SIGTERM or an endpoint: mark the Liveness row draining, answer 503 on `/readyz`, take no new Sessions, let each Run finish, save and release, then close each stopped Run's stream with a reconnect code ([FEATURES.md → G-F27](../FEATURES.md#g-f27--service-many-instances-)). Another instance still waits for a Session the draining one is using.
- ✅ The Event design ([DESIGNS.md → Every step is an Event, and a stored Event is an Entry](../DESIGNS.md#every-step-is-an-event-and-a-stored-event-is-an-entry--2026-10-18), [FEATURES.md → G-F13](../FEATURES.md#g-f13--events-)): every stored Event is an Entry, numbered by its position and written before it is sent; each kind of Event is a Go type with the names that FEATURES.md lists; a Session sends to Subscriptions it never waits for, and `EventObserver` goes. The HTTP and CLI contracts changed with it, in the contract version change that Phase 3 made. The message Events have their types, and a live Session sends them; the Runner and the endpoints got them in Phase 8, step 3.
- ✅ A stream resumes on any instance from the number of the last Event it saw, with the Record's epoch: `GET /v1/sessions/{id}/stream` (`Last-Event-ID`, `after`). A Run's stream carries each Event's number, and a stream that ends says whether to reconnect and after how long, spread at random ([FEATURES.md → G-F14](../FEATURES.md#g-f14--http-and-streaming-)).
- ✅ Claim Events: `session_claimed`, with why and from which instance, `session_released`, `session_fenced` and `liveness_lapsed`.
- ✅ The multi-instance harness: several instances over one shared store behind a hashing proxy, with requests sent through it and straight to random instances, concurrent messages and an instance killed.
- ✅ Ended Liveness rows are cleaned up: an instance removes its own row when it stops, and the store conformance suite checks that a removed row reads as not alive.

Exit:

- ✅ G-G08 passes for every message an instance accepts.
- ✅ With requests sent to random instances, the harness gets the same Transcripts and Event order as a single instance.
- ✅ A stream cut at every Event of the reference workload resumes equal to an uncut one, over HTTP.
- ✅ A subscriber that stops reading never slows its Session, and catches up from the Record with every stored Event once.
- ✅ G-G06 holds for the Event break: a before-and-after entry in [CHANGELOG.md](../CHANGELOG.md).
- ✅ A drained instance commits its Runs and releases its Claims before it leaves.
- ✅ A test stops an instance's heartbeat and expects its Claims to end and its late appends to fail.
- ✅ A test fills an instance to its hard limit and expects a "not run" refusal, an Event, and no Session activated there.

## Phase 6 — Compaction ✅

A Session can live for weeks, and its user is rarely there: they come back after the provider's cache has expired, and a long Run can outgrow the window. Each compaction closes the history since the last Checkpoint into a Segment that is never rewritten, with the conversation word for word, a line per tool call, and a Recap the Agent's model writes on the cached prefix ([DESIGNS.md → Compaction closes a Segment, before the cache goes cold](../DESIGNS.md#compaction-closes-a-segment-before-the-cache-goes-cold--2026-10-19), [FEATURES.md → G-F06](../FEATURES.md#g-f06--compaction-)). Each item is marked ✅ built or ⬜ to build.

- ✅ Already built: tags and their escaping, the gateway's `context_window`, cache retention and overflow report, ToolSets that stay on, the Record's storage for Segments, the Run on every Message, and the Recap names.
- Each team runs its own gateway, so Gotato adapts to the wire APIs and tests that with unit tests, and runs no checks against real providers. The model's Recap is off by default; a team turns it on once the payments check (G-E01) passes on its own gateway. Fake gateways that play each wire API's quirks are built in Phase 9 (`internal/fakegateway`).
- ✅ Step 1, Segments built by code: a compaction on demand or at the start of a Run closes a Segment, and keeps the ToolSets the Segment used and those the kept tail calls. A user Message or final reply past 2K tokens is cut, and the line that says so names `read_segment`, which reads the rest from step 4. The compact endpoints take `min_tokens`, so a service's idle timer skips a stretch too short to be worth a model call, and a compaction that names no `keep` keeps the latest Run's Turns as its tail, as mid-Run. A Record compacted into one summary before Segments is refused, not migrated. A compaction that fails is dropped, and the Run goes on.
- ✅ Step 2, the model writes the Recap, on the cached prefix: the request is the Context the next call would send plus one plain ask, validated by the five-heading rules, and a reply that fails leaves the Segment without a Recap. It is off by default in the library, the service and the CLI. The payments check of G-E01 ships as `go run ./internal/cmd/gotato-workload -recap-check`. With it, a service can compact its idle Sessions before their cache goes cold.
- ✅ Step 3, past the ceiling and on overflow: compaction before any model call, mid-Run included, through `TurnPreparer`, and once after the provider refuses a request as too long. The ceiling is the window less max(10%, 16K tokens) when the gateway knows the window; the Agent's and the Session's override it. A Turn compacts at most once: still past the ceiling after it ends the Run with `limit_exceeded`, and a second refusal with `context_overflow`. `AutoCompactor` is the first `TurnPreparer`, and `RunPreparer` is gone.
- ✅ Step 4, many Segments: the model reads a Segment back with `read_segment`, page by page, and the oldest Segments merge when they pass their budget, right after a compaction closes a Segment, at the start of a Run, and on an overflow, into the one merged Segment that opens the Context, down to half the budget. The budget is half the ceiling, or 32K tokens with none. Its Recap is written from their Recaps alone by the Agent's model, and by code, as a digest, when the model's Recap is off. On an overflow, the limit the refusal names sizes the merge, and a merge whose Recap fails ends the Run with `context_overflow`. The `long_session` task (2,000 Turns of 20 KB Tool Results, a 128K-token window) plays it.

Exit:

- ✅ A test on a fake model compacts on demand, as a service's idle timer does, then a Run past the ceiling mid-Run, then a request refused as too long. It checks that the Segments before keep their bytes, that a tool call stays with its result in the kept tail, that every instance builds the same request from the Checkpoint, that a Recap that fails leaves a Segment without one, and that a compaction whose write fails leaves the Run going.
- ✅ A test reads a Segment back with `read_segment`, merges the oldest Segments, and checks that every instance offers the same ToolSets after a load, from a Checkpoint or between two.
- ✅ A user Message, a Tool Result, a Recap and a kept assistant reply that hold the start or the end of one of the four tags, `<notice>`, `<segment>`, `<tool_calls>` and `<recap>`, reach the model escaped.
- ✅ G-G07 and G-G09 pass across Runs, reloads, instances and compactions (the `long_session` play, with and without the model's Recap, merges included), and G-G11 passes for every fault it lists.
- ✅ G-G10 passes on the `long_session` task.
- ✅ G-B14's sizes and G-B16's tokens have a number, from the fake model. Their costs and times, and G-B15, need a provider's cache and prices, so they come from a live run on a team's own gateway, which Gotato does not run; the live run measures them whenever a team runs it ([Every release](#every-release-from-the-first-on)).
- ✅ The payments check of G-E01 ships, for a team to run on its own gateway, and the Recap written by the model stays off by default.

## Phase 7 — MCP and tool search ✅

A Session's tools come from many places, and a service that connects MCP servers can offer hundreds of them. The model should see the few it needs, and a Tool Result should never fill the Context on its own. This phase loads tools one by one from a catalog, adds the MCP adapter that supplies them, spills a large Tool Result into the Record, and takes the counting caps out of the core. Designed on 2026-10-25 ([DESIGNS.md → A limit guards a real failure](../DESIGNS.md#a-limit-guards-a-real-failure--2026-10-25), [FEATURES.md → G-F34](../FEATURES.md#g-f34--tool-search-), [G-F12](../FEATURES.md#g-f12--the-mcp-adapter-) and [G-F35](../FEATURES.md#g-f35--large-tool-results-)). Each step is marked ✅ when built; they build on one another, in this order:

- ✅ Step 1, limits that guard a real failure: `MaxTurns`, `MaxToolCalls`, `MaxToolResultBytes` and `RunDeadline` leave the core. A team that wants a Turn or call cap writes a `TurnStopper`, and a Run's deadline is its `ctx`. `service.Agent` keeps one Run deadline, `RunTimeout` (30 minutes by default), that the request's own timeout overrides. `MaxMessageBytes` stays, and `ModelCallDeadline` and `ToolCallDeadline` stay, marked for review. The DESIGNS section about every Run ending is the final form of this step.
- ✅ Step 2, large Tool Results: a Tool Result over one page (about 8K tokens) is spilled into the Record as an Entry of its own, the Context holds a preview and a ref, and the model reads the rest page by page with `read_result`, which pages as `read_segment` does. A load does not read a spilled body until it is asked for. This replaces what `MaxToolResultBytes` did.
- ✅ Step 3, tool search: the unit of loading is one tool. `search_tools` replaces `activate_toolset`, and its description is the catalog. `WithFoldedToolSet` joins `WithToolSet` and `WithActiveToolSet`. A loaded tool stays until the next compaction. `MaxActiveToolSets` is removed, `tool_loaded` and `tool_unloaded` replace `toolset_activated`, and a `ToolSearcher` lets a team plug in its own search. The tool list changes when a tool loads (the list form); the native form is step 4.
- ✅ Step 4, native deferred loading: the tools the model loads keep the cache. On Anthropic Messages and OpenAI's Responses API the tool list stays byte for byte the same while tools load, chosen by the gateway key `deferred_tools`; Chat Completions changes the tool list.
- ✅ Step 5, the MCP adapter `adapter/mcp`: a Go module of its own, on the official MCP Go SDK (v1.8.0). One MCP server is one ToolSet, streamable HTTP and stdio are the transports, and a tool's annotations become its Effect. It comes with a change to the core: a ToolSet that fails to resolve is skipped with a warning instead of failing `NewSession`, and ToolSets resolve again at the start of every Run, so a server's `list_changed` takes effect at the next Run, a loaded tool that vanished is unloaded (`why: gone`), a recovered ToolSet comes back, and the ToolSets resolve at the same time, so a Run waits for the slowest server ([FEATURES.md → G-F10](../FEATURES.md#g-f10--tool-sets-)).

Exit:

- ✅ A test on a fake model plays a Run that no `TurnStopper` stops and that settles by its model's reply, by its `ctx`, by a `TurnStopper` and by the compaction ceiling, and finds no counting cap in the core.
- ✅ A test spills a Tool Result of 1 MB, checks that the Context holds only the preview and the ref, that a load reads no spilled body, that `read_result` pages through the whole body, and that every instance builds the same request from the same Record (G-G09).
- ✅ A test loads tools by name and by query on a fake model, checks that a call to an unloaded catalog tool fails with a Tool Result that says it is loaded now, that a compaction keeps the tools called in the closed Segment or the kept tail and unloads the rest, that every instance offers the same tools after a load, that the Effect cap keeps a tool out of the index, and that a tool switched off for a Session is never loaded.
- ✅ A test of each wire API's form of deferred loading checks the request bytes of two consecutive Turns that load a tool: the same tool list in the native form, a changed one in the list form (G-G07).
- ✅ The MCP adapter's tests run against an in-process MCP server over both transports, and check the Effect from annotations, the slimmed schemas, the content mapping and a server's `list_changed` at the next Run. Its module passes its own `go test`, `go vet` and `golangci-lint`.
- ✅ G-G07, G-G09 and G-G10 keep passing.

## Phase 8 — Warm Session ✅

A Session stays in memory while it is in use, so a Run in progress can be steered from any endpoint, and the runtime can tell the model facts that are not the user's words. The store stays the truth, and a warm copy is a cache, never a lock. Designed on 2026-10-25 ([DESIGNS.md → A warm copy is a cache, not a lock](../DESIGNS.md#a-warm-copy-is-a-cache-not-a-lock--2026-10-25), [FEATURES.md → G-F27](../FEATURES.md#g-f27--service-many-instances-) and [G-F26](../FEATURES.md#g-f26--steering-a-run-)). The steps build on one another, in this order:

- ✅ Step 1, the lifecycle: after a Run the Session stays in memory, and leaves after its TTL (60 idle minutes, per Agent). Before each Run on a warm copy the instance compares the Record's head in the store with the copy's: unchanged, it uses the copy; changed, it reloads. The copy holds no Claim while idle, so another instance can take the Session over, and the old copy is dropped. A warm Session that idles is compacted by default shortly before its prompt cache expires or it leaves memory, by its own timer, with the same compaction a service's timer calls in Phase 6; a message that arrives cancels it and is served first. A setting turns the idle compaction off. What stays warm in this step is the Session's Record and its Writer, held by the Runner, which takes a clock as a setting so that tests wait for no TTL. The Runner still builds a live `gotato.Session` over the copy for each Run, since nothing reaches a Session between Runs before step 3.
- ✅ Step 2, the memory limit: warm Sessions count toward `MaxActiveSessions`. At the limit the instance evicts the longest-idle warm Session first, and refuses a Run as not run (`overloaded`) only when every live Session is running a Run.
- ✅ Step 3, messages from any endpoint: Steer, FollowUp, Interrupt and Stop reach a running Session through the Go API (`Runner.Send`, `Runner.CancelSession`), HTTP (`POST /v1/sessions/{id}/messages`) and the CLI (`session message`, `session cancel`), each taking effect in the writer's own process, and a message that arrives after a Run has ended starts a new Run. This closes the G-F26 gap; notices join the same routes in step 4. `Interrupt`, which the core did not have yet, was built here: it cancels the model call in flight, keeps the text it had sent as a Cut-off reply, and starts the next Turn with the message. The Runner holds no queue between Runs, since a message that finds no Run starts one. It comes with:
  - the load balancer's hash first, and, for a caller that cannot route by Session, an advertised address in each instance's Liveness row (`Config.Address`, `--advertise`; optional) and a new error, `not_writer`, marked not run and retryable, that names the writer's address when a message reaches another instance during a Run;
  - forwarding to that address by the HTTP endpoints (`httpapi.WithForwarding`, `--forward`), as a setting a team opts in to; a forwarded request is marked and never forwarded again. An embedding service forwards with its own client. Gotato builds no network between instances itself;
  - the message Events (`message_queued`, `message_committed`, `message_dropped`, `message_rejected` and `abort_requested`) reaching the Runner and every endpoint, written through the Run's Writer. A message queued as its Run settles runs in a Run that follows it, on the same instance, in the order it was accepted; it is dropped, with a reason in its Event, only when the Run was stopped, the instance drains or the writer failed. 
- ✅ Step 4, runtime notices (G-F29), designed on 2026-10-18: a notice is a Message of its own kind (`gotato.RoleNotice`), waits in the queue that holds a Steer, and goes in right before the next model call. It starts no Round and no Run. The output-limit continuation is a `continue` notice, and a service sends its own through `Session.Notice`, `Runner.Notice`, HTTP (`POST /v1/sessions/{id}/notices`) and the CLI (`session notice`), with the routing of step 3 (`not_writer`, forwarding). A `time_away` notice joins them: a message that starts a Run after the Session was idle longer than a threshold (30 minutes by default, `Agent.TimeAwayAfter`) is followed by how long it has been and the current time. It comes with:
  - the notices kept in the Record (`notice_queued`, `notice_committed`, `Record.PendingNotices`, `Summary.Notices`), so a notice sent between Runs, or left by a Run that settled or was stopped, outlives its instance and goes in after the prompt of the Session's next Run on any instance;
  - the Session's last busy time kept in the Summary (`Summary.LastRunEnd`), so a Session loaded from its facts knows how long it was idle;
  - tagged text on all three wire APIs: a text block in the adjacent user turn on Anthropic Messages, a user item on the Responses API, a user message on Chat Completions; a Segment keeps a notice from a service word for word and leaves Gotato's own out.
- ✅ Step 5, a stream cut after some output (G-F31): the gateway sends the same request again within `max_retries`, with a `model_retried` Event, and the reply cut off stays in the Record as a log.
- `gotato.Session`, which Phase 3 named, keeps its name. The warm copy, its TTL and its timers belong to the Runner, because they need the store's head and a clock; the live Session grows what step 3 needs, its queue held between Runs.

Exit:

- ✅ G-G08 passes with messages that arrive mid-Run (`TestMessagesMidRunLandOnceOnTheWriter`, `TestManyInstancesOneWriterWithMessages`).
- ✅ A test steers, interrupts and stops a Run on a fake model, and checks where each message lands in the Transcript (`service/messages_test.go`; the core's `interrupt_test.go`).
- ✅ A test sends a message for a Run to an instance that is not its writer, and gets `not_writer`, marked not run and retryable, with the writer's advertised address; with forwarding on, the endpoint sends it to that address and the Run takes it. With no Run going, the same instance takes the Session over and starts a new Run. It runs over the Go API, HTTP (409, `Gotato-Writer`) and the CLI (exit 1, `not_writer`).
- ✅ A test sends notices during a Run and between Runs, and checks that each lands right before a model call, after the Tool Results or the next prompt, starts no Round and no Run, and reaches each wire API (tagged text, the one form): `TestNoticeDuringARunLandsAfterTheToolResults`, `TestNoticeBetweenRunsWaitsForThePrompt` and `TestNoticeNeverKeepsARunGoing` in the core, `TestANoticeReachesARunAtItsNextModelCall`, `TestANoticeBetweenRunsWaitsForTheNextRun` and `TestANoticeBetweenRunsOutlivesItsInstance` in `service`, `gateway/notice_test.go`, and the HTTP and CLI tests, `not_writer` included.
- ✅ A test brings a message to a Session idle longer than the threshold, and checks that a `time_away` notice with the elapsed time and the current time follows it, and that a message inside the threshold gets none (`TestATimeAwayNoticeFollowsAMessageAfterALongAbsence`, on an evicted Session too). `TestOutputLimitContinuationIsANoticeAndStartsNoRound` checks that the output-limit continuation is a notice and starts no Round.
- ✅ A test on a fake model lets a warm Session idle, and checks that it is compacted before its cache expires and leaves memory at its TTL.
- ✅ A test writes a warm Session's Record from another instance, and checks that the first instance, at its next Run, finds the head changed, reloads, and runs on the new Record.
- ✅ A test fills an instance with warm Sessions to `MaxActiveSessions`, and checks that a new Run evicts the longest-idle one and runs, and that with every live Session running it is refused as not run.
- ✅ G-B07 shows the load a warm Session saves, and G-B08 the heap it holds, both compared with Phase 1.

## Phase 9 — Before the release ✅

The last phase tests Gotato the way a team would run it: against gateways that behave like the providers, and on several instances in a local cluster.

- ✅ **Fake gateways.** `internal/fakegateway` is one test-only server that speaks each wire API (Claude's Messages, OpenAI's Responses, Chat Completions) and plays their quirks on demand, from a directive in the last user text or the `X-Fake-Quirk` header: a refusal as too long in each provider's words and fields, before any output and mid-stream; a stream cut after text and in a tool call's arguments; cached-token usage; a reasoning replay, which it refuses to see dropped; a system or `developer` message mid-conversation, accepted or refused as the provider does; the native form of deferred tools on Claude's Messages (`defer_loading` and `tool_reference`) and OpenAI's Responses (client `tool_search`), where the `load` directive makes the model search, load and call a tool; a slow reply, for the cluster. It records every request, and `internal/fakegateway/cmd/fakegateway` serves it on a port. The gateway's adaptation and compaction's overflow path run against it.
- ✅ **A local cluster.** Several instances of the chat bot below on one local Kubernetes cluster (kind), sharing a file store, with the fake gateway as the model and a fake chat platform in front, so a Run stays in flight. The scenarios, each run on a cold cluster and checked afterwards against the Records: a rolling deploy while Runs are in flight, a writer killed mid-Run (deleted, and killed with SIGKILL), scaling from 3 to 1 to 5 instances, bursts of messages on one Session from several instances, and events delivered again after a late acknowledgement, which it measures; and, with an MCP server on it, an MCP server killed, a bot deployed and the MCP server rolled gracefully while calls are in flight, and a tool loaded by search still loaded after a takeover. The cluster files, the load driver and the fake chat platform live in [`examples/larkbot`](../examples/larkbot), because they belong to the application.
- ✅ **A chat bot.** An example the maintainer builds, in [`examples/larkbot`](../examples/larkbot), a module of its own: a Lark chat bot that embeds Gotato, one Session per message thread, and runs on the local cluster behind the fake gateway. It is an application and not a client Gotato ships ([G-N18](../FEATURES.md#g-n18--no-ready-made-clients)). A chat platform delivers each event to one instance at random, so the bot checks from the caller's side what [FEATURES.md → G-F26](../FEATURES.md#g-f26--steering-a-run-) says about a caller that cannot route by Session: it forwards a message to the writer, and retries one that was not run, with backoff and in order per Session. What it leaves to the application, with the numbers from the cluster, is how to drop an event delivered again.

Exit:

- ✅ Each quirk the fake gateways play has a test, and an overflow in each provider's form compacts and sends again. An overflow in the middle of a reply ends the Run with `context_overflow` instead, and the test pins that.
- ✅ G-G08 and G-G11 pass on the local cluster through a rolling deploy and a killed writer.
- ✅ The chat bot, with the fake gateway behind it, answers every message once through the same rolling deploy (25 of 25 messages, none twice).

## Phase 10 — Pruning

Before the first release, what Gotato ships is cut to what a service embedding an agent needs, and then the code that is left is made clear. No feature is added during the phase. Each step takes the same course: a read-only inventory, a list the maintainer approves item by item, small branches that carry it out, and the full checks. A step starts once the one before it has landed, because it looks only at what is left. A step's results are what the repository records: the [CHANGELOG.md](../CHANGELOG.md) entry of each break, and the rows of [FEATURES.md](../FEATURES.md) it changes.

- ✅ **1. Surfaces.** Every package, module and binary outside the core is sorted into one of four kinds: the product, which a service imports or runs; our own tests; support for an example; or removed. What ships is the core in `gotato/`, `session` with `session/storetest`, `modelctx`, `gateway`, `service` with `service/httpapi`, `testkit`, the CLI and `adapter/mcp`. Our own test tools live in `internal/`, and the example keeps its cluster. A directory's name is its package's name, so no import needs an alias. There is no gRPC adapter ([G-N27](../FEATURES.md#g-n27--no-grpc-adapter)) and no separate tool registry.
- ⬜ **2. Code with no gain.** In the product that is left: an abstraction with one implementation, an option that only tests set, a layer that only forwards, a feature that only tests use, and anything whose upkeep outweighs what it gives.
- ⬜ **3. The exported API.** An export used only inside its package becomes unexported, and two ways to do one thing become one. Every module that uses Gotato still builds, and each break has its entry in [CHANGELOG.md](../CHANGELOG.md).
- ⬜ **4. Leftovers.** Dead code, stale comments and stray directories, and every document checked against the code, so that none names what was removed.
- ⬜ **5. Clear code.** Structure (files split, code in the right package), refactoring (duplicate logic, long functions) and doc comments that say why. A change inside a package changes no behavior and no test; one that touches the exported API is handled as in step 3. Tests are pruned with the code they cover.
- ⬜ **6. The release.** The maintainer approves the reviewed documents. The MCP adapter is a module of its own that reaches the root through `replace`, which `go get` ignores, so the root is tagged `v0.1.0` first, the adapter then requires that version and is tagged with its directory (`adapter/mcp/v0.1.0`). A module outside the repository, with no `replace`, follows the README from nothing to a running agent with an MCP ToolSet. Release notes name what was never checked against a real provider.

Exit:

- Every step's list is approved by the maintainer and carried out.
- `deadcode` and the linters report nothing unused, and no document names a removed package, type or feature.
- The module outside the repository builds and runs against the tagged versions.

## Every release, from the first on

- Every goal that passed keeps passing.
- Every benchmark but G-B13 is measured again, and compared with the previous release (for the first release, with the numbers of Phase 1) and with what its row in GOALS.md names.
- G-G06 is checked: each break has a before-and-after entry in [CHANGELOG.md](../CHANGELOG.md).

## Not scheduled

- G-B13, which needs developers new to Gotato;
- the ideas in [FEATURES.md → Later](../FEATURES.md#later);
- the other items in [TODO.md](TODO.md), which are fixed or settled outside the phases.
