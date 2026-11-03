# Gotato — Roadmap

**Current checkpoint:** [CP4: The 0.3.0 release](#cp4-the-030-release). The code of 0.3.0 is built and every objective but [O11: Breaking changes are written down](GOALS.md#o11-breaking-changes-are-written-down) was met again on 2026-11-03; what is left is the benchmarks, the live run, the check of [O11: Breaking changes are written down](GOALS.md#o11-breaking-changes-are-written-down) and the tags.

The roadmap schedules what [GOALS.md](GOALS.md) checks and what [FEATURES.md](../FEATURES.md) marks as planned. It adds no feature. The guidelines are in [DESIGNS.md](../DESIGNS.md), and what is open is in [TODO.md](TODO.md).

## Summary

```
Roadmap
├── ✅ CP1: The first release, 0.2.0 — a library that runs on many instances, tagged 2026-10-29
├── ✅ CP2: What a compaction takes out stays readable — the fixes from real use, and a reference for everything a compaction removes
├── ✅ CP3: Layers a team can replace — profiles, one model description, parts and one package per layer
└── 🟨 CP4: The 0.3.0 release — the goals checked again, then tagged
    ├── ✅ CP4.1: Objectives and constraints — met again on 2026-11-03
    ├── 🟨 CP4.2: The scripted benchmarks — measured again on a quiet machine
    ├── ⬜ CP4.3: The live run — measures what only a real model can
    ├── ⬜ CP4.4: Breaking changes are written down — checked against 0.2.0
    └── ⬜ CP4.5: The version is cut
```

## CP1: The first release, 0.2.0

**Status:** ✅ 2026-10-29

A Session survives the loss of an instance, and many instances behave like one. Tagged as 0.2.0, and a break is written down from this release on.

**Delivers:**

- [F1: Runs](../FEATURES.md#f1-runs)
- [F2: Sessions](../FEATURES.md#f2-sessions)
- [F4: Context](../FEATURES.md#f4-context)
- [F5: Compaction](../FEATURES.md#f5-compaction)
- [F6: Tools](../FEATURES.md#f6-tools)
- [F7: Model](../FEATURES.md#f7-model)
- [F8: Store](../FEATURES.md#f8-store)
- [F9: Many instances](../FEATURES.md#f9-many-instances)
- [F10: Events](../FEATURES.md#f10-events)
- [F11: Extensions](../FEATURES.md#f11-extensions)
- [F12: HTTP API](../FEATURES.md#f12-http-api)
- [F13: MCP adapter](../FEATURES.md#f13-mcp-adapter)
- [F14: Library](../FEATURES.md#f14-library)

**Exit criteria:** each met on 2026-10-25 or before.

- **Objectives**
  - [O1: Session reconstructable](GOALS.md#o1-session-reconstructable-)
  - [O2: The same Record gives the same request](GOALS.md#o2-the-same-record-gives-the-same-request-)
  - [O3: No unplanned change to the prefix](GOALS.md#o3-no-unplanned-change-to-the-prefix-)
  - [O4: A long Session stays within its window](GOALS.md#o4-a-long-session-stays-within-its-window-)
  - [O5: A failure leaves the Session usable](GOALS.md#o5-a-failure-leaves-the-session-usable-)
  - [O6: One owner across instances](GOALS.md#o6-one-owner-across-instances)
  - [O7: No silent stream cutoffs](GOALS.md#o7-no-silent-stream-cutoffs)
  - [O8: No failures caused by Gotato](GOALS.md#o8-no-failures-caused-by-gotato)
  - [O9: Context fidelity](GOALS.md#o9-context-fidelity)
  - [O10: Effect cap holds](GOALS.md#o10-effect-cap-holds)

## CP2: What a compaction takes out stays readable

**Status:** ✅ 2026-11-02

What real use found was fixed in three rounds, each merged and checked on its own: safety and documentation, small compatible code, and a reference for everything a compaction takes out of the Context.

**Delivers:**

- [F5.1.3: What a Segment keeps](../FEATURES.md#f513-what-a-segment-keeps)
- [F5.3: Read back](../FEATURES.md#f53-read-back)
- [F6.7: Large results](../FEATURES.md#f67-large-results)

**Exit criteria:**

- **Objectives**
  - [O3: No unplanned change to the prefix](GOALS.md#o3-no-unplanned-change-to-the-prefix-) holds on a Segment written with refs.
  - [O2: The same Record gives the same request](GOALS.md#o2-the-same-record-gives-the-same-request-) holds on a Segment written with refs.
  - [O4: A long Session stays within its window](GOALS.md#o4-a-long-session-stays-within-its-window-) still holds.

## CP3: Layers a team can replace

**Status:** ✅ 2026-11-03

Each layer does one job, and a team replaces a default without copying the code around it. Definitions, the model bridge, compaction, the store and the host were rebuilt to that rule, and a value is set only through the layer that owns it.

**Delivers:**

- [F2.4: Your own metadata](../FEATURES.md#f24-your-own-metadata)
- [F3: Agent profiles and tasks](../FEATURES.md#f3-agent-profiles-and-tasks)
- [F5.6: Replace a part](../FEATURES.md#f56-replace-a-part)
- [F7.8: Replace a layer](../FEATURES.md#f78-replace-a-layer)
- [F8.3: Your own store](../FEATURES.md#f83-your-own-store)
- [F8.8: Keep definitions](../FEATURES.md#f88-keep-definitions)
- [F9.8: Admission](../FEATURES.md#f98-admission)
- [F14.2: Layers that import only downward](../FEATURES.md#f142-layers-that-import-only-downward)

**Exit criteria:**

- **Constraints**
  - [R1: Each package imports only the layers below it](GOALS.md#r1-each-package-imports-only-the-layers-below-it)
- **Objectives**, met again after the last of these landed, on 2026-11-03
  - [O1: Session reconstructable](GOALS.md#o1-session-reconstructable-)
  - [O2: The same Record gives the same request](GOALS.md#o2-the-same-record-gives-the-same-request-)
  - [O3: No unplanned change to the prefix](GOALS.md#o3-no-unplanned-change-to-the-prefix-)
  - [O4: A long Session stays within its window](GOALS.md#o4-a-long-session-stays-within-its-window-)
  - [O5: A failure leaves the Session usable](GOALS.md#o5-a-failure-leaves-the-session-usable-)
  - [O6: One owner across instances](GOALS.md#o6-one-owner-across-instances)
  - [O7: No silent stream cutoffs](GOALS.md#o7-no-silent-stream-cutoffs)
  - [O8: No failures caused by Gotato](GOALS.md#o8-no-failures-caused-by-gotato)
  - [O9: Context fidelity](GOALS.md#o9-context-fidelity)
  - [O10: Effect cap holds](GOALS.md#o10-effect-cap-holds)

## CP4: The 0.3.0 release

**Status:** 🟨

The next release is 0.3.0, because Unreleased holds breaking changes. Its gates are the goals checked again, in the order below, and then the tags. A goal whose last result is dated before 2026-11-02 predates the layers of CP3 and is measured again here.

**Delivers:**

- [F14.4: A small footprint](../FEATURES.md#f144-a-small-footprint)
- [F14.5: A versioned contract](../FEATURES.md#f145-a-versioned-contract)

**Exit criteria:**

- **Sub-checkpoints**
  - [CP4.1: Objectives and constraints](#cp41-objectives-and-constraints) is met.
  - [CP4.2: The scripted benchmarks](#cp42-the-scripted-benchmarks) is met.
  - [CP4.3: The live run](#cp43-the-live-run) is met.
  - [CP4.4: Breaking changes are written down](#cp44-breaking-changes-are-written-down) is met.
  - [CP4.5: The version is cut](#cp45-the-version-is-cut) is met.
- **Do not hold the release,** since they need people or a real model
  - [M2: The first request after an idle return](GOALS.md#m2-the-first-request-after-an-idle-return-)
  - [M4: Time to a first agent](GOALS.md#m4-time-to-a-first-agent)
  - [B12: The Recap keeps the facts](GOALS.md#b12-the-recap-keeps-the-facts-)

### CP4.1: Objectives and constraints

**Status:** ✅

Run again on 2026-11-03, after the last decision landed: the suites of all six modules pass under the race detector, and the counts are in GOALS.

**Exit criteria:**

- **Objectives**, every one but [O11: Breaking changes are written down](GOALS.md#o11-breaking-changes-are-written-down), which [CP4.4: Breaking changes are written down](#cp44-breaking-changes-are-written-down) holds
  - [O1: Session reconstructable](GOALS.md#o1-session-reconstructable-)
  - [O2: The same Record gives the same request](GOALS.md#o2-the-same-record-gives-the-same-request-)
  - [O3: No unplanned change to the prefix](GOALS.md#o3-no-unplanned-change-to-the-prefix-)
  - [O4: A long Session stays within its window](GOALS.md#o4-a-long-session-stays-within-its-window-)
  - [O5: A failure leaves the Session usable](GOALS.md#o5-a-failure-leaves-the-session-usable-)
  - [O6: One owner across instances](GOALS.md#o6-one-owner-across-instances)
  - [O7: No silent stream cutoffs](GOALS.md#o7-no-silent-stream-cutoffs)
  - [O8: No failures caused by Gotato](GOALS.md#o8-no-failures-caused-by-gotato)
  - [O9: Context fidelity](GOALS.md#o9-context-fidelity)
  - [O10: Effect cap holds](GOALS.md#o10-effect-cap-holds)
- **Constraints**
  - [R1: Each package imports only the layers below it](GOALS.md#r1-each-package-imports-only-the-layers-below-it)
  - [R2: A Session runs in a small main with no server or database](GOALS.md#r2-a-session-runs-in-a-small-main-with-no-server-or-database)

### CP4.2: The scripted benchmarks

**Status:** 🟨

Run on 2026-11-03 on a machine with other work running. They are run again on a quiet machine, with 0.2.0 built beside the current code, because time per Turn looks 10 to 35% above 0.2.0 ([TODO.md](TODO.md)). The reference service's resident memory ([B8: Memory per task](GOALS.md#b8-memory-per-task)) has no script yet.

**Exit criteria:**

- **Benchmarks:** measured again and set against their reference
  - [B5: Gotato's time per Turn](GOALS.md#b5-gotatos-time-per-turn)
  - [B6: Time added before the first token](GOALS.md#b6-time-added-before-the-first-token)
  - [B7: Cold-load time](GOALS.md#b7-cold-load-time)
  - [B8: Memory per task](GOALS.md#b8-memory-per-task)
  - [B9: CPU per Turn, at 10, 100 and 1,000 messages](GOALS.md#b9-cpu-per-turn-at-10-100-and-1000-messages)
  - [B10: Startup time](GOALS.md#b10-startup-time)
  - [B11: Concurrent Runs per instance before Gotato's time per Turn doubles](GOALS.md#b11-concurrent-runs-per-instance-before-gotatos-time-per-turn-doubles)
  - [B13: What a user learns](GOALS.md#b13-what-a-user-learns)
- **Comparisons**
  - [C1: Footprint](GOALS.md#c1-footprint) is measured again and set against its reference.
- **Measures**
  - [M1: Context size and cost per Turn by the Session's age](GOALS.md#m1-context-size-and-cost-per-turn-by-the-sessions-age-): the sizes and tokens are measured again.
  - [M3: Cost of a compaction](GOALS.md#m3-cost-of-a-compaction): the tokens are measured again.

### CP4.3: The live run

**Status:** ⬜

The last live results are older than the layers of CP3, and three measures have no cost or time yet. A live run needs a model endpoint and its price, so it is on demand and not in CI. A goal not measured by the release stays ⬜, and the release notes name what was never checked against a real provider, as those of 0.2.0 do.

**Exit criteria:**

- **Benchmarks:** measured again, or named in the release notes as not measured
  - [B1: Cache hit rate](GOALS.md#b1-cache-hit-rate-)
  - [B2: Token cost per task](GOALS.md#b2-token-cost-per-task)
  - [B3: Tool-call success rate, split by cause](GOALS.md#b3-tool-call-success-rate-split-by-cause)
  - [B4: Error rate, split by cause](GOALS.md#b4-error-rate-split-by-cause)

### CP4.4: Breaking changes are written down

**Status:** ⬜

Each break under Unreleased has its before-and-after entry, `apidiff` is run on the shipped packages against `v0.2.0`, and the HTTP contract version is settled ([TODO.md](TODO.md)).

**Exit criteria:**

- **Objectives**
  - [O11: Breaking changes are written down](GOALS.md#o11-breaking-changes-are-written-down) is met.

### CP4.5: The version is cut

**Status:** ⬜

The Unreleased entries become the 0.3.0 entry with its date, and the root and both adapters are tagged as the release procedure in CONTRIBUTING.md says. `adapter/http` has no tag yet, so this is its first.

**Exit criteria:**

- [CP4.1: Objectives and constraints](#cp41-objectives-and-constraints) to [CP4.4: Breaking changes are written down](#cp44-breaking-changes-are-written-down) are met, and the tags are pushed.
