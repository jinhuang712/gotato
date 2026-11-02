<!-- what-we-build: draft, waits for the maintainer's approval -->
# Gotato — Roadmap

What comes next, and how we know it is done. The roadmap adds no new feature. It schedules what [GOALS.md](GOALS.md) measures, what [FEATURES.md](../FEATURES.md) marks as planned (⬜) or names as a gap (🚧), and what real use reported in [INTEGRATION-FEEDBACK.md](INTEGRATION-FEEDBACK.md). The principles are in [DESIGNS.md](../DESIGNS.md), the forks already decided are in [decisions](decisions/README.md), and terms are in [GLOSSARY.md](../GLOSSARY.md).

0.2.0 is released (`v0.2.0` and `adapter/mcp/v0.2.0`). [CHANGELOG.md](../CHANGELOG.md) already lists breaking changes under Unreleased, so the next release is named **0.3.0**. It is built in three rounds. Each round merges and is checked on its own. No step has a date.

## Round 1: safety and documentation (no API change)

- **D-009**: the `WithForwarding` example checks the host IP against a CIDR, in the example and in `guides/http-in-five-minutes.md`; `serve --forward` says "tests only" in its help.
- **D-001**: the release notes tell the releaser to push nested-module tags first, warm the proxy, run `go get` once in a clean module cache, and announce only when it passes; `GOPROXY=direct` is the fallback.
- Docs only: D-011, D-003, D-012, D-005, D-021.
- Guides: D-013, D-023 and the D-006 sentence (streaming card, process text, deltas visible only on the writer), D-007 (de-duplication across pods), D-015 (per-Run instruction, with a test that pins it), D-014 and D-008 (`storetest` without a database, Mongo), D-022 (asking a person before a tool runs, without `SessionID` yet).
- The documentation half of D-019, D-020 and D-010: caching through a gateway, compaction after a refusal only when the window is unknown, reasoning effort through a gateway.

Done when: every item above is out of [TODO.md](TODO.md); the new guide examples compile and run.

## Round 2: small, compatible code

- **D-004** `WithDefaultEffect`: used when a tool has no annotations and no `WithToolEffect`. **D-002** `WithToolFilter` and `WithToolRename`: a rename changes only the name the Model sees; `WithToolEffect`, the filter and the call to the server keep the server's own name.
- **D-020** `ceiling=0` in `inspect` (the warning was dropped; decision 042 answers the cause).
- **D-019** the chat wire reads `cache_read_input_tokens` and `cache_creation_input_tokens`.
- **D-026** with **D-018**: `Code` on `ToolResult` and `ToolEnded`, a code for "not loaded", and the "also found, not loaded" line worded as an instruction.
- **D-022** `SessionID` on `ToolUse`.
- **D-006** and **D-007** in `examples/larkbot`: a streaming card, and Lark's `message_id` as `RequestID`.
- **D-021** an optional adapter switch that makes non-read tools `Sequential`; the default does not change.

Each change is a new field or option, adds its entry to FEATURES.md, and keeps every goal that passed.

Done when: each item has a test, a FEATURES.md entry and a CHANGELOG line; no break.

## Round 3: decide first, then build

Each item starts as a decision in [decisions](decisions/README.md). Code follows only if the decision says yes.

Both decisions are made and built: [042](decisions/042-a-models-profile-is-its-own-value-apart-from-the-connection.md) (a model's profile apart from the connection, the window stated) and [043](decisions/043-compaction-moves-content-out-and-never-makes-it-unreadable.md) (a compaction leaves a reference to what it takes out). The six items that were open here are under *Not planned*.

Done when: each item has a decision, accepted or refused.

## The 0.3.0 release

Starts when rounds 1 and 2 are merged and round 3 is decided.

- Every goal that passed keeps passing.
- Every benchmark but G-B13 is measured again and compared with the 0.2.0 values and with what its row in GOALS.md names.
- G-G06 is checked: each break, including those already under Unreleased, has a before-and-after entry in CHANGELOG.md.
- The D-001 procedure is followed, and the release notes name what was never checked against a real provider.

## Not planned

Each has a reason; one is picked up again only when someone names a real failure or need.

- G-B13, which needs developers new to Gotato;
- the ideas in [FEATURES.md → Later](../FEATURES.md#later);
- **D-024** `MaxTurns` and `MaxToolCalls`: decision 038 allows a cap only for a named failure, and a slow Run that ends well is not one; a team that wants a cap writes a `TurnStopper`;
- **D-018** (second half) loading and running a read-only tool on the refusal: the reworded `search_tools` line comes first, and nothing shows yet that more is needed;
- **D-019** `cache_control` on the chat wire: whether an endpoint adds it is that endpoint's business, and no measurement shows a cache would speed up a Run;
- **D-010** a pass-through field for reasoning effort: no team has needed it;
- **D-005** a price table or an Event hook: cost is a team's own conversion of `Usage`, and FEATURES.md says so;
- the core de-duplicating `RequestID` (named in D-007): the example passes the message id as `RequestID`, and no one needs the core to do it;
- **D-032** a per-Run value that reaches `ToolUse`: the caller's `ctx` already carries one into `Before` and `Execute`, and no one needs more;
- the exported plumbing of `Feed` (`Published`, `PublishLabeled`, `SetLabel`, `MirrorTo`, `NewInstanceFeed`): a smaller surface means redesigning the `service` and `gotato` split, which costs more than it gives and does not touch a user of Gotato;
- whether a user who comes back after a compaction finds the conversation they left (G-E01 checks that the Recap keeps the facts, not that the conversation carries on): no way to measure it is designed, and it needs a real model;
- whether `tool_choice: none` keeps the prompt cache on Anthropic: Gotato runs no check against a live provider, and the Recap request is a default per wire API that the configuration can change;
- how long a model-written Recap takes, against `ModelCallDeadline`: a team sees it on its own endpoint, and `ModelRecap.Timeout` sets the call's own deadline;
- whether each provider words a refusal as too long as the fake gateway does: a team that finds another wording adds it by wrapping `gateway.DefaultClassifier` (`overflow_phrases` in the test tools' file);
- **D-025** `arguments` on `tool_started`: the base64 form is documented in round 1 and that is enough for now;
- the other items in [TODO.md](TODO.md), which are fixed or settled outside any release.
