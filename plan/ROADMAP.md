# Gotato — Roadmap

**Current checkpoint:** [CP4: The 0.3.0 release](#cp4-the-030-release). The code of 0.3.0 is built and every objective but O11 was met again on 2026-11-03; what is left is the benchmarks, the live run, the check of O11 and the tags.

The roadmap schedules what [GOALS.md](GOALS.md) checks and what [FEATURES.md](../FEATURES.md) marks as planned. It adds no feature. The principles are in [DESIGNS.md](../DESIGNS.md), and what is open is in [TODO.md](TODO.md).

## Summary

```
Roadmap
├── ✅ CP1: The first release, 0.2.0 — a library that runs on many instances, tagged 2026-10-29
├── ✅ CP2: Fixes and decisions from real use — safety, small compatible code and the refs of a compaction
├── ✅ CP3: Layers a team can replace — profiles, one model description, parts and one package per layer
└── 🟨 CP4: The 0.3.0 release — the goals checked again, then tagged
    ├── ✅ CP4.1: The objectives and constraints are met again on 2026-11-03
    ├── 🟨 CP4.2: The scripted benchmarks are measured again on a quiet machine
    ├── ⬜ CP4.3: The live run measures what only a real model can
    ├── ⬜ CP4.4: O11 is checked against 0.2.0
    └── ⬜ CP4.5: The version is cut
```

## CP1: The first release, 0.2.0

**Status:** ✅ 2026-10-29

- ✅ The core, the Runner on many instances over one store, the HTTP handler, the model bridge for three wire APIs, the test kit and the MCP adapter, tagged as `v0.2.0` and `adapter/mcp/v0.2.0`.
- ✅ A break is written down from this release on.

**Exit criteria:** [O1](GOALS.md#o1-session-reconstructable-), [O2](GOALS.md#o2-the-same-record-gives-the-same-request-), [O3](GOALS.md#o3-no-unplanned-change-to-the-prefix-), [O4](GOALS.md#o4-a-long-session-stays-within-its-window-), [O5](GOALS.md#o5-a-failure-leaves-the-session-usable-), [O6](GOALS.md#o6-one-owner-across-instances), [O7](GOALS.md#o7-no-silent-stream-cutoffs), [O8](GOALS.md#o8-no-failures-caused-by-gotato), [O9](GOALS.md#o9-context-fidelity) and [O10](GOALS.md#o10-effect-cap-holds), each met on 2026-10-25 or before.

## CP2: Fixes and decisions from real use

**Status:** ✅ 2026-11-02

What real use found, built in three rounds, each merged and checked on its own.

- ✅ Safety and documentation, with no API change: the forwarding example checks the host against the cluster's CIDR, the test CLI's `serve --forward` says it is for tests only, the release procedure names the nested-module tags and the proxy, and the guides cover the known gaps.
- ✅ Small compatible code: a default Effect, a filter, a rename and a sequential switch for the MCP adapter, a `Code` on a failed Tool Result and on `tool_ended`, a code for a tool not loaded, `SessionID` on `ToolUse`, the cache tokens of the chat wire, the compaction ceiling always shown by inspection, and a streaming card with the chat message's ID as `RequestID` in the example bot.
- ✅ Decision 043 built: a compaction leaves a ref to everything it takes out, and `read_result` reads it. Decision 042, a model's profile apart from its connection, was built and then superseded by decision 045 in CP3.

**Exit criteria:** [O3](GOALS.md#o3-no-unplanned-change-to-the-prefix-) and [O2](GOALS.md#o2-the-same-record-gives-the-same-request-) hold on a Segment written with refs, and [O4](GOALS.md#o4-a-long-session-stays-within-its-window-) still holds.

## CP3: Layers a team can replace

**Status:** ✅ 2026-11-03

Decisions 044 to 051, each built on main. A team replaces a default without copying the code around it, and a value is set only through the layer that owns it.

- ✅ Agent profiles and task specs, registered in code or from a store (044), and a Session opened for a task spec at create, also over HTTP (051).
- ✅ One description of a model, `ModelConfig`, and the model bridge in four layers: the wire API, the connection, the retry and the meaning of an error (045).
- ✅ Compaction as a fixed frame around parts a user replaces, the `ReadBack` that owns the page size, and the public `ref` package (046).
- ✅ Values set through their owner: reserved metadata keys, typed fields in place of `Agent.Options`, a Transcript that declares its capabilities, and values returned in place of handles (047).
- ✅ The Runner and the Session built from parts with one job each, the `Admitter` for whether a Run may start, and host plumbing out of the core's API (048).
- ✅ A store contract of two seams and an optional listing, with exported ID and batch checks (049).
- ✅ One package per layer, in the order `host`, `profile`, `store`, `record`, `gotato`, `ref`, with the HTTP API as the module `adapter/http` (050).

**Exit criteria:** [R1](GOALS.md#r1-each-package-imports-only-the-layers-below-it), and these objectives, met again after the last of these landed, on 2026-11-03: [O1](GOALS.md#o1-session-reconstructable-), [O2](GOALS.md#o2-the-same-record-gives-the-same-request-), [O3](GOALS.md#o3-no-unplanned-change-to-the-prefix-), [O4](GOALS.md#o4-a-long-session-stays-within-its-window-), [O5](GOALS.md#o5-a-failure-leaves-the-session-usable-), [O6](GOALS.md#o6-one-owner-across-instances), [O7](GOALS.md#o7-no-silent-stream-cutoffs), [O8](GOALS.md#o8-no-failures-caused-by-gotato), [O9](GOALS.md#o9-context-fidelity) and [O10](GOALS.md#o10-effect-cap-holds).

## CP4: The 0.3.0 release

**Status:** 🟨

The next release is 0.3.0, because Unreleased holds breaking changes. Its gates are the goals checked again, in the order below, and then the tags. A goal whose last result is dated before 2026-11-02 predates the layers of CP3 and is measured again here.

- ✅ **CP4.1: Objectives and constraints.** Every objective but O11, and both constraints, were run again on 2026-11-03, after the last decision landed: the suites of all six modules pass under the race detector, and the counts are in GOALS.
- 🟨 **CP4.2: The scripted benchmarks.** B5, B6, B7, B8, B9, B10 and B11 were run on 2026-11-03 on a machine with other work running, and C1 was measured the same day. They are run again on a quiet machine, with 0.2.0 built beside the current code, because time per Turn looks 10 to 35% above 0.2.0 ([TODO.md](TODO.md)). The reference service's resident memory (B8) has no script yet.
- ⬜ **CP4.3: The live run.** B1, B2, B3 and B4 are dated 2026-10-16, B5 and B6 have their live half on 2026-10-17, and M1, M2 and M3 have no cost or time yet. A live run needs a model endpoint and its price, so it is on demand and not in CI. A goal not measured by the release stays ⬜, and the release notes name what was never checked against a real provider, as those of 0.2.0 do.
- ⬜ **CP4.4: O11.** Each break under Unreleased has its before-and-after entry, `apidiff` is run on the shipped packages against `v0.2.0`, and the HTTP contract version is settled ([TODO.md](TODO.md)).
- ⬜ **CP4.5: The version is cut.** The Unreleased entries become the 0.3.0 entry with its date. The `require` line of each nested module that shipped (`adapter/mcp`, and `adapter/http`, which has no tag yet) names the new root version in the release commit, since both say `v0.2.0` today. The root and both adapters are tagged together, the proxy is warmed, and a clean module cache runs `go get` once for each before the release is announced, as the release procedure in RUNBOOK.md says.

**Exit criteria:** [O11](GOALS.md#o11-breaking-changes-are-written-down) met, and every objective and constraint of GOALS still met. The scripted goals are measured again and set against their reference: [B5](GOALS.md#b5-gotatos-time-per-turn), [B6](GOALS.md#b6-time-added-before-the-first-token), [B7](GOALS.md#b7-cold-load-time), [B8](GOALS.md#b8-memory-per-task), [B9](GOALS.md#b9-cpu-per-turn-at-10-100-and-1000-messages), [B10](GOALS.md#b10-startup-time), [B11](GOALS.md#b11-concurrent-runs-per-instance-before-gotatos-time-per-turn-doubles), [C1](GOALS.md#c1-footprint), and the sizes and tokens of [M1](GOALS.md#m1-context-size-and-cost-per-turn-by-the-sessions-age-) and [M3](GOALS.md#m3-cost-of-a-compaction). The live goals [B1](GOALS.md#b1-cache-hit-rate-), [B2](GOALS.md#b2-token-cost-per-task), [B3](GOALS.md#b3-tool-call-success-rate-split-by-cause) and [B4](GOALS.md#b4-error-rate-split-by-cause) are measured again or named in the release notes as not measured. [M4](GOALS.md#m4-time-to-a-first-agent), [M2](GOALS.md#m2-the-first-request-after-an-idle-return-) and [B12](GOALS.md#b12-the-recap-keeps-the-facts-) need people or a real model and do not hold the release.
