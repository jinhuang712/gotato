<!-- what-we-build: drafted 2026-10-29 -->
# Gotato — Designs

Every capitalized term on this page is defined in [GLOSSARY.md](GLOSSARY.md), which also gives each term's name in the code.

Gotato is designed around nine principles, in three groups. A principle is a question to ask of every new requirement, and it is kept here without the forks that follow from it: each fork someone could reasonably reverse is a decision in [plan/decisions](plan/decisions/README.md), with its problem, its choice, what we did not choose and what it costs. Each principle below lists the decisions that follow it and the specs that show how it works.

**What Gotato guarantees**

1. **The store is the only truth.** A step counts once it is in the store, and the live Session, the Context, the bytes of each request and the Events are derived from it, so every instance derives the same thing at any moment.
2. **A request only grows at its end.** Each request is the one before with new content after it, and no byte already sent changes, so the provider serves most of every request from its prompt cache.
3. **The Context is a bounded view.** What the model sees is built for it on every Turn, not the log, and it fits the model's window whether the Session has lived a day or several weeks.
4. **A failure stays local.** A failure costs the step it happened in and no more: a mistake the model can fix becomes a tool result, and no failure loses the conversation.

**What Gotato is**

5. **Keep subtracting.** Gotato does the least it takes to run Sessions in the cloud, and anything new has to show that Gotato cannot do without it.
6. **Stay inside the boundary.** Routing, authentication, databases, telemetry and user interfaces are needed, but the service around Gotato already has them, so Gotato leaves seams for them instead of building them.
7. **Serve purpose-built agents.** Gotato is built for agents whose tools the team writes and whose jobs it can predict, with their bounds in their definition and defaults that err on the safe side.

**How Gotato runs**

8. **No instance is special.** Instances coordinate only through the store, and one instance is only the simplest case of many.
9. **A Session can be stopped and seen.** Every Run can be stopped at any point, and what it did and what it cost are computed from its Events.

The four guarantees are asked of every decision, whatever its principle: a decision about tools or instances can still lose a step, change a byte already sent, or let the Context grow without bound. This page describes the design we intend, not the state of the code. What is built is in [FEATURES.md](FEATURES.md), what comes next is in [ROADMAP.md](plan/ROADMAP.md), and beliefs are in [PHILOSOPHY.md](PHILOSOPHY.md).

## 1. The store is the only truth

A Session's Record in the store is the one copy of what happened. A step counts only once it is written there, and the live Session, the Context, the bytes of each request and the Events a client reads are all derived from it. Two instances, or one instance a week later, derive the same thing from the same Record, so a live Session is a goroutine that can be dropped and rebuilt, not a process kept until someone destroys it. A library user who runs a Session with no store gives this up knowingly. The store keeps the original: what a request needs changed is changed when the request is built, and only what is written once to be reused as it is, such as a Segment, is stored in its final form.

**Ask:** if this instance stopped right now, would anything be lost, and would another instance derive the same thing from the Record?

- **Decisions:** [002](plan/decisions/002-the-store-is-the-truth-and-memory-is-a-cache.md) the store is the truth; [017](plan/decisions/017-the-record-is-an-append-only-log.md) an append-only Record; [028](plan/decisions/028-every-step-is-written-as-facts-and-a-log.md) facts and a log; [030](plan/decisions/030-every-entry-is-an-event.md) every Entry is an Event; [018](plan/decisions/018-events-are-typed-written-before-sent-and-never-awaited.md) typed Events, written before sent; [022](plan/decisions/022-a-session-is-a-goroutine-over-its-record.md) a Session is a goroutine; [034](plan/decisions/034-a-segment-is-written-once.md) a Segment is written once; [036](plan/decisions/036-a-warm-copy-is-a-cache-not-a-lock.md) a warm copy is a cache; [016](plan/decisions/016-gotato-session-is-the-live-unit-and-session-record-is-what-the-store-keeps.md) Session and Record types; [013](plan/decisions/013-a-conformance-suite-ships-with-the-store-contract.md) a conformance suite.
- **How it works:** [The Record](specs/record.md), [a Session](specs/session.md), [Events](specs/events.md).

## 2. A request only grows at its end

At scale, what the model reads costs more than what the service holds ([PHILOSOPHY.md → 4](PHILOSOPHY.md#4-where-is-agent-as-a-service-going)). A provider serves the part of a request it has seen from its prompt cache, at a fraction of the price, while every byte of it is the same. So what never changes comes first, what grows comes last, and a change Gotato makes itself is told at the end instead of rewritten at the top. The same Record giving the same bytes is not enough: a rule that sorted the tools again at every load would still make the provider read everything after them at full price. A few decisions change bytes already sent, on purpose and at a known moment (the last Round's reasoning, a compaction's tool list, a merge of the oldest Segments), and each says why the cost is worth it.

**Ask:** does this change any byte the provider has already been sent?

- **Decisions:** [006](plan/decisions/006-the-request-is-laid-out-for-the-cache-and-a-change-is-appended.md) the request is laid out for the cache; [031](plan/decisions/031-a-notice-is-a-message-of-its-own-kind.md) notices; [029](plan/decisions/029-reasoning-goes-back-for-the-current-round.md) reasoning for the current Round; [039](plan/decisions/039-tools-load-one-by-one-from-a-catalog.md) tools load from a catalog; [034](plan/decisions/034-a-segment-is-written-once.md) a Segment is written once; [032](plan/decisions/032-compaction-closes-a-segment-and-never-rewrites-one.md) compaction closes a Segment.
- **How it works:** [the Context](specs/context.md), [tools](specs/tools.md), [the model seam](specs/model.md).

## 3. The Context is a bounded view

What the model sees is a value built on every Turn, not the history sent as it is. A `ContextBuilder` chooses and lays it out so it can be inspected, and it stays within the model's window however long the Session lives. A Session can last weeks, its user often comes back after the provider's cache has expired, and a long Run can outgrow the window. Compaction is part of the design, not an option: with no settings at all, Segments have a budget, so a long conversation never grows until every request is refused.

**Ask:** after weeks of use, does the model's view still fit its window, and still hold what the model needs?

- **Decisions:** [007](plan/decisions/007-the-context-is-a-value-built-every-turn-and-a-team-can-replace-it.md) the Context is a built value; [032](plan/decisions/032-compaction-closes-a-segment-and-never-rewrites-one.md) compaction closes a Segment; [033](plan/decisions/033-code-builds-a-segment-and-the-model-writes-only-the-recap.md) code builds a Segment; [034](plan/decisions/034-a-segment-is-written-once.md) a Segment is written once; [040](plan/decisions/040-a-large-tool-result-is-spilled-and-read-in-pages.md) large results are spilled; [029](plan/decisions/029-reasoning-goes-back-for-the-current-round.md) reasoning for the current Round; [031](plan/decisions/031-a-notice-is-a-message-of-its-own-kind.md) notices.
- **How it works:** [the Context](specs/context.md), [compaction](specs/compaction.md), [tools](specs/tools.md).

## 4. A failure stays local

A Session runs with nobody watching each step, so something will fail: a model call, a tool, the store, a client, an instance. A failure costs the step it happened in and no more. A mistake the model can fix goes back to it and the Run goes on; a Run that cannot go on settles and says why; a slow reader slows only itself; and no failure loses the conversation, because the Record already holds every step before it.

**Ask:** when this fails, what does it cost beyond itself?

- **Decisions:** [023](plan/decisions/023-a-mistake-the-model-can-fix-is-a-tool-result.md) mistakes and settling; [035](plan/decisions/035-the-runtime-says-whether-an-error-ran-and-whether-to-retry.md) ran, and retry; [038](plan/decisions/038-a-limit-guards-a-real-failure.md) a limit guards a real failure; [018](plan/decisions/018-events-are-typed-written-before-sent-and-never-awaited.md) typed Events, written before sent; [027](plan/decisions/027-instances-coordinate-through-the-stores-conditional-writes.md) conditional writes and liveness.
- **How it works:** [errors](specs/errors.md), [the model seam](specs/model.md), [a Session](specs/session.md).

## 5. Keep subtracting

Gotato does the least it takes to run Sessions in the cloud. Every addition is weight that each service embedding Gotato carries for as long as it does, so the default answer to a new feature is no. A better model improves the Loop with no code change, while every node, interface and plugin point is code someone keeps.

**Ask:** can a Go service that embeds an agent work without it? If it can, it stays out.

- **Decisions:** [008](plan/decisions/008-agents-are-flat-and-the-application-composes-them.md) flat Agents; [009](plan/decisions/009-one-loop-extended-at-fixed-stages.md) one Loop; [010](plan/decisions/010-gotato-is-a-light-library-with-a-versioned-contract.md) a light library; [038](plan/decisions/038-a-limit-guards-a-real-failure.md) a limit guards a real failure; [001](plan/decisions/001-one-implementation-per-wire-api.md) one implementation per wire API; [041](plan/decisions/041-a-read-only-bash-for-large-results-is-another-project.md) a bash is another project.
- **How it works:** [a Session and its Loop](specs/session.md), [the model seam](specs/model.md).

## 6. Stay inside the boundary

Gotato is a library inside your service. Routing, authentication, databases, telemetry and user interfaces are all needed, and your service already has them. Gotato does its own job well and leaves seams for theirs. Where *Keep subtracting* asks whether something is needed at all, this principle asks whose job it is: routing is needed, it is just not Gotato's.

**Ask:** is this a job only an agent runtime has, or one the service around it already does?

- **Decisions:** [024](plan/decisions/024-gotato-builds-no-infrastructure.md) no infrastructure; [003](plan/decisions/003-gotato-owns-the-shapes-and-adapters-own-storage.md) shapes and adapters; [025](plan/decisions/025-callers-credentials-and-quotas-belong-to-the-service.md) callers, credentials, quotas; [037](plan/decisions/037-when-the-hash-missed-the-answer-names-the-writer.md) the answer names the writer; [010](plan/decisions/010-gotato-is-a-light-library-with-a-versioned-contract.md) a light library; [001](plan/decisions/001-one-implementation-per-wire-api.md) one implementation per wire API; [026](plan/decisions/026-gotato-ships-no-machine-no-client-and-no-tool-catalog.md) no machine, client or catalog.
- **How it works:** [many instances](specs/multi-instance.md), [the model seam](specs/model.md), [tools and MCP](specs/tools.md).

## 7. Serve purpose-built agents

Gotato is built for agents whose tools the team chooses and writes, and whose jobs it can predict ([PHILOSOPHY.md → 3](PHILOSOPHY.md#3-how-is-a-cloud-session-different-from-a-local-one)). A general assistant or a coding agent in the cloud needs a machine and a person standing by, and Gotato provides neither. Its defaults err on the safe side: a tool that declares no Effect is taken as destructive, and an Agent never runs a tool above its cap.

**Ask:** does a purpose-built agent need this, or only a general assistant?

- **Decisions:** [004](plan/decisions/004-an-agent-is-a-definition-and-a-session-is-the-live-unit.md) Agent and Session; [016](plan/decisions/016-gotato-session-is-the-live-unit-and-session-record-is-what-the-store-keeps.md) Session and Record types; [005](plan/decisions/005-no-approval-system.md) no approval system; [011](plan/decisions/011-an-agents-bounds-are-its-tools-and-its-effect-cap.md) bounds and tool output; [014](plan/decisions/014-the-effect-cap-hides-tools-above-it.md) the cap hides tools; [015](plan/decisions/015-only-the-agent-sets-the-effect-cap.md) only the Agent sets the cap; [026](plan/decisions/026-gotato-ships-no-machine-no-client-and-no-tool-catalog.md) no machine, client or catalog; [041](plan/decisions/041-a-read-only-bash-for-large-results-is-another-project.md) a bash is another project.
- **How it works:** [tools](specs/tools.md), [a Session](specs/session.md).

## 8. No instance is special

Gotato is designed for many instances behind your load balancer. One instance is the simplest case of that design, and a caller cannot tell which instance served it. Instances coordinate only through the store, so no instance holds anything another lacks. This principle adds the most machinery, so *Keep subtracting* applies inside it: Gotato picks the least machinery that is still correct on many instances.

**Ask:** is this still right when a request reaches any instance?

- **Decisions:** [019](plan/decisions/019-one-writer-per-session-claimed-in-the-store.md) one writer per Session; [027](plan/decisions/027-instances-coordinate-through-the-stores-conditional-writes.md) conditional writes and liveness; [020](plan/decisions/020-no-session-is-bound-to-an-instance.md) no Session bound to an instance; [021](plan/decisions/021-messages-reach-the-writer-and-take-effect-in-its-process.md) messages reach the writer; [036](plan/decisions/036-a-warm-copy-is-a-cache-not-a-lock.md) a warm copy is a cache; [024](plan/decisions/024-gotato-builds-no-infrastructure.md) no infrastructure; [037](plan/decisions/037-when-the-hash-missed-the-answer-names-the-writer.md) the answer names the writer.
- **How it works:** [many instances](specs/multi-instance.md), [the Record](specs/record.md).

## 9. A Session can be stopped and seen

A cloud Session runs with nobody watching each step. So it can be stopped at any point, and it leaves enough behind to see what it did and what it cost: every Run can be canceled through its `ctx`, and the Events and `Usage` of a Run are the only source of its numbers. The Context can be inspected with the code a Turn runs.

**Ask:** if this step goes wrong, do the Events show it, and can it be stopped?

- **Decisions:** [012](plan/decisions/012-a-run-stops-through-its-context-and-the-record-is-the-trace.md) stop through ctx, the Record is the trace; [018](plan/decisions/018-events-are-typed-written-before-sent-and-never-awaited.md) typed Events, written before sent; [021](plan/decisions/021-messages-reach-the-writer-and-take-effect-in-its-process.md) messages reach the writer; [030](plan/decisions/030-every-entry-is-an-event.md) every Entry is an Event; [007](plan/decisions/007-the-context-is-a-value-built-every-turn-and-a-team-can-replace-it.md) the Context is a built value.
- **How it works:** [Events](specs/events.md), [a Session](specs/session.md), [the Context](specs/context.md).

## When principles collide

When two principles pull apart, this order decides:

1. **The store is the only truth.** A lost conversation breaks trust in the service.
2. **A request only grows at its end.** What the model reads is most of what a Session costs, and a byte changed in the prefix makes the provider read everything after it again.
3. **Keep subtracting.** Weight is paid by every service that embeds Gotato, for as long as it does.

The Effect cap is not part of the trade: an Agent never runs a tool above its cap to save a Turn or a write.
