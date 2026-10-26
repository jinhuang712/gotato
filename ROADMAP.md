<!-- what-we-build: drafted 2026-10-26 -->
# Gotato — Roadmap

What gets built next, in what order, and how we know each step is done. The roadmap adds no new feature. It schedules what [GOALS.md](GOALS.md) measures, what [FEATURES.md](FEATURES.md) marks as planned (⬜) or names as a gap (🚧), and the design questions still open, which it places at the start of the phase that needs them or keeps under [Waiting for design](#waiting-for-design). The principles that answer them are in [DESIGNS.md](DESIGNS.md). Terms are in [GLOSSARY.md](GLOSSARY.md).

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
Phase 9  Large results       the model queries its Session's Record with a read-only bash subset, with no machine
Phase 10 Before release      fake gateways, a local cluster with a chat bot on it, and CI that flags a removed or changed exported identifier
         ── waiting for design: changes that keep the cache ──
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
- **The reader of large results comes after the warm Session and before the release.** Phase 7 spills a large Tool Result and lets the model page through it. The `bash` tool needs only the Record, so it waits for no other phase; it is placed here so that the chat bot of Phase 10 can show what models do with it, and its command list is reviewed there.
- **The tests a team's own setup would run come last:** fake gateways, a local cluster with a chat bot on it, and the compatibility check. Each team runs its own gateway, so Gotato adapts to the wire APIs and proves it with unit tests along the way, and the fake gateways and the cluster test the whole once the features are in. The compatibility check waits for mass testing, when many callers depend on the exported API.

## Phase 1 — Measure ✅

Built.

Builds the tools that most goals need, and gives every benchmark a number. Each item is marked ✅ built, 🚧 started, or ⬜ to build.

- ✅ the reference workload: its tasks and fake models in `testkit` (`ReferenceTasks`), and a driver beside `service` (`service/workload`) that runs them through the Runner. The tasks are a short lookup, a 10-Turn conversation, a tool error and its recovery, a support case the model works through in five Turns, and Sessions with 10, 100 and 1,000 messages, run alone and under load;
- ✅ the live run: the same tasks against a real provider, with a fixed model and its price, run on demand with `gotato-workload -live`. G-B05 and G-B07 take the model's own time from it. First run on 2026-10-16, against `gpt-6.1-sol`;
- ✅ Events that time each store load and save: the Runner emits `session_loaded` and `session_saved`, and `service/workload` reads the store's time from them. A Run now loads its Session once, not twice;
- ✅ a startup benchmark, and a build script that measures the footprint, both on a reference service in `bench/refservice`;
- ✅ a test that inspects each Turn's Context through the Go API, from that Turn's own Transcript, and compares its prefix hash with the Turn's `context_built` Event;
- ✅ Context inspection that applies the extensions, says when an auto-compaction is pending, and shows the model options, so its prefix hash matches the request (the G-F05 gap);
- ✅ each failed Run and tool call attributed to its cause: the model, the provider, the tool, the store or Gotato;
- ✅ a prompt cache key per Session, so the live run measures cache hits at all (G-B01).

Every number about a Run is computed from recorded Events and `Usage`, so a team gets the same numbers from its own telemetry ([DESIGNS.md → Every number about a Run comes from Events and `Usage`](DESIGNS.md#every-number-about-a-run-comes-from-events-and-usage)). Memory, CPU, startup and footprint come from Go benchmarks and the build script.

Exit:

- ✅ G-G03 passes in CI.
- ✅ G-G04 passes on the reference workload in CI, and the live run counts no failure caused by Gotato.
- ✅ Every benchmark but G-B13 has a number.

## Phase 2 — Effect cap ✅

Built.

- ✅ An Agent can name the highest Effect its tools may have, with `gotato.WithMaxEffect` or `service.Agent.MaxEffect`. There is no cap by default.
- ✅ A tool above the cap is hidden from the model, including a tool of a ToolSet, which is never in the index and is never loaded, and a call to it fails as a call to an unknown tool ([FEATURES.md → The Effect cap hides tools](FEATURES.md#the-effect-cap-hides-tools-and-only-an-agent-sets-it--2026-10-16)).
- ✅ The cap belongs to the Agent: a Session can neither change it nor switch on a hidden tool.
- ✅ `gotato tools list`, `GET /v1/agents` and the gRPC `Agents` call report the cap.

Exit:

- ✅ G-G05 passes.

## Phase 3 — Names ✅

Built.

- ✅ `service.AgentSpec` becomes `service.Agent`.
- ✅ The old `gotato.Agent` becomes `gotato.Session`, the live Session: one concrete type, with the interfaces it was split into removed. The Loop stays a concept ([FEATURES.md → The live Session is `gotato.Session`](FEATURES.md#the-live-session-is-gotatosession-and-its-record-is-sessionrecord--2026-10-16)).
- ✅ `session.Session`, the stored record, becomes `session.Record`.
- ✅ A live Session's ID is its Session ID, and Events carry it as `session_id`. `agent_start` and `agent_end` become `run_start` and `run_end`.
- ✅ The rename ships as one planned break, with no `// Deprecated:` aliases, and a [CHANGELOG.md](CHANGELOG.md) entry.

Exit:

- ✅ No exported identifier, Event kind or field carries the old meaning of Agent.
- ✅ G-G06 holds for the break: the HTTP and gRPC contract version is `"4"`, and the CLI contract version is `"2"`.

## Design discussion ✅

The three store questions are answered as decisions dated 2026-10-16: the first two in DESIGNS.md, and the third with its feature in FEATURES.md. The maintainer approved them on 2026-10-17.

- ✅ How a Turn is saved: [The Record is an append-only log](DESIGNS.md#the-record-is-an-append-only-log--2026-10-16).
- ✅ How one instance runs a Session at a time, and what happens to a request that reaches another instance: [One writer per Session, claimed in the store](DESIGNS.md#one-writer-per-session-claimed-in-the-store--2026-10-16) and [Gotato builds no infrastructure, and stays within its boundary](DESIGNS.md#gotato-builds-no-infrastructure-and-stays-within-its-boundary--2026-10-16).
- ✅ What happens to a message that arrives during a Run: [Messages that arrive during a Run](FEATURES.md#messages-that-arrive-during-a-run--2026-10-16).

Runtime notices (FEATURES.md G-F29), which follow from the third answer, were designed on 2026-10-18 ([DESIGNS.md → What is not the user's words goes to the model as a notice](DESIGNS.md#what-is-not-the-users-words-goes-to-the-model-as-a-notice--2026-10-18)), and are built in [Phase 8](#phase-8--warm-session). The warm Session, and the answer for a message that reaches an instance that is not the writer, were designed on 2026-10-25 ([DESIGNS.md → A warm copy is a cache, not a lock](DESIGNS.md#a-warm-copy-is-a-cache-not-a-lock--2026-10-25), [FEATURES.md → G-F26](FEATURES.md#when-the-hash-missed-the-answer-names-the-writer--2026-10-25)), and are built in the same phase. The Events on a message's way into a Session were designed with every other Event on 2026-10-18 ([DESIGNS.md → Every step is an Event, and a stored Event is an Entry](DESIGNS.md#every-step-is-an-event-and-a-stored-event-is-an-entry--2026-10-18)).

Exit: the maintainer approves the three Decisions.

## Phase 4 — Durable steps ✅

Built.

- ✅ The Record becomes an append-only log of facts and log entries ([DESIGNS.md → The Record is an append-only log](DESIGNS.md#the-record-is-an-append-only-log--2026-10-16) and [The Record keeps every step](DESIGNS.md#the-record-keeps-every-step-as-facts-and-a-log--2026-10-17)). Every step is appended as it happens, at a position the store never gives twice, so a crash loses at most the step in flight. A reply cut off mid-stream is kept, and each reply records the model that answered it.
- ✅ A compaction appends a Checkpoint, and a load reads the facts from the last one.
- ✅ A Claim is an Entry, so a writer that lost its Claim always writes to a taken position, and the store refuses it. The store contract also carries the Liveness rows that Phase 5 uses, so adapters change once.
- ✅ A store conformance suite in `testkit/storetest`, written against that contract. An adapter runs it to prove it keeps what Gotato relies on, and the memory and file stores run it too ([FEATURES.md → Adapters prove the store contract](FEATURES.md#adapters-prove-the-store-contract-with-a-conformance-suite--2026-10-16)).
- ✅ Fault injection: a crash after every write of the reference workload, and a client that disconnects mid-stream on HTTP and gRPC.

Exit:

- ✅ G-G01 and G-G02 pass.
- ✅ The memory and file stores pass the conformance suite. A test adapter that moves a stale write onto the head fails it, and so does one that lets two copies write one position.
- ✅ A test compacts, appends, reloads and compares the Context with the one before the reload.
- ✅ G-B05, G-B07 and G-B09 show what writing every step costs, compared with a Run that saved once.

## Phase 5 — One writer ✅

Built. The part that keeps two instances from running one Session is built, and so is the drain. The Event design, decided on 2026-10-18, is built too, because stream resume and the Claim Events stand on it, and so are the routes that resume a stream, the load balancer's part and the harness behind a hashing proxy.

- ✅ One writer at a time: an instance claims a Session in the store before it calls the model, and its Claim is valid while its Liveness row is ([DESIGNS.md → One writer per Session](DESIGNS.md#one-writer-per-session-claimed-in-the-store--2026-10-16)). An instance that cannot show its row was renewed in time writes nothing.
- ✅ Any instance answers: it takes over an idle Session, and waits, up to a bound, for one its live writer is using, then answers 409 with `Retry-After`.
- ✅ The load balancer's part: the HTTP and gRPC endpoints read the `session-id` header as a check and answer with it when they create a Session, an instance past its hard limit (`MaxActiveSessions`, `--max-sessions`) refuses new Sessions with a 503 marked "not run", and `/readyz` reports a drain and the active Sessions ([FEATURES.md → G-F27](FEATURES.md#g-f27--service-many-instances-)).
- ✅ Drain in order, on SIGTERM or an endpoint: mark the Liveness row draining, answer 503 on `/readyz`, take no new Sessions, let each Run finish, save and release, then close each stopped Run's stream with a reconnect code ([FEATURES.md → G-F27](FEATURES.md#g-f27--service-many-instances-)). Another instance still waits for a Session the draining one is using.
- ✅ The Event design ([DESIGNS.md → Every step is an Event, and a stored Event is an Entry](DESIGNS.md#every-step-is-an-event-and-a-stored-event-is-an-entry--2026-10-18), [FEATURES.md → G-F13](FEATURES.md#g-f13--events-)): every stored Event is an Entry, numbered by its position and written before it is sent; each kind of Event is a Go type with the names that FEATURES.md lists; a Session sends to Subscriptions it never waits for, and `EventObserver` goes. The HTTP, gRPC and CLI contracts changed with it, in the contract version change that Phase 3 made. The message Events have their types, and a live Session sends them; the Runner and the endpoints got them in Phase 8, step 3.
- ✅ A stream resumes on any instance from the number of the last Event it saw, with the Record's epoch: `GET /v1/sessions/{id}/stream` (`Last-Event-ID`, `after`) and gRPC `StreamSession`. A Run's stream carries each Event's number, and a stream that ends says whether to reconnect and after how long, spread at random ([FEATURES.md → G-F14](FEATURES.md#g-f14--http-grpc-and-streaming-)).
- ✅ Claim Events: `session_claimed`, with why and from which instance, `session_released`, `session_fenced` and `liveness_lapsed`.
- ✅ The multi-instance harness: several instances over one shared store behind a hashing proxy, with requests sent through it and straight to random instances, concurrent messages and an instance killed.
- ✅ Ended Liveness rows are cleaned up: an instance removes its own row when it stops, and the store conformance suite checks that a removed row reads as not alive.

Exit:

- ✅ G-G08 passes for every message an instance accepts.
- ✅ With requests sent to random instances, the harness gets the same Transcripts and Event order as a single instance.
- ✅ A stream cut at every Event of the reference workload resumes equal to an uncut one, over HTTP and over gRPC.
- ✅ A subscriber that stops reading never slows its Session, and catches up from the Record with every stored Event once.
- ✅ G-G06 holds for the Event break: a before-and-after entry in [CHANGELOG.md](CHANGELOG.md), under the contract version change that Phase 3 made.
- ✅ A drained instance commits its Runs and releases its Claims before it leaves.
- ✅ A test stops an instance's heartbeat and expects its Claims to end and its late appends to fail.
- ✅ A test fills an instance to its hard limit and expects a "not run" refusal, an Event, and no Session activated there.

## Phase 6 — Compaction ✅

A Session can live for weeks, and its user is rarely there: they come back after the provider's cache has expired, and a long Run can outgrow the window. Each compaction closes the history since the last Checkpoint into a Segment that is never rewritten, with the conversation word for word, a line per tool call, and a Recap the Agent's model writes on the cached prefix ([DESIGNS.md → Compaction closes a Segment, before the cache goes cold](DESIGNS.md#compaction-closes-a-segment-before-the-cache-goes-cold--2026-10-19), [FEATURES.md → G-F06](FEATURES.md#g-f06--compaction-)). Each item is marked ✅ built or ⬜ to build.

- ✅ Already built: tags and their escaping, the gateway's `context_window`, cache retention and overflow report, ToolSets that stay on, the Record's storage for Segments, the Run on every Message, and the Recap names.
- Each team runs its own gateway, so Gotato adapts to the wire APIs and tests that with unit tests, and runs no checks against real providers. The model's Recap is off by default; a team turns it on once the payments check (G-E01) passes on its own gateway. Fake gateways that play each wire API's quirks are built in Phase 10 (`test/fakegateway`).
- ✅ Step 1, Segments built by code: a compaction on demand or at the start of a Run closes a Segment, and keeps the ToolSets the Segment used and those the kept tail calls. A user Message or final reply past 2K tokens is cut, and the line that says so names `read_segment`, which reads the rest from step 4. The compact endpoints take `min_tokens`, so a service's idle timer skips a stretch too short to be worth a model call, and a compaction that names no `keep` keeps the latest Run's Turns as its tail, as mid-Run. A Record compacted into one summary before Segments is refused, not migrated. A compaction that fails is dropped, and the Run goes on.
- ✅ Step 2, the model writes the Recap, on the cached prefix: the request is the Context the next call would send plus one plain ask, validated by the five-heading rules, and a reply that fails leaves the Segment without a Recap. It is off by default in the library, the service and the CLI. The payments check of G-E01 ships as `gotato-workload -recap-check`. With it, a service can compact its idle Sessions before their cache goes cold.
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

A Session's tools come from many places, and a service that connects MCP servers can offer hundreds of them. The model should see the few it needs, and a Tool Result should never fill the Context on its own. This phase loads tools one by one from a catalog, adds the MCP adapter that supplies them, spills a large Tool Result into the Record, and takes the counting caps out of the core. Designed on 2026-10-25 ([DESIGNS.md → A limit guards a real failure](DESIGNS.md#a-limit-guards-a-real-failure--2026-10-25), [FEATURES.md → G-F34](FEATURES.md#g-f34--tool-search-), [G-F12](FEATURES.md#g-f12--the-mcp-adapter-) and [G-F35](FEATURES.md#g-f35--large-tool-results-)). Each step is marked ✅ when built; they build on one another, in this order:

- ✅ Step 1, limits that guard a real failure: `MaxTurns`, `MaxToolCalls`, `MaxToolResultBytes` and `RunDeadline` leave the core. A team that wants a Turn or call cap writes a `TurnStopper`, and a Run's deadline is its `ctx`. `service.Agent` keeps one Run deadline, `RunTimeout` (30 minutes by default), that the request's own timeout overrides. `MaxMessageBytes` stays, and `ModelCallDeadline` and `ToolCallDeadline` stay, marked for review. The DESIGNS section about every Run ending is the final form of this step.
- ✅ Step 2, large Tool Results: a Tool Result over one page (about 8K tokens) is spilled into the Record as an Entry of its own, the Context holds a preview and a ref, and the model reads the rest page by page with `read_result`, which pages as `read_segment` does. A load does not read a spilled body until it is asked for. This replaces what `MaxToolResultBytes` did.
- ✅ Step 3, tool search: the unit of loading is one tool. `search_tools` replaces `activate_toolset`, and its description is the catalog. `WithFoldedToolSet` joins `WithToolSet` and `WithActiveToolSet`. A loaded tool stays until the next compaction. `MaxActiveToolSets` is removed, `tool_loaded` and `tool_unloaded` replace `toolset_activated`, and a `ToolSearcher` lets a team plug in its own search. The tool list changes when a tool loads (the list form); the native form is step 4.
- ✅ Step 4, native deferred loading: the part of G-F32 that concerns tools. On Anthropic Messages and OpenAI's Responses API the tool list stays byte for byte the same while tools load, chosen by the gateway key `deferred_tools`; Chat Completions changes the tool list.
- ✅ Step 5, the MCP adapter `adapter/mcp`: a Go module of its own, as `adapter/grpc` is, on the official MCP Go SDK (v1.8.0). One MCP server is one ToolSet, streamable HTTP and stdio are the transports, and a tool's annotations become its Effect. It comes with a change to the core: a ToolSet that fails to resolve is skipped with a warning instead of failing `NewSession`, and ToolSets resolve again at the start of every Run, so a server's `list_changed` takes effect at the next Run, a loaded tool that vanished is unloaded (`why: gone`), a recovered ToolSet comes back, and the ToolSets resolve at the same time, so a Run waits for the slowest server ([FEATURES.md → G-F10](FEATURES.md#g-f10--tool-sets-and-the-registry-)).

Exit:

- ✅ A test on a fake model plays a Run that no `TurnStopper` stops and that settles by its model's reply, by its `ctx`, by a `TurnStopper` and by the compaction ceiling, and finds no counting cap in the core.
- ✅ A test spills a Tool Result of 1 MB, checks that the Context holds only the preview and the ref, that a load reads no spilled body, that `read_result` pages through the whole body, and that every instance builds the same request from the same Record (G-G09).
- ✅ A test loads tools by name and by query on a fake model, checks that a call to an unloaded catalog tool fails with a Tool Result that says it is loaded now, that a compaction keeps the tools called in the closed Segment or the kept tail and unloads the rest, that every instance offers the same tools after a load, that the Effect cap keeps a tool out of the index, and that a tool switched off for a Session is never loaded.
- ✅ A test of each wire API's form of deferred loading checks the request bytes of two consecutive Turns that load a tool: the same tool list in the native form, a changed one in the list form (G-G07).
- ✅ The MCP adapter's tests run against an in-process MCP server over both transports, and check the Effect from annotations, the slimmed schemas, the content mapping and a server's `list_changed` at the next Run. Its module passes its own `go test`, `go vet` and `golangci-lint`.
- ✅ G-G07, G-G09 and G-G10 keep passing.

## Phase 8 — Warm Session ✅

A Session stays in memory while it is in use, so a Run in progress can be steered from any endpoint, and the runtime can tell the model facts that are not the user's words. The store stays the truth, and a warm copy is a cache, never a lock. Designed on 2026-10-25 ([DESIGNS.md → A warm copy is a cache, not a lock](DESIGNS.md#a-warm-copy-is-a-cache-not-a-lock--2026-10-25), [FEATURES.md → G-F27](FEATURES.md#g-f27--service-many-instances-) and [G-F26](FEATURES.md#g-f26--steering-a-run-)). The steps build on one another, in this order:

- ✅ Step 1, the lifecycle: after a Run the Session stays in memory, and leaves after its TTL (60 idle minutes, per Agent). Before each Run on a warm copy the instance compares the Record's head in the store with the copy's: unchanged, it uses the copy; changed, it reloads. The copy holds no Claim while idle, so another instance can take the Session over, and the old copy is dropped. A warm Session that idles is compacted by default shortly before its prompt cache expires or it leaves memory, by its own timer, with the same compaction a service's timer calls in Phase 6; a message that arrives cancels it and is served first. A setting turns the idle compaction off. What stays warm in this step is the Session's Record and its Writer, held by the Runner, which takes a clock as a setting so that tests wait for no TTL. The Runner still builds a live `gotato.Session` over the copy for each Run, since nothing reaches a Session between Runs before step 3.
- ✅ Step 2, the memory limit: warm Sessions count toward `MaxActiveSessions`. At the limit the instance evicts the longest-idle warm Session first, and refuses a Run as not run (`overloaded`) only when every live Session is running a Run.
- ✅ Step 3, messages from any endpoint: Steer, FollowUp, Interrupt and Stop reach a running Session through the Go API (`Runner.Send`, `Runner.CancelSession`), HTTP (`POST /v1/sessions/{id}/messages`), gRPC (`SendMessage`) and the CLI (`session message`, `session cancel`), each taking effect in the writer's own process, and a message that arrives after a Run has ended starts a new Run. This closes the G-F26 gap; notices join the same routes in step 4. `Interrupt`, which the core did not have yet, was built here: it cancels the model call in flight, keeps the text it had sent as a Cut-off reply, and starts the next Turn with the message. The Runner holds no queue between Runs, since a message that finds no Run starts one. It comes with:
  - the load balancer's hash first, and, for a caller that cannot route by Session, an advertised address in each instance's Liveness row (`Config.Address`, `--advertise`; optional) and a new error, `not_writer`, marked not run and retryable, that names the writer's address when a message reaches another instance during a Run;
  - forwarding to that address by the HTTP and gRPC endpoints (`httpapi.WithForwarding`, `grpcadapter.WithForwarding`, `--forward`), as a setting a team opts in to; a forwarded request is marked and never forwarded again. An embedding service forwards with its own client. Gotato builds no network between instances itself;
  - the message Events (`message_queued`, `message_committed`, `message_dropped`, `message_rejected` and `abort_requested`) reaching the Runner and every endpoint, written through the Run's Writer. A message queued as its Run settles runs in a Run that follows it, on the same instance, in the order it was accepted; it is dropped, with a reason in its Event, only when the Run was stopped, the instance drains or the writer failed. `message_withdrawn` waits for a way to withdraw a message.
- ✅ Step 4, runtime notices (G-F29), designed on 2026-10-18: a notice is a Message of its own kind (`gotato.RoleNotice`), waits in the queue that holds a Steer, and goes in right before the next model call. It starts no Round and no Run. The output-limit continuation is a `continue` notice, and a service sends its own through `Session.Notice`, `Runner.Notice`, HTTP (`POST /v1/sessions/{id}/notices`), gRPC (`Notice`) and the CLI (`session notice`), with the routing of step 3 (`not_writer`, forwarding). A `time_away` notice joins them: a message that starts a Run after the Session was idle longer than a threshold (30 minutes by default, `Agent.TimeAwayAfter`) is followed by how long it has been and the current time. It comes with:
  - the notices kept in the Record (`notice_queued`, `notice_committed`, `Record.PendingNotices`, `Summary.Notices`), so a notice sent between Runs, or left by a Run that settled or was stopped, outlives its instance and goes in after the prompt of the Session's next Run on any instance;
  - the Session's last busy time kept in the Summary (`Summary.LastRunEnd`), so a Session loaded from its facts knows how long it was idle;
  - tagged text on all three wire APIs: a text block in the adjacent user turn on Anthropic Messages, a user item on the Responses API, a user message on Chat Completions; a Segment keeps a notice from a service word for word and leaves Gotato's own out.

  The other two of Gotato's own kinds (`settings_changed`, `run_interrupted`) are sent by G-F32.
- ⬜ The tags reach each wire API in its default native form, which the gateway configuration can override, with tagged text as the fallback for every model; a notice from the service stays inside a Segment word for word. This waits for runtime notices, and for changes that keep the cache (G-F32).
- ✅ Step 5, a stream cut after some output (G-F31): the gateway sends the same request again within `max_retries`, with a `model_retried` Event, and the reply cut off stays in the Record as a log.
- `gotato.Session`, which Phase 3 named, keeps its name. The warm copy, its TTL and its timers belong to the Runner, because they need the store's head and a clock; the live Session grows what step 3 needs, its queue held between Runs.

Exit:

- ✅ G-G08 passes with messages that arrive mid-Run (`TestMessagesMidRunLandOnceOnTheWriter`, `TestManyInstancesOneWriterWithMessages`).
- ✅ A test steers, interrupts and stops a Run on a fake model, and checks where each message lands in the Transcript (`service/messages_test.go`; the core's `interrupt_test.go`).
- ✅ A test sends a message for a Run to an instance that is not its writer, and gets `not_writer`, marked not run and retryable, with the writer's advertised address; with forwarding on, the endpoint sends it to that address and the Run takes it. With no Run going, the same instance takes the Session over and starts a new Run. It runs over the Go API, HTTP (409, `Gotato-Writer`), gRPC (`FAILED_PRECONDITION`, `gotato-writer`) and the CLI (exit 1, `not_writer`).
- ✅ A test sends notices during a Run and between Runs, and checks that each lands right before a model call, after the Tool Results or the next prompt, starts no Round and no Run, and reaches each wire API (tagged text today; the native forms wait for G-F32): `TestNoticeDuringARunLandsAfterTheToolResults`, `TestNoticeBetweenRunsWaitsForThePrompt` and `TestNoticeNeverKeepsARunGoing` in the core, `TestANoticeReachesARunAtItsNextModelCall`, `TestANoticeBetweenRunsWaitsForTheNextRun` and `TestANoticeBetweenRunsOutlivesItsInstance` in `service`, `gateway/notice_test.go`, and the HTTP, gRPC and CLI tests, `not_writer` included.
- ✅ A test brings a message to a Session idle longer than the threshold, and checks that a `time_away` notice with the elapsed time and the current time follows it, and that a message inside the threshold gets none (`TestATimeAwayNoticeFollowsAMessageAfterALongAbsence`, on an evicted Session too). `TestOutputLimitContinuationIsANoticeAndStartsNoRound` checks that the output-limit continuation is a notice and starts no Round.
- ✅ A test on a fake model lets a warm Session idle, and checks that it is compacted before its cache expires and leaves memory at its TTL.
- ✅ A test writes a warm Session's Record from another instance, and checks that the first instance, at its next Run, finds the head changed, reloads, and runs on the new Record.
- ✅ A test fills an instance with warm Sessions to `MaxActiveSessions`, and checks that a new Run evicts the longest-idle one and runs, and that with every live Session running it is refused as not run.
- ✅ G-B07 shows the load a warm Session saves, and G-B08 the heap it holds, both compared with Phase 1.

## Phase 9 — Reading large results ✅

A model that reads a spilled result page by page reads all of it or guesses. Often it wants one line, one field or the lines that match, and in a pod with no machine ([DESIGNS.md → A Session has no machine](DESIGNS.md#a-session-has-no-machine)) it cannot run `grep`. This phase gives the model a `bash` tool: a read-only subset of bash that queries its own Session's Record, with behavior that matches GNU and bash and a clear error for anything not supported. The design and what was ruled out are in [DESIGNS.md → Reading a large result needs no machine](DESIGNS.md#reading-a-large-result-needs-no-machine--2026-10-26), and the feature is [FEATURES.md → G-F36](FEATURES.md#g-f36--reading-large-results-). It is a module of its own, `adapter/bash`, whose engine does not import Gotato.

- ✅ Step 1, the core exposes a Session's Record as a read-only `fs.FS`, with the standard library alone.
- ✅ Step 2, the engine: the interpreter's handlers taken over (commands, file opens, environment) and the syntax check that refuses loops, functions, `$( )` and writing redirects, with `cat`, `head` and `grep` running. Built in `adapter/bash` (`bash.Run`, on `mvdan.cc/sh/v3`); `grep` had `-i -v -c -n -E -F -G -e -x -l -q -H -h`, and `-o -w -r -A -B -C` came in step 3.
- ✅ Step 3, the remaining commands, each with GNU conformance tests: `tail`, `sed -n`, `wc`, `sort`, `uniq`, `cut`, `tr`, `base64`, `echo`, `ls`, and `grep -o -w -r -A -B -C`. The commands follow the `C.UTF-8` locale; the supported flags are in the table of [DESIGNS.md](DESIGNS.md#reading-a-large-result-needs-no-machine--2026-10-26).
- ✅ Step 4, jq through gojq, and the protections: `jq` (`-r -j -c -s -n -R -e -S -a --tab --indent --arg --argjson`, with the differences from jq written in [DESIGNS.md](DESIGNS.md#jq-where-it-differs-from-jq)), the cut for a very long line (`WithMaxLine`, 8 KiB by default, on the final stdout only), `--help` on every command, and `ctx` and output bounds on a jq program (no heap watcher or timer of the engine's own: a process-wide measure is wrong in a service; a program that builds a huge value without output is ended only by the Run's deadline, a known limit). The spill of long output is the Gotato layer's, in step 5: the engine hands it the output up to the bound and `Result.Truncated`.
- ✅ Step 5, the Gotato layer and the reference task. `adapter/bash/bashtool` wraps the engine as the `bash` tool (Effect read, one parameter `command`), installed with `SessionTools: []service.SessionToolsFunc{bashtool.Tools()}`; the generic hook `service.Agent.SessionTools` gives it the calling Session's Record and the Runner's store, and each call runs over `session.ResultsFS`. Output is bounded at four pages and spilled by the core over one page, and the spilled output is readable through the tool. The reference task is `testkit.BashLookupTask`, played by `workload.Run` and by `go run ./cmd/gotato-workload-bash` in `adapter/bash`; it cannot join `testkit.ReferenceTasks()`, because the root module cannot link the engine.

Exit:

- ✅ A test on a fake model reads a spilled result of 1 MB with the `bash` tool, finds one line and one field, and checks that the answer is short and the same on every instance (`TestBashLookupReferenceTask`, `TestBashLookupKeepsRequestsSmall`, `TestSameRecordSameBashOutputOnAnotherInstance` in `adapter/bash/bashtool`; the other instance reads the body from the store).
- ✅ A test checks that the tool cannot write, loop, run a program or reach anything but the data of its own Session (`TestBashCannotReachAnotherSession`, `TestNonZeroExitIsAResultNotAFailure`; the engine's `TestACommandCannotReadARealFile`, `TestNoCommandStartsAProgram`, `TestUnsupportedSyntaxIsRefused` and `TestTheEnvironmentIsEmpty`).
- ✅ Every supported flag passes the GNU conformance tests in CI (`TestAgainstGNU`, run by the `adapter/bash` job with `GOTATO_GNU=1`; the last CI run on `main` before step 5 passed), and every unsupported flag and every unsupported syntax fails with a clear error (`TestUnsupportedIsAClearError`, `TestHelpAndUnsupportedOptions`, `TestUnsupportedSyntaxIsRefused`).
- ✅ `adapter/bash` passes its own vet, test and lint, and the root module still imports only the standard library (`TestLayering`).
- ✅ The decision is in DESIGNS.md and FEATURES.md, and there is no entry for it in [TODO.md](TODO.md).

## Phase 10 — Before the release

The last phase tests Gotato the way a team would run it: against gateways that behave like the providers, on several instances in a local cluster, and against the exported API it promises.

- ✅ **Fake gateways.** `test/fakegateway` is one test-only server that speaks each wire API (Claude's Messages, OpenAI's Responses, Chat Completions) and plays their quirks on demand, from a directive in the last user text or the `X-Fake-Quirk` header: a refusal as too long in each provider's words and fields, before any output and mid-stream; a stream cut after text and in a tool call's arguments; cached-token usage; a reasoning replay, which it refuses to see dropped; a system or `developer` message mid-conversation, accepted or refused as the provider does; a slow reply, for the cluster. It records every request, and `test/fakegateway/cmd/fakegateway` serves it on a port. The gateway's adaptation and compaction's overflow path run against it.
- ✅ **A local cluster.** Several instances of the chat bot below on one local Kubernetes cluster (kind), sharing a file store, with the fake gateway as the model and a fake chat platform in front, so a Run stays in flight. The scenarios, each run on a cold cluster and checked afterwards against the Records: a rolling deploy while Runs are in flight, a writer killed mid-Run (deleted, and killed with SIGKILL), scaling from 3 to 1 to 5 instances, bursts of messages on one Session from several instances, and events delivered again after a late acknowledgement; and, with the `bash` tool and an MCP server on it, a spilled result read from the instance that took a Session over, an MCP server restarted and a bot deployed while calls are in flight, and a tool loaded by search still loaded after a takeover. The cluster files, the load driver and the fake chat platform live in the example's repository, because they belong to the application.
- ✅ **A chat bot.** An example the maintainer builds, in a repository of its own: a Lark chat bot that embeds Gotato, one Session per message thread, and runs on the local cluster behind the fake gateway. It is an application and not a client Gotato ships ([G-N18](FEATURES.md#g-n18--no-ready-made-clients)). A chat platform delivers each event to one instance at random, so the bot checks from the caller's side what [FEATURES.md → G-F26](FEATURES.md#g-f26--steering-a-run-) says about a caller that cannot route by Session: it forwards a message to the writer, and retries one that was not run, with backoff and in order per Session. What it leaves to the application, with the numbers from the cluster, is how to drop an event delivered again.
- ⬜ **Compatibility check**, below.

### Compatibility check

- CI compares the exported identifiers of the core package and the standard runtime packages with those of the last tagged release, and flags a removed or changed one that no [CHANGELOG.md](CHANGELOG.md) entry names ([DESIGNS.md → A named, versioned contract](DESIGNS.md#a-named-versioned-contract)).
- It starts when mass testing begins, and the maintainer sets the date. Until the first release is tagged there is nothing to compare with, so CI does not run the check, and its tests use a baseline of their own.

Exit:

- Against a test baseline, a removed exported identifier fails CI, and the same change passes with its changelog entry. G-G06 no longer rests on review alone.
- ✅ Each quirk the fake gateways play has a test, and an overflow in each provider's form compacts and sends again. An overflow in the middle of a reply ends the Run with `context_overflow` instead, and the test pins that.
- ✅ G-G08 and G-G11 pass on the local cluster through a rolling deploy and a killed writer.
- ✅ The chat bot, with the fake gateway behind it, answers every message once through the same rolling deploy (25 of 25 messages, none twice).

## Waiting for design

These are planned in [FEATURES.md](FEATURES.md) and need a design before they get a phase. Each is discussed when what it depends on is in place.

| Feature | Depends on | Discussed |
|---|---|---|
| G-F32 — Changes that keep the cache | a reference task that changes a Session's instruction or switches a tool on for one Session, to measure what a change costs. The tools the model loads itself are Phase 7 | after that measurement |

## Every release, from the first on

- Every goal that passed keeps passing.
- Every benchmark but G-B13 is measured again, and compared with the previous release (for the first release, with the numbers of Phase 1) and with what its row in GOALS.md names.
- G-G06 is checked: each break has a before-and-after entry in [CHANGELOG.md](CHANGELOG.md).

## Not scheduled

- G-B13, which needs developers new to Gotato;
- the ideas in [FEATURES.md → Later](FEATURES.md#later);
- the other items in [TODO.md](TODO.md), which are fixed or settled outside the phases.
