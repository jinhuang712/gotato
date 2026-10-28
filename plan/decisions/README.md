# Decisions

A decision is a fork in the road that someone could reasonably reverse: we chose one way, and at least one other way was open. Each lives in its own file, numbered in the order the decisions were made. A file may hold a family of related forks that share one reason, as a themed decision: each fork keeps its own line under *Not chosen*. Mechanism with no fork behind it, such as how a state machine moves, belongs in a spec, not here. The principles that the decisions follow are in [DESIGNS.md](../../DESIGNS.md).

## The format

A file is short, under one page, in plain words:

```text
# NNN. <the choice, as a statement>
Decided: YYYY-MM-DD    Status: current | superseded by NNN
## The problem        the force or limit, in one or two sentences
## The choice         what we chose, in a sentence or two
## Not chosen         one line per alternative: what it is, and why not
## Consequences       what it makes easy, and what it makes hard
```

- **Numbers are never reused.** They are three digits, zero padded, in the order of the Decided date; decisions of one day are ordered by when they were first written.
- **A decision is not rewritten.** When a later decision replaces it, the old file stays and its status says `superseded by NNN`.
- **The date is the decision's identity,** not a story. A file says what we chose and why, and leaves the history to git.

## The index

| No. | Decided | Decision |
|---|---|---|
| [001](001-one-implementation-per-wire-api.md) | 2026-09-17 | A provider is configuration, and Gotato implements each wire API once |
| [002](002-the-store-is-the-truth-and-memory-is-a-cache.md) | 2026-10-15 | The store is the truth, and memory is a cache |
| [003](003-gotato-owns-the-shapes-and-adapters-own-storage.md) | 2026-10-15 | Gotato owns the shapes, and adapters own storage and telemetry |
| [004](004-an-agent-is-a-definition-and-a-session-is-the-live-unit.md) | 2026-10-15 | An Agent is a definition, and a Session is the live unit |
| [005](005-no-approval-system.md) | 2026-10-15 | The Effect cap and tools that check their own arguments replace an approval system |
| [006](006-the-request-is-laid-out-for-the-cache-and-a-change-is-appended.md) | 2026-10-16 | The request is laid out for the provider's cache, and a change is appended |
| [007](007-the-context-is-a-value-built-every-turn-and-a-team-can-replace-it.md) | 2026-10-16 | The Context is a value built every Turn, which a team can replace and inspect |
| [008](008-agents-are-flat-and-the-application-composes-them.md) | 2026-10-16 | Agents are flat, and the application composes them |
| [009](009-one-loop-extended-at-fixed-stages.md) | 2026-10-16 | One Loop, extended at fixed stages, with a concrete type until a second one exists |
| [010](010-gotato-is-a-light-library-with-a-versioned-contract.md) | 2026-10-16 | Gotato is a light library, tested with fake models, with a versioned contract |
| [011](011-an-agents-bounds-are-its-tools-and-its-effect-cap.md) | 2026-10-16 | An Agent's bounds are its named tools and its Effect cap, and tool output is data |
| [012](012-a-run-stops-through-its-context-and-the-record-is-the-trace.md) | 2026-10-16 | A Run is stopped through its context, and the Record is the trace |
| [013](013-a-conformance-suite-ships-with-the-store-contract.md) | 2026-10-16 | A conformance suite ships with the store contract |
| [014](014-the-effect-cap-hides-tools-above-it.md) | 2026-10-16 | A tool above the Effect cap is hidden from the model |
| [015](015-only-the-agent-sets-the-effect-cap.md) | 2026-10-16 | Only the Agent sets the Effect cap |
| [016](016-gotato-session-is-the-live-unit-and-session-record-is-what-the-store-keeps.md) | 2026-10-16 | `gotato.Session` is the live unit, and `session.Record` is what the store keeps |
| [017](017-the-record-is-an-append-only-log.md) | 2026-10-16 | The Record is an append-only log |
| [018](018-events-are-typed-written-before-sent-and-never-awaited.md) | 2026-10-16 | Events are typed values, written before they are sent, and no subscriber slows a Session |
| [019](019-one-writer-per-session-claimed-in-the-store.md) | 2026-10-16 | One writer per Session, by a claim with an epoch |
| [020](020-no-session-is-bound-to-an-instance.md) | 2026-10-16 | No Session is bound to an instance, and many instances behave like one |
| [021](021-messages-reach-the-writer-and-take-effect-in-its-process.md) | 2026-10-16 | Steer lands at a Turn boundary, Interrupt and Stop at once, and a late message starts a new Run |
| [022](022-a-session-is-a-goroutine-over-its-record.md) | 2026-10-16 | A Session is a goroutine over its Record, cheap to drop and rebuild |
| [023](023-a-mistake-the-model-can-fix-is-a-tool-result.md) | 2026-10-16 | A mistake the model can fix is a tool result, and every Run settles with a status and a cause |
| [024](024-gotato-builds-no-infrastructure.md) | 2026-10-16 | Correctness is Gotato's, routing is the infrastructure's |
| [025](025-callers-credentials-and-quotas-belong-to-the-service.md) | 2026-10-16 | A caller's identity, credential and quota belong to the service around Gotato |
| [026](026-gotato-ships-no-machine-no-client-and-no-tool-catalog.md) | 2026-10-16 | Gotato ships no machine, no command-running tool, no tool catalog and no client |
| [027](027-instances-coordinate-through-the-stores-conditional-writes.md) | 2026-10-16 | Instances coordinate through the store's conditional writes, with one liveness row each |
| [028](028-every-step-is-written-as-facts-and-a-log.md) | 2026-10-17 | Every step is written as it happens, as facts and a log |
| [029](029-reasoning-goes-back-for-the-current-round.md) | 2026-10-18 | Reasoning goes back for the current Round where the provider allows it |
| [030](030-every-entry-is-an-event.md) | 2026-10-18 | Every Entry is an Event, and the Record is the Session's Event log |
| [031](031-a-notice-is-a-message-of-its-own-kind.md) | 2026-10-18 | A notice is a Message of its own kind, kept in the Record |
| [032](032-compaction-closes-a-segment-and-never-rewrites-one.md) | 2026-10-19 | Compaction closes what came since the last Checkpoint into a Segment, and never rewrites a Segment |
| [033](033-code-builds-a-segment-and-the-model-writes-only-the-recap.md) | 2026-10-19 | Code builds a Segment, and the Agent's own model writes only the Recap |
| [034](034-a-segment-is-written-once.md) | 2026-10-24 | A Segment is written once, and a Checkpoint points to it |
| [035](035-the-runtime-says-whether-an-error-ran-and-whether-to-retry.md) | 2026-10-25 | The runtime sets whether an error ran and whether a retry may succeed |
| [036](036-a-warm-copy-is-a-cache-not-a-lock.md) | 2026-10-25 | A warm copy is a cache with a head check, not a lock |
| [037](037-when-the-hash-missed-the-answer-names-the-writer.md) | 2026-10-25 | When the hash missed, the answer names the writer |
| [038](038-a-limit-guards-a-real-failure.md) | 2026-10-25 | A limit guards a real failure, and sizes follow the model's window |
| [039](039-tools-load-one-by-one-from-a-catalog.md) | 2026-10-25 | A Tool is the unit of loading, found through a catalog |
| [040](040-a-large-tool-result-is-spilled-and-read-in-pages.md) | 2026-10-25 | A large Tool Result is spilled into the Record, and read in pages |
| [041](041-a-read-only-bash-for-large-results-is-another-project.md) | 2026-10-27 | A bash for large results lives in its own project |
