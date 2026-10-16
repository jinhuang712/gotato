<!-- what-we-build: approved 2026-10-16 -->
# Gotato — Roadmap

What gets built next, in what order, and how we know each step is done. The roadmap adds no new feature. It schedules what [GOALS.md](GOALS.md) measures, what [FEATURES.md](FEATURES.md) marks as planned (⬜) or names as a gap (🚧), and the open questions in [DESIGNS.md](DESIGNS.md) that the maintainer has placed. Terms are in [GLOSSARY.md](GLOSSARY.md).

Gotato stays at 0.1.x until it is stable, so each phase ships as a 0.1.x release, with no date. A phase is done when its exit criteria hold: goals that pass, benchmarks that have a number, decisions written down, and the checks the phase names.

## The order

The phases, in order, with the design discussion that comes before the store changes:

```text
Phase 1  Measure         the reference workload and the live run
Phase 2  Effect cap      an Agent hides the tools above its highest Effect
Phase 3  Names           the Agent and the Loop take their glossary names
         ── design discussion: ownership, how a Turn is saved, what a warm Session holds ──
Phase 4  Durable Turns   save every Turn, and refuse a stale save
Phase 5  Ownership       one instance runs a Session at a time
Phase 6  Warm Session    a Session stays in memory while in use, and leaves after its TTL
Phase 7  Structured      an Agent's final reply fits a JSON schema
Phase 8  Access hooks    the caller's identity reaches extensions and stores, and a refusal answers 403
Phase 9  Compatibility   CI flags a removed or changed exported identifier
         ── waiting for design: a last Turn that answers, recovering a cut stream, changes that keep the cache ──
```

Why this order:

- **Measurement comes first,** so each later phase is measured against a baseline. Saving every Turn adds a write per Turn, and the benchmarks show what that costs.
- **The Effect cap and the rename depend on nothing else.** The cap adds a setting to the Agent, not a type, so it does not wait for the rename.
- **The rename comes before the store and ownership work,** so that code is written with the final names. The stored record keeps its name until Phase 6 builds the live Session, so it is renamed once.
- **The design discussion comes before the store changes.** The store contract that Phase 4 ships already carries what ownership needs, such as a revision or a fencing token, so store adapters change once.
- **Each of the last three phases builds on the one before.** Ownership needs Turns that are saved and checked, and a warm Session needs an owner to hold it.
- **Fault injection and the multi-instance harness come with the phases whose goals they check.** The store conformance suite comes with Phase 4, because it is written against the contract that phase ships.
- **Structured output depends on nothing above,** so the maintainer can move it earlier. It sits after the store work because the multi-pod goals carry the pitch.
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

Every number about a Run is computed from recorded Events and `Usage`, so a team gets the same numbers from its own telemetry ([DESIGNS.md → Events and telemetry](DESIGNS.md#events-and-telemetry)). Memory, CPU, startup and footprint come from Go benchmarks and the build script.

Exit:

- ✅ G-G03 passes in CI.
- ✅ G-G04 passes on the reference workload in CI, and the live run counts no failure caused by Gotato.
- ✅ Every benchmark but G-B13 has a number.

## Phase 2 — Effect cap ✅

Built, and ships in the next 0.1.x release.

- ✅ An Agent can name the highest Effect its tools may have, with `gotato.WithMaxEffect` or `service.AgentSpec.MaxEffect`. There is no cap by default.
- ✅ A tool above the cap is hidden from the model, including the tools of a ToolSet it activates later, and a call to it fails as a call to an unknown tool ([DESIGNS.md → Tools and safety](DESIGNS.md#tools-and-safety)).
- ✅ The cap belongs to the Agent: a Session can neither change it nor switch on a hidden tool.
- ✅ `gotato tools list`, `GET /v1/agents` and the gRPC `Agents` call report the cap.

Exit:

- ✅ G-G05 passes.

## Phase 3 — Names

- `gotato.Agent` becomes the Loop, and `service.AgentSpec` becomes the Agent.
- Event kinds and fields that carry the old names, such as `agent_start` and `agent_id`, change with them.
- The rename ships as one planned break, with a [CHANGELOG.md](CHANGELOG.md) entry. A `// Deprecated:` alias is not possible, because the name `Agent` takes a new meaning.
- `session.Session`, the stored record, keeps its name until Phase 6.

Exit: `gotato.Agent` and `service.AgentSpec` carry their new names, and G-G06 holds for the break, with a contract version bump for the changed Event kinds and fields.

## Design discussion

After Phase 3, the maintainer and contributors answer the questions in [DESIGNS.md → Open questions](DESIGNS.md#open-questions):

- how ownership of a Session works across instances, including what happens to a message that reaches an instance that does not own it;
- how a Turn is saved: an append of the new Turn, or a rewrite of the whole Session;
- what a warm Session holds, and what happens to a message that arrives while a Run is in progress;
- how Gotato tells the model something that is not the user's words: runtime notices (FEATURES.md G-F29), and the Events on a message's way into a Session. They follow from the answer to the question above;
- what the stored record is called, once Phase 6 gives the name Session to the live unit.

One requirement is already fixed: a second instance stops before it calls the model.

Exit: each answer is a Decision in DESIGNS.md, approved by the maintainer.

## Phase 4 — Durable Turns

- Every Turn a Session commits is saved, so a crash loses at most the Turn in flight.
- A save made from a stale copy fails with a conflict, instead of overwriting a newer one.
- The store contract carries what ownership needs, as the design discussion decided.
- A store conformance suite in `testkit`, written against that contract. An adapter runs it to prove it keeps what Gotato relies on, and the memory and file stores run it too ([DESIGNS.md → Decisions](DESIGNS.md#adapters-prove-the-store-contract-with-a-conformance-suite--2026-10-16)).
- Fault injection: a crash at every step of the reference workload, and a client that disconnects mid-stream on HTTP and gRPC.

Exit:

- G-G01 and G-G02 pass.
- The memory and file stores pass the conformance suite, and a test adapter that overwrites a newer save fails it.
- G-B05 and G-B07 show what saving every Turn costs, compared with Phase 1.

## Phase 5 — Ownership

- One instance runs a Session at a time. A second instance stops before it calls the model.
- A message that reaches an instance that does not own the Session is forwarded, made to wait or rejected, as the design discussion decided.
- Cancel and stream requests reach the owner from any instance.
- The multi-instance harness: several instances over one shared store, with concurrent messages and random instance kills.

Exit:

- G-G08 passes for every message an instance accepts.
- In the harness, a Run is cancelled and streamed from an instance that does not own its Session.

## Phase 6 — Warm Session

- A Session stays in memory while it is in use, and leaves after its TTL. The store stays the truth.
- Before it leaves, the Session is compacted by default, and a setting turns this off. How it is compacted waits for the compaction discussion of a later phase ([DESIGNS.md → Compaction](DESIGNS.md#compaction)); until then, the TTL only removes the Session from memory.
- A message that arrives mid-Run is queued, steered or rejected, as the design discussion decided, the same way through the Go API, HTTP, gRPC and the CLI. This closes the G-F26 gap.
- Runtime notices (G-F29) ship as the design discussion decided.
- Longer prompt cache retention, and a keepalive for a warm Session's cache (G-F33), are designed with the warm Session.
- The stored record takes the name the design discussion chose, and the live Session takes the name Session. This ships as a planned break, with a [CHANGELOG.md](CHANGELOG.md) entry.

Exit:

- G-G08 passes with messages that arrive mid-Run.
- G-G06 holds for the rename of the stored record.
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

- CI compares the exported identifiers of the root package and the standard runtime packages with the last release, and flags a removed or changed one that no [CHANGELOG.md](CHANGELOG.md) entry names ([DESIGNS.md → Compatibility](DESIGNS.md#compatibility)).
- It starts when mass testing begins, and the maintainer sets the date.

Exit: a removed exported identifier fails CI, and the same change passes with its changelog entry. G-G06 no longer rests on review alone.

## Waiting for design

These are planned in [FEATURES.md](FEATURES.md) and need a design before they get a phase. Each is discussed when what it depends on is in place.

| Feature | Depends on | Discussed |
|---|---|---|
| G-F30 — A last Turn that answers | runtime notices (G-F29), and `tool_choice` in every gateway | once G-F29 is designed |
| G-F31 — Recovering a cut stream | runtime notices (G-F29) | once G-F29 is designed |
| G-F32 — Changes that keep the cache | a reference task that switches a tool set on, to measure what a change costs | after that measurement |

## Every release

- Every goal that passed keeps passing.
- Every benchmark but G-B13 is measured again, and compared with the previous release and with what its row in GOALS.md names.
- G-G06 is checked: each break has a before-and-after entry in [CHANGELOG.md](CHANGELOG.md).

## Not scheduled

- G-B13, which needs developers new to Gotato;
- the ideas in [FEATURES.md → Later](FEATURES.md#later);
- the other open questions in [DESIGNS.md → Open questions](DESIGNS.md#open-questions): the model catalog, and how a Session is compacted, including before its TTL and as part of an Agent's definition;
- the other items in [TODO.md](TODO.md), which are fixed or settled outside the phases.
