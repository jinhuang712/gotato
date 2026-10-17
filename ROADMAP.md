<!-- what-we-build: drafted 2026-10-17 -->
# Gotato — Roadmap

What gets built next, in what order, and how we know each step is done. The roadmap adds no new feature. It schedules what [GOALS.md](GOALS.md) measures, what [FEATURES.md](FEATURES.md) marks as planned (⬜) or names as a gap (🚧), and the design questions still open, which it places at the start of the phase that needs them or keeps under [Waiting for design](#waiting-for-design). The principles that answer them are in [DESIGNS.md](DESIGNS.md). Terms are in [GLOSSARY.md](GLOSSARY.md).

Gotato stays at 0.1.x until it is stable, so each phase ships as a 0.1.x release, with no date. A phase is done when its exit criteria hold: goals that pass, benchmarks that have a number, decisions written down, and the checks the phase names.

## The order

The phases, in order, with the design discussion that comes before the store changes:

```text
Phase 1  Measure         the reference workload and the live run
Phase 2  Effect cap      an Agent hides the tools above its highest Effect
Phase 3  Names           the Agent, the live Session and its Record take their glossary names
         ── design discussion: how a Turn is saved, who writes, where messages go (answered) ──
Phase 4  Durable steps   every step is appended as it happens, and every append is fenced
Phase 5  One writer      any instance answers like the only one, with one writer at a time
Phase 6  Warm Session    a Session stays in memory while in use, and takes Steer, Interrupt and Stop from any endpoint
Phase 7  Structured      an Agent's final reply fits a JSON schema
Phase 8  Access hooks    the caller's identity reaches extensions and stores, and a refusal answers 403
Phase 9  Compatibility   CI flags a removed or changed exported identifier
         ── waiting for design: a last Turn that answers, recovering a cut stream, changes that keep the cache, how a Session is compacted ──
```

Why this order:

- **Measurement comes first,** so each later phase is measured against a baseline. Saving every step adds a write per step, and the benchmarks show what that costs.
- **The Effect cap and the rename depend on nothing else.** The cap adds a setting to the Agent, not a type, so it does not wait for the rename.
- **The rename comes before the store and writer work,** so that code is written with the final names. The live Session and its Record both take their names in Phase 3, so Phase 6 grows the Session without renaming it.
- **The design discussion comes before the store changes.** The store contract that Phase 4 ships already carries the Claims, the Epoch and the Liveness rows that Phase 5 uses, so store adapters change once.
- **Each of the last three phases builds on the one before.** One writer needs Turns that are appended and fenced, and a warm Session needs a writer to hold it.
- **Fault injection and the multi-instance harness come with the phases whose goals they check.** The store conformance suite comes with Phase 4, because it is written against the contract that phase ships.
- **Structured output depends on nothing above,** so the maintainer can move it earlier. It sits after the store work because the many-instance goals carry the pitch.
- **Access hooks depend on nothing above either.** They give enterprise teams the places to enforce their own rules, and they come after the store work for the same reason as structured output.
- **The compatibility check waits for mass testing,** when many callers depend on the exported API.

## Phase 1 — Measure ✅

Shipped in v0.1.2.

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

Built, and ships in the next 0.1.x release.

- ✅ An Agent can name the highest Effect its tools may have, with `gotato.WithMaxEffect` or `service.Agent.MaxEffect`. There is no cap by default.
- ✅ A tool above the cap is hidden from the model, including the tools of a ToolSet it activates later, and a call to it fails as a call to an unknown tool ([FEATURES.md → The Effect cap hides tools](FEATURES.md#the-effect-cap-hides-tools-and-only-an-agent-sets-it--2026-10-16)).
- ✅ The cap belongs to the Agent: a Session can neither change it nor switch on a hidden tool.
- ✅ `gotato tools list`, `GET /v1/agents` and the gRPC `Agents` call report the cap.

Exit:

- ✅ G-G05 passes.

## Phase 3 — Names ✅

Built, and ships in the next 0.1.x release.

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

Runtime notices (FEATURES.md G-F29), and the names of the Events on a message's way into a Session, follow from the third answer. They are designed at the start of [Phase 6](#phase-6--warm-session).

Exit: the maintainer approves the three Decisions.

## Phase 4 — Durable steps ✅

Built, and ships in the next 0.1.x release.

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

## Phase 5 — One writer 🚧

The part that keeps two instances from running one Session is built, and ships in the next 0.1.x release. Each item is marked ✅ built, 🚧 started, or ⬜ to build.

- ✅ One writer at a time: an instance claims a Session in the store before it calls the model, and its Claim is valid while its Liveness row is ([DESIGNS.md → One writer per Session](DESIGNS.md#one-writer-per-session-claimed-in-the-store--2026-10-16)). An instance that cannot show its row was renewed in time writes nothing.
- ✅ Any instance answers: it takes over an idle Session, and waits, up to a bound, for one its live writer is using, then answers 409 with `Retry-After`.
- ⬜ The load balancer's part: the HTTP and gRPC endpoints read the `session-id` header, an instance past its hard limit refuses new Sessions with a 503 marked "not run", and `/readyz` reports a drain.
- 🚧 Drain in order, on SIGTERM or an endpoint: take no new Sessions, tell every live Session at once, let each finish, save and release, then close each stream with a reconnect code. Today `Runner.Close` ends the Liveness row after a drain, so another instance takes its Sessions over at once.
- ⬜ A stream resumes on any instance from its last position, with the Record's epoch.
- ⬜ Lifecycle Events: a Session activated, taken over, released or fenced.
- 🚧 The multi-instance harness: several instances over one shared store, with requests sent to random instances, concurrent messages and an instance killed. A hashing proxy in front of them is still to build.
- ⬜ Ended Liveness rows are cleaned up.

Exit:

- ✅ G-G08 passes for every message an instance accepts.
- ⬜ With requests sent to random instances, the harness gets the same Transcripts and Event order as a single instance.
- ⬜ A stream cut at every Event of the reference workload resumes equal to an uncut one.
- ⬜ A drained instance commits its Runs and releases its Claims before it leaves.
- ✅ A test stops an instance's heartbeat and expects its Claims to end and its late appends to fail.
- ⬜ A test fills an instance to its hard limit and expects a "not run" refusal, an Event, and no Session activated there.

## Phase 6 — Warm Session

- Runtime notices (G-F29) and the names of the Events on a message's way into a Session are designed first, and then built.
- A Session stays in memory while it is in use, and leaves after its TTL. The store stays the truth.
- Before it leaves, the Session is compacted by default, and a setting turns this off. How it is compacted waits for its own design ([Waiting for design](#waiting-for-design)); until then, the TTL only removes the Session from memory.
- Steer, FollowUp and Interrupt reach a running Session through the Go API, HTTP, gRPC and the CLI, as Stop already does on the instance that runs it, and each takes effect in the writer's own process. A message that arrives after a Run has ended starts a new Run. This closes the G-F26 gap.
- Longer prompt cache retention, and a keepalive for a warm Session's cache (G-F33), are designed with the warm Session.
- `gotato.Session`, which Phase 3 named, grows its saving and its TTL. Its name does not change.

Exit:

- G-G08 passes with messages that arrive mid-Run.
- A test steers, interrupts and stops a Run on a fake model, and checks where each message lands in the Transcript.
- G-B07 shows the load a warm Session saves, and G-B08 the heap it holds, both compared with Phase 1.

## Phase 7 — Structured output

- An Agent can carry a JSON schema, and its final reply fits it, on all three wire APIs ([FEATURES.md → G-F28](FEATURES.md#g-f28--structured-output-)).
- A reply that does not fit settles the Run as `failed`, with a reason that names the schema. The model is not asked to try again.
- HTTP and gRPC change nothing: the schema is part of the Agent's definition.

Exit:

- A scripted model returns a reply that fits and one that does not, and the Run settles as `completed` and as `failed`, on each wire API's replayed replies.
- An Agent without a schema behaves as before, and the contract version does not change.

## Phase 8 — Access hooks

- The caller's identity travels in `context.Context` from the handler to every extension and every store call, and a test keeps it so.
- A new "not allowed" error: a store adapter or an extension returns it, and HTTP answers 403 and gRPC the matching code ([FEATURES.md → G-F14](FEATURES.md#g-f14--http-grpc-and-streaming-)).
- Authentication and the rule for who may touch which Session stay the application's. Gotato documents one example: a Session's owner kept in its metadata and checked by a store wrapper.

Exit:

- A store wrapper that refuses a caller by the identity in `ctx` makes reading, running and cancelling a Session answer 403, tested on HTTP and gRPC.
- The new code is recorded in [CHANGELOG.md](CHANGELOG.md) as an addition to both contracts.

## Phase 9 — Compatibility check

- CI compares the exported identifiers of the root package and the standard runtime packages with the last release, and flags a removed or changed one that no [CHANGELOG.md](CHANGELOG.md) entry names ([DESIGNS.md → A named, versioned contract](DESIGNS.md#a-named-versioned-contract)).
- It starts when mass testing begins, and the maintainer sets the date.

Exit: a removed exported identifier fails CI, and the same change passes with its changelog entry. G-G06 no longer rests on review alone.

## Waiting for design

These are planned in [FEATURES.md](FEATURES.md) and need a design before they get a phase. Each is discussed when what it depends on is in place.

| Feature | Depends on | Discussed |
|---|---|---|
| G-F30 — A last Turn that answers | runtime notices (G-F29), and `tool_choice` in every gateway | once G-F29 is designed |
| G-F31 — Recovering a cut stream | runtime notices (G-F29) | once G-F29 is designed |
| G-F32 — Changes that keep the cache | a reference task that switches a tool set on, to measure what a change costs | after that measurement |
| G-F06 — How a Session is compacted: who writes the summary, what stays word for word, and how compacting before the TTL relates to the ceiling at the start of a Run | the append-only Record and its Checkpoints (Phase 4), and the warm Session's TTL (Phase 6) | after Phase 6 |

## Every release

- Every goal that passed keeps passing.
- Every benchmark but G-B13 is measured again, and compared with the previous release and with what its row in GOALS.md names.
- G-G06 is checked: each break has a before-and-after entry in [CHANGELOG.md](CHANGELOG.md).

## Not scheduled

- G-B13, which needs developers new to Gotato;
- the ideas in [FEATURES.md → Later](FEATURES.md#later);
- the other items in [TODO.md](TODO.md), which are fixed or settled outside the phases.
