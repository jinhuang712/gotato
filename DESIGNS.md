# Designs

A message goes into a Session, and a settled result and a stream of Events come out. Between the two a Session is a Loop that calls the model and the tools it asks for, and every step counts only once the store holds it. Six terms carry this page. A **Run** is one request to a Session, from its input to its settled result. A **Turn** is one model call plus the tools it asked for. The **Record** is what the store keeps of a Session: an append-only log of everything that happened. The **Context** is what the model sees on one Turn. An **Event** is something that happened to a Session, stored in the Record and streamed to clients. An **Instance** is one running copy of your service.

Gotato is designed around twelve guidelines, in five groups. A guideline is a question to ask of every new requirement. It is kept here without the forks that follow from it: each fork someone could reasonably reverse is a decision with its problem, its choice, what we did not choose and what it costs. Each guideline below lists its decisions and the specs that show how it works.

**What Gotato guarantees**

1. **The store is the only truth:** a step counts once it is in the store, and everything else is derived from it.
2. **A request only grows at its end:** no byte already sent to the provider changes.
3. **The Context is a bounded view:** what the model sees fits its window however long the Session lives.
4. **A failure stays local:** a failure costs the step it happened in and no more.

**What Gotato is**

5. **Keep subtracting:** Gotato does the least it takes to run Sessions in the cloud, with the fewest terms, names and settings.
6. **Stay inside the boundary:** what the service around Gotato already does is left to it.
7. **What an Agent may do is read from its definition:** its tools and its Effect cap are its bounds, and nothing in a conversation widens them.

**How Gotato runs**

8. **No instance is special:** any Instance can serve any request, and one is the simplest case of many.
9. **A Session can be stopped and seen:** every Run can be stopped, and what it did and cost is computed from its Events.

**How Gotato is built**

10. **Each layer does one job, and each default can be replaced:** a user changes a behavior Gotato chose without forking it.

**What users can rely on**

11. **A Session has one writer at a time:** two Instances never run or write the same Session at once.
12. **The contract is versioned, and a break is written down:** what a service depends on changes only in a versioned break with a before-and-after note.

The four guarantees are asked of every decision, whatever its guideline: a decision about tools or Instances can still lose a step, change a byte already sent, or let the Context grow without bound. This page states the design we intend. Whether it is built is for FEATURES.md and ROADMAP.md to say.

## 1. The store is the only truth

A Session's Record in the store is the one copy of what happened. A step counts only once it is written there, and the live Session, the Context, the bytes of each request and the Events a client reads are all derived from it. Two Instances, or one Instance a week later, derive the same thing from the same Record, so a live Session can be dropped and rebuilt at any moment. The store keeps the original, and a change that a request needs is made when the request is built. A library user who runs a Session with no store gives this up knowingly.

It costs a write before each step counts, and a store that keeps the contract of conditional writes.

**Ask:** if this Instance stopped right now, would anything be lost, and would another Instance derive the same thing from the Record?

- **Decisions:** [002](plan/decisions/002-the-store-is-the-truth-and-memory-is-a-cache.md) the store is the truth; [017](plan/decisions/017-the-record-is-an-append-only-log.md) an append-only Record; [028](plan/decisions/028-every-step-is-written-as-facts-and-a-log.md) facts and a log; [030](plan/decisions/030-every-entry-is-an-event.md) every Entry is an Event; [018](plan/decisions/018-events-are-typed-written-before-sent-and-never-awaited.md) typed Events, written before sent; [022](plan/decisions/022-a-session-is-a-goroutine-over-its-record.md) a Session is a goroutine; [034](plan/decisions/034-a-segment-is-written-once.md) a Segment is written once; [036](plan/decisions/036-a-warm-copy-is-a-cache-not-a-lock.md) a warm copy is a cache; [016](plan/decisions/016-gotato-session-is-the-live-unit-and-session-record-is-what-the-store-keeps.md) Session and Record types; [013](plan/decisions/013-a-conformance-suite-ships-with-the-store-contract.md) a conformance suite; [049](plan/decisions/049-the-store-contract-holds-only-what-every-store-must-do.md) the store contract.
- **How it works:** [the Record](specs/record.md), [saving](specs/store.md), [a Session](specs/session.md), [Events](specs/events.md).

## 2. A request only grows at its end

At scale, what the model reads costs more than what the service holds ([PHILOSOPHY.md → 4](PHILOSOPHY.md#4-where-is-agent-as-a-service-going)). A provider serves the part of a request it has seen from its prompt cache at a fraction of the price, while every byte of it is the same. So what never changes comes first, what grows comes last, and a change Gotato makes itself is told at the end and not rewritten at the top. The same Record giving the same bytes is not enough: a rule that reordered the tools at every load would make the provider read everything after them at full price. A few decisions change bytes already sent, on purpose and at a known moment, and each says why the cost is worth it.

It costs freedom to tidy: old content stays where it is, and every correction is a new entry at the end.

**Ask:** does this change any byte the provider has already been sent?

- **Decisions:** [006](plan/decisions/006-the-request-is-laid-out-for-the-cache-and-a-change-is-appended.md) the request is laid out for the cache; [031](plan/decisions/031-a-notice-is-a-message-of-its-own-kind.md) notices; [029](plan/decisions/029-reasoning-goes-back-for-the-current-round.md) reasoning for the current Round; [039](plan/decisions/039-tools-load-one-by-one-from-a-catalog.md) tools load from a catalog; [034](plan/decisions/034-a-segment-is-written-once.md) a Segment is written once; [032](plan/decisions/032-compaction-closes-a-segment-and-never-rewrites-one.md) compaction closes a Segment; [045](plan/decisions/045-the-gateway-is-layered-and-each-layer-can-be-replaced.md) a model bridge in layers; [046](plan/decisions/046-compaction-is-a-fixed-frame-around-parts-a-user-can-replace.md) compaction as a frame with parts.
- **How it works:** [the Context](specs/context.md), [tools](specs/tools.md), [the model seam](specs/model.md).

## 3. The Context is a bounded view

What the model sees is a value built on every Turn, not the history sent as it is. It is built so that it can be inspected, and it stays within the model's window however long the Session lives. A Session can last weeks, its user often comes back after the provider's cache has expired, and a long Run can outgrow the window. So compaction, which closes old history into a Segment with a short recap at its end, is part of the design and not an option: with no settings at all, a long conversation never grows until every request is refused. What leaves the Context stays in the Record and can be read back.

It costs detail: a recap can lose a fact, and reading an original back is a tool call.

**Ask:** after weeks of use, does the model's view still fit its window, and still hold what the model needs?

- **Decisions:** [007](plan/decisions/007-the-context-is-a-value-built-every-turn-and-a-team-can-replace-it.md) the Context is a built value; [032](plan/decisions/032-compaction-closes-a-segment-and-never-rewrites-one.md) compaction closes a Segment; [033](plan/decisions/033-code-builds-a-segment-and-the-model-writes-only-the-recap.md) code builds a Segment; [034](plan/decisions/034-a-segment-is-written-once.md) a Segment is written once; [040](plan/decisions/040-a-large-tool-result-is-spilled-and-read-in-pages.md) large results are spilled; [043](plan/decisions/043-compaction-moves-content-out-and-never-makes-it-unreadable.md) moved content stays readable; [029](plan/decisions/029-reasoning-goes-back-for-the-current-round.md) reasoning for the current Round; [031](plan/decisions/031-a-notice-is-a-message-of-its-own-kind.md) notices.
- **How it works:** [the Context](specs/context.md), [compaction](specs/compaction.md), [tools](specs/tools.md).

## 4. A failure stays local

A Session runs with nobody watching each step, so something will fail: a model call, a tool, the store, a client, an Instance. A failure costs the step it happened in and no more. A mistake the model can fix goes back to it and the Run goes on. A Run that cannot go on settles and says why. A slow reader slows only itself. No failure loses the conversation, because the Record already holds every step before it.

It costs an answer for every failure: each error says whether anything ran and whether a retry may succeed, and every layer has to give it.

**Ask:** when this fails, what does it cost beyond itself?

- **Decisions:** [023](plan/decisions/023-a-mistake-the-model-can-fix-is-a-tool-result.md) mistakes and settling; [035](plan/decisions/035-the-runtime-says-whether-an-error-ran-and-whether-to-retry.md) ran, and retry; [038](plan/decisions/038-a-limit-guards-a-real-failure.md) a limit guards a real failure; [018](plan/decisions/018-events-are-typed-written-before-sent-and-never-awaited.md) typed Events, written before sent; [027](plan/decisions/027-instances-coordinate-through-the-stores-conditional-writes.md) conditional writes and liveness.
- **How it works:** [errors](specs/errors.md), [the model seam](specs/model.md), [a Session](specs/session.md).

## 5. Keep subtracting

Gotato does the least it takes to run Sessions in the cloud. Every addition is weight that each service embedding Gotato carries for as long as it does, so the default answer to a new feature is no. A better model improves the Loop with no code change, while every node, interface and extension point is code someone keeps.

The weight is what a user has to learn and depend on, not the lines it takes: the terms of the glossary, the exported names, the settings, the modules it pulls in and the footprint it leaves. A guarantee makes code, and that code is fine when it is the least that keeps the guarantee. A feature that stays is still built with the fewest names and settings that do its job, and a setting with no reason is a default fixed in code.

It costs the features a team would have liked: what a Go service can do without goes to the application built on Gotato.

**Ask:** can a Go service that embeds an agent work without it? If it can, it stays out. If it stays, can it do its job with fewer terms, names or settings?

- **Decisions:** [008](plan/decisions/008-agents-are-flat-and-the-application-composes-them.md) flat Agents; [009](plan/decisions/009-one-loop-extended-at-fixed-stages.md) one Loop; [010](plan/decisions/010-gotato-is-a-light-library-with-a-versioned-contract.md) a light library; [038](plan/decisions/038-a-limit-guards-a-real-failure.md) a limit guards a real failure; [001](plan/decisions/001-one-implementation-per-wire-api.md) one implementation per wire API; [041](plan/decisions/041-a-read-only-bash-for-large-results-is-another-project.md) a bash is another project; [052](plan/decisions/052-the-footprint-is-a-measure-and-has-no-limit.md) the footprint has no limit; [053](plan/decisions/053-minimal-is-measured-in-what-a-user-learns-and-depends-on.md) minimal is measured in what a user learns.
- **How it works:** [a Session and its Loop](specs/session.md), [the model seam](specs/model.md).

## 6. Stay inside the boundary

Gotato is a library inside your service. Routing, authentication, databases, telemetry and user interfaces are all needed, and your service already has them. Gotato does its own job well and leaves seams for theirs, and a transport such as HTTP is an optional adapter beside the layers and not part of them. Where *Keep subtracting* asks whether something is needed at all, this guideline asks whose job it is: routing is needed, it is just not Gotato's.

It costs the batteries: a team supplies its own store, telemetry, authentication and clients, and Gotato keeps the seams for them stable.

**Ask:** is this a job only an agent runtime has, or one the service around it already does?

- **Decisions:** [024](plan/decisions/024-gotato-builds-no-infrastructure.md) no infrastructure; [003](plan/decisions/003-gotato-owns-the-shapes-and-adapters-own-storage.md) shapes and adapters; [025](plan/decisions/025-callers-credentials-and-quotas-belong-to-the-service.md) callers, credentials, quotas; [037](plan/decisions/037-when-the-hash-missed-the-answer-names-the-writer.md) the answer names the writer; [010](plan/decisions/010-gotato-is-a-light-library-with-a-versioned-contract.md) a light library; [001](plan/decisions/001-one-implementation-per-wire-api.md) one implementation per wire API; [026](plan/decisions/026-gotato-ships-no-machine-no-client-and-no-tool-catalog.md) no machine, client or catalog; [050](plan/decisions/050-each-package-is-one-layer-named-for-its-job.md) a transport is an adapter.
- **How it works:** [the map](specs/ARCHITECTURE.md), [hosting](specs/host.md), [the model seam](specs/model.md), [MCP tools](specs/mcp.md), [the HTTP API](specs/http.md).

## 7. What an Agent may do is read from its definition

What an Agent may do is written in one place, its definition, where a reviewer reads it: its named tools and the highest Effect its tools may have, the Effect cap. Nobody watches each step of a Session, so nothing in a conversation can widen these bounds. A Session can switch off a tool of its Agent and never add one or lift the cap. A Tool Result reaches the model as data and never as the system's instruction. A tool whose Effect (how much it can change) is not declared counts as destructive. A job that runs again may only narrow its Agent. Why Gotato is for agents whose tools and jobs the team knows is in [PHILOSOPHY.md → 3](PHILOSOPHY.md#3-how-is-a-cloud-session-different-from-a-local-one).

It costs reach: Gotato does less than a general agent, and a team writes careful tools because the tools are the bounds.

**Ask:** can a conversation, a Tool Result or a caller widen what this Agent may do?

- **Decisions:** [004](plan/decisions/004-an-agent-is-a-definition-and-a-session-is-the-live-unit.md) Agent and Session; [005](plan/decisions/005-no-approval-system.md) no approval system; [011](plan/decisions/011-an-agents-bounds-are-its-tools-and-its-effect-cap.md) bounds and tool output; [014](plan/decisions/014-the-effect-cap-hides-tools-above-it.md) the cap hides tools; [015](plan/decisions/015-only-the-agent-sets-the-effect-cap.md) only the Agent sets the cap; [026](plan/decisions/026-gotato-ships-no-machine-no-client-and-no-tool-catalog.md) no machine, client or catalog; [044](plan/decisions/044-an-agent-profile-and-a-task-spec-are-registered-inline-or-from-a-store.md) a job only narrows its Agent.
- **How it works:** [tools](specs/tools.md), [definitions](specs/profile.md), [a Session](specs/session.md).

## 8. No instance is special

Gotato is designed for many Instances behind your load balancer. One Instance is the simplest case of that design, and a caller cannot tell which Instance served it. Instances coordinate only through the store, so no Instance holds anything another lacks. This guideline adds the most machinery, so *Keep subtracting* applies inside it: Gotato picks the least machinery that is still correct on many Instances.

It costs machinery that a single Instance would not need: a claim on each Session, a liveness record for each Instance and conditional writes in the store.

**Ask:** is this still right when a request reaches any Instance?

- **Decisions:** [020](plan/decisions/020-no-session-is-bound-to-an-instance.md) no Session bound to an Instance; [036](plan/decisions/036-a-warm-copy-is-a-cache-not-a-lock.md) a warm copy is a cache; [024](plan/decisions/024-gotato-builds-no-infrastructure.md) no infrastructure; [049](plan/decisions/049-the-store-contract-holds-only-what-every-store-must-do.md) the store contract; [050](plan/decisions/050-each-package-is-one-layer-named-for-its-job.md) a host needs a definition in code.
- **How it works:** [hosting](specs/host.md), [saving](specs/store.md), [the Record](specs/record.md).

## 9. A Session can be stopped and seen

A cloud Session runs with nobody watching each step. So it can be stopped at any point, and it leaves enough behind to see what it did and what it cost: every Run can be canceled by its caller, and the Events and the usage of a Run are the only source of its numbers. The Context can be inspected, built by the code a Turn runs.

It costs a stored Event for each step and the discipline to count nothing outside the Events.

**Ask:** if this step goes wrong, do the Events show it, and can it be stopped?

- **Decisions:** [012](plan/decisions/012-a-run-stops-through-its-context-and-the-record-is-the-trace.md) stop through the context, the Record is the trace; [018](plan/decisions/018-events-are-typed-written-before-sent-and-never-awaited.md) typed Events, written before sent; [021](plan/decisions/021-messages-reach-the-writer-and-take-effect-in-its-process.md) messages reach the writer; [030](plan/decisions/030-every-entry-is-an-event.md) every Entry is an Event; [007](plan/decisions/007-the-context-is-a-value-built-every-turn-and-a-team-can-replace-it.md) the Context is a built value.
- **How it works:** [Events](specs/events.md), [a Session](specs/session.md), [the Context](specs/context.md).

## 10. Each layer does one job, and each default can be replaced

Gotato is a library, and a library is used in ways its authors did not foresee. So each layer does one job and knows only the layer below it: the packages run from the host through the definitions, the store and the Record down to the core, each importing only those below it, and a test fails when one does not. A definition is not mixed with runtime policy, a model's facts are not mixed with the connection to it, and a transport's shapes are not mixed with the runtime's.

Wherever Gotato decides something for its user, such as where a definition comes from, when to compact, which wire API to speak or how to authenticate, the decision is a default behind a small interface. The user can replace it without forking Gotato or reimplementing the layer around it. A default may be held in code or loaded from a store, and both go through the same seam.

Nothing a user plugs in can bypass a layer that owns a guarantee: an option cannot replace the part that writes the Record, and a value read from a store cannot raise an Agent above its cap. Each value has one owner and one typed way in, and any other way in is refused before anything is written, never dropped or overridden. *Keep subtracting* decides whether a behavior exists at all, and this guideline decides how a behavior that exists is built.

It costs a contract for every seam: each interface stays stable once it ships, and a replaceable part needs a frame that keeps the guarantees whatever it returns.

**Ask:** can a user replace this default without copying the code around it, and does this type hold one layer's concerns?

- **Decisions:** [003](plan/decisions/003-gotato-owns-the-shapes-and-adapters-own-storage.md) shapes and adapters; [009](plan/decisions/009-one-loop-extended-at-fixed-stages.md) one Loop, extended at fixed stages; [013](plan/decisions/013-a-conformance-suite-ships-with-the-store-contract.md) a conformance suite; [044](plan/decisions/044-an-agent-profile-and-a-task-spec-are-registered-inline-or-from-a-store.md) profiles inline or from a store; [045](plan/decisions/045-the-gateway-is-layered-and-each-layer-can-be-replaced.md) a model bridge in layers; [046](plan/decisions/046-compaction-is-a-fixed-frame-around-parts-a-user-can-replace.md) compaction as a frame with parts; [047](plan/decisions/047-a-value-a-layer-owns-is-set-only-through-that-layer.md) a value set only through its layer, any other way refused; [048](plan/decisions/048-the-runner-and-the-session-are-built-from-parts-and-host-plumbing-is-internal.md) parts with one job; [049](plan/decisions/049-the-store-contract-holds-only-what-every-store-must-do.md) the store contract; [050](plan/decisions/050-each-package-is-one-layer-named-for-its-job.md) one package per layer; [051](plan/decisions/051-a-session-is-opened-for-a-task-spec-when-it-is-created.md) a TaskSpec Session opened at create.
- **How it works:** [the map](specs/ARCHITECTURE.md), [definitions](specs/profile.md), [who may set what](specs/session.md#who-may-set-what), [a Session's parts](specs/session.md#a-session-is-built-from-parts), [a Runner's parts](specs/host.md#a-runner-is-built-from-parts), [the model bridge](specs/model.md#the-model-bridge-is-four-layers), [the compaction frame](specs/compaction.md#the-frame-and-its-parts).

## 11. A Session has one writer at a time

However many Instances serve requests, one Instance writes a Session at any moment. The writer holds a Claim in the Record, and the store refuses a write from an Instance that lost it, so two Instances never run or write the same Session at once. A message that reaches another Instance is not run there: the answer names the writer. Gotato gets this from the store's conditional write alone. *No instance is special* says any Instance may serve a request, and this guideline says only one of them writes.

It costs a wait after a crash: another Instance takes over only when the dead writer's liveness row has expired, and the step in flight is lost.

**Ask:** can two Instances write or run this Session at the same time?

- **Decisions:** [019](plan/decisions/019-one-writer-per-session-claimed-in-the-store.md) one writer per Session; [027](plan/decisions/027-instances-coordinate-through-the-stores-conditional-writes.md) conditional writes and liveness; [021](plan/decisions/021-messages-reach-the-writer-and-take-effect-in-its-process.md) messages reach the writer; [037](plan/decisions/037-when-the-hash-missed-the-answer-names-the-writer.md) the answer names the writer.
- **How it works:** [hosting](specs/host.md), [saving](specs/store.md).

## 12. The contract is versioned, and a break is written down

Services embed Gotato and upgrade on their own schedule, so what they depend on is named and versioned: the exported names of the core and standard runtime packages, the Event types and their fields, the HTTP contract with its own version, the store contract, and the places where many Instances behave differently from one. An addition is free. A break is rare, raises the version it touches, and comes with a before-and-after entry in the changelog.

It costs speed and surface: a change that suits Gotato may wait for a release, and every exported name is a promise, so Gotato exports little.

**Ask:** does this change something a user depends on, and if so, does it carry a version and a before-and-after note?

- **Decisions:** [010](plan/decisions/010-gotato-is-a-light-library-with-a-versioned-contract.md) a light library with a versioned contract; [048](plan/decisions/048-the-runner-and-the-session-are-built-from-parts-and-host-plumbing-is-internal.md) host plumbing leaves the public API.
- **How it works:** [the map](specs/ARCHITECTURE.md), [the HTTP API](specs/http.md).
