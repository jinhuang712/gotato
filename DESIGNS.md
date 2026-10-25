<!-- what-we-build: drafted 2026-10-26 -->
# Gotato — Designs

Every capitalized term on this page is defined in [GLOSSARY.md](GLOSSARY.md), which also gives each term's name in today's code.

Gotato is designed around nine principles, in three groups.

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

A principle is a question to ask of every new requirement. The four guarantees are asked of every decision on this page, whatever its section: a decision about tools or instances can still lose a step, change a byte already sent, or let the Context grow without bound. Each section below names its question, then lists the decisions that follow from the principle: what was chosen, what was not, and why. The decisions that weighed several options keep their table and their date. After the principles comes the order that decides when two of them pull apart.

This page describes the design we intend, not the state of the code. What is built, and how each feature works, is in [FEATURES.md](FEATURES.md). Open design questions are in [ROADMAP.md](ROADMAP.md), and beliefs are in [PHILOSOPHY.md](PHILOSOPHY.md).

## 1. The store is the only truth

A Session's Record in the store is the one copy of what happened. A step counts only once it is written there, and everything else is derived from it: the live Session that runs it, the Context the model sees, the bytes of each request and the Events a client reads. Two instances, or one instance a week later, derive the same thing from the same Record. A live Session is a goroutine running its Record, not a process kept until someone destroys it, so dropping it loses nothing and any instance rebuilds it cheaply. A library user who runs a Session with no store gives this up knowingly: the live Session's Transcript is then all there is.

The store keeps the original. What a request needs changed, such as text that imitates a tag, is changed when the request is built, so a rule that changes later applies to the whole history. Only what is written once to be reused as it is, such as a Segment (a stretch of history a compaction closed), is stored in its final form.

**Ask:** if this instance stopped right now, would anything be lost, and would another instance derive the same thing from the Record?

### The store is the truth, and memory is a cache — 2026-10-15

A Session behaves like a live unit while it is in use, and like a record while it waits.

| Option | For | Against |
|---|---|---|
| **A live unit kept warm, with the store as the truth** (chosen) | Holds a Session's queue, stream and status while it works. Survives the loss of any instance. The TTL only frees memory. | A write per step. Machinery to load and to let go. |
| Nothing in memory: load and save around every Run (today's code) | The fewest moving parts. | The Session has no queue of its own, so a message that arrives mid-Run can only wait or be rejected. |
| A long-lived unit in memory that owns its state | Fast, and natural for queues and streams. | Lost with its instance. Pins each conversation to one instance. |

An earlier version of Gotato kept agents in memory for their whole life, and a later one replaced them with a stored record. This decision brings a long-lived unit back. It is safe this time, because a Session's identity is its record, not the memory that holds it.

### A warm copy is a cache, not a lock — 2026-10-25

After a Run, the live Session can stay in memory, so the next message finds its queue, its tools and its Transcript ready, and an idle timer can compact it before its provider's cache expires. The question is what the copy is allowed to claim while it waits ([FEATURES.md → G-F27](FEATURES.md#g-f27--service-many-instances-)).

| Option | For | Against |
|---|---|---|
| **A cache with a head check before each Run** (chosen) | The copy holds no Claim while idle, so any instance can take the Session over, as it does today, and nothing is pinned. A stale copy costs one read of the head and a reload. Memory stays reclaimable: the longest-idle copy can be evicted at once. | A read of the head before each Run on a warm copy. |
| The warm copy holds the Claim | No head check: the copy is always current. | An idle Session is bound to one instance for its whole TTL. A takeover must wait for it or fence it, a deploy must release every idle Session, and an instance holding many idle Sessions cannot give memory back without a write. It brings back the pin that [No Session is bound to an instance](#no-session-is-bound-to-an-instance) rules out. |
| No warm copy: load and save around every Run (today's code) | The fewest moving parts. | A load for every Run. A message that arrives between Runs has no Session to meet, and an idle compaction needs a service timer of its own. |

- **The head check.** Before a Run on a warm copy, the instance compares the Record's head in the store with the copy's. Unchanged, the copy runs. Changed, because another instance wrote the Session, the instance reloads from the last Checkpoint. The Claim's conditional write still fences any race between the check and the Run, as for every append.
- **The idle compaction is the same compaction.** The Session's own timer calls what a service's timer calls before the warm Session (see [principle 3](#compaction-closes-a-segment-before-the-cache-goes-cold--2026-10-19)). A message that arrives cancels it and is served first.
- **The memory limit evicts before it refuses.** Warm Sessions count toward the activation limit. At the limit, the longest-idle warm Session goes first, which loses nothing because the store is the truth, and a Run is refused only when every live Session is running.

### A Session's identity is its Record

- **Instead of:** a live object with an identity of its own.
- **Why:** the Session ID is its Record's ID, so whichever instance builds the Session, it is the same Session.

### The Record is an append-only log — 2026-10-16

A save had to cost the size of what changed, and a stale copy had to fail instead of overwriting a newer one.

| Option | For | Against |
|---|---|---|
| **Append only, with checkpoints for compaction** (chosen) | A save costs one step. Two writers cannot append the same sequence number. Every adapter maps it to an insert. Nothing is lost to compaction. | The Record grows; storage is bounded by the team. |
| Rewrite the whole Record, with a version check | The simplest contract. | A save costs the whole Session, megabytes for long ones. |
| Append, plus a rewrite for compaction | Compaction stays a replacement. | Two write paths, and the rewrite brings the cost back. |

### The Record keeps every step, as facts and a log — 2026-10-17

A crash should lose as little as it can, and a rebuild should read only what it needs.

| Option | For | Against |
|---|---|---|
| **Every step, written as it happens, as facts and a log** (chosen) | A crash loses at most the step in flight: the last second of a reply, or the result of the tool that was running. A tool that started is known to have started. A rebuild reads only the facts. | A write for every step: eight for a short Run with one tool call. |
| Every Turn, at its end | One write per Turn. | A crash loses the Turn in flight, and with it the record that a tool started. |
| Every Run, at its end | One write per Run. | A crash loses the whole Run. |

- **Facts** are what a rebuild reads: Messages, the tool calls that started, metadata changes, claims and Checkpoints.
- **Log entries** record what happened: a Run's start and end, its usage, model calls, retries, a release, and the parts of a streaming reply, written every second or few KiB. A rebuild does not read them, except to answer a crash: when a Run never ended, a load also reads the log since the last Checkpoint for the parts of that Run's reply.
- **Every Entry is an Event.** Facts and log entries are what a Session's Events report, so the Record is the Session's Event log and nothing is kept twice ([principle 7](#every-step-is-an-event-and-a-stored-event-is-an-entry--2026-10-18)).
- **The Context is never stored.** Every Turn builds it from the facts, so a rebuilt Session sends what it would have sent.
- **What a crash leaves is answered when the Session is read, and, but for one case, nothing is written for it.** A tool call that started has an unknown outcome, one that never started was not run, and a reply cut off mid-stream is kept as a cut-off reply that the model never sees again. The cut-off reply is the one exception to writing nothing: its parts are in the log, so the Instance that takes the Session over rebuilds it from them and writes it as a fact, and every later load has it.

### Every step is an Event, and a stored Event is an Entry — 2026-10-18

Events are facts a program can read, instead of log lines. A Session's Record and its Events tell the same story: kept apart, they say it twice under two sets of names, and a number that counts within one Run means nothing to another instance.

| Option | For | Against |
|---|---|---|
| **Every Entry is an Event, and the Record is the Session's Event log** (chosen) | One set of names, one copy. An Event's number is its Entry's position, which every instance reads the same, so a stream resumes anywhere. | A break: the number counts the Record, not the Run, so the HTTP, gRPC and CLI contracts change version. |
| Events replace the log entries only | Half the duplication goes. | Facts and Events still say the same things twice. |
| Events kept apart from the Record | No change. | Everything is said twice, and only one instance can resume a stream. |

Every Event carries its time and the Session it concerns, if any; a stored Event also carries its number, and an Event of a Run its Run and its Turn. Events come in three kinds, by where they live:

- **Stored Events** are Entries: every change of state the Writer sees, such as a Run starting, a reply ending, a Tool starting, a message queued or a Claim.
- **Deltas** are sent and never stored: text, reasoning and tool progress. What they carry ends up whole in a Message or a Tool Result.
- **Instance Events** happen where no Record can take them, because the Instance holds no Claim: a request waiting or refused, a fenced write, a Liveness row that lapsed, a failed write, the time a load or save took. They go only to that Instance's subscribers.

### An Event is written before it is sent

- **Instead of:** sending an Event first, and writing it with the next step.
- **Why:** a client never holds an Event the Record lacks, so a resumed stream never shows one number with two meanings. Most Events ride the write of their step, which delays them by the write alone. The few that fall between steps, such as a retry, an Abort request or a queued message, get a write of their own, because they are rare. Deltas are sent at once, marked with the number of the Event they follow.

### A load starts from the last Checkpoint

- **Instead of:** replaying the whole history.
- **Why:** rebuilding a Session costs the part since its last Checkpoint, so a Session that lives long stays cheap to rebuild.

### The Context depends only on the Record and the Agent

- **Instead of:** state that lives on one instance.
- **Why:** every instance builds the same Context for a Session, so the provider's cache serves it whichever instance sends it.

### A live Session is a goroutine

- **Instead of:** a process, a subprocess or a sandbox for each Session.
- **Why:** a goroutine costs kilobytes and starts in microseconds, so one instance holds many Sessions, most of them idle.

### A Session holds only what is cheap to rebuild

- **Instead of:** a Session that opens its own subprocesses or holds its own long-lived connections.
- **Why:** anything expensive in a Session makes dropping it expensive. Model clients and connections belong to the instance, and its Sessions share them.

### Sessions are built when needed

- **Instead of:** a pool of prepared Sessions.
- **Why:** building one is cheap enough that a pool would only add state to keep right.

### An idle Session leaves memory at its TTL

- **Instead of:** staying in memory until someone destroys it.
- **Why:** memory is a cache, so an idle Session gives it back, after 60 minutes unless its Agent sets another TTL, or sooner when the instance needs the room: at its memory limit it evicts the longest-idle Session first ([A warm copy is a cache, not a lock](#a-warm-copy-is-a-cache-not-a-lock--2026-10-25)). It is compacted shortly before the TTL, or before its provider's cache expires if that comes first, unless a setting turns this off (see [principle 3](#compaction-closes-a-segment-before-the-cache-goes-cold--2026-10-19)).

### A Fork writes the store and activates nothing

- **Instead of:** copying a live Session in memory.
- **Why:** the new Session is a new Record, so whichever instance its first request reaches activates it.

### A stream resumes from a position

- **Instead of:** a stream that must reconnect to the same instance, or start over.
- **Why:** an instance can go away at any time. A stored Event's number is its Entry's position in the Record, which every instance reads the same. A client reconnects to any instance with the number of the last Event it saw, gets every later Event from the Record and then the live ones, and the stream goes on without gaps or duplicates.
- **The Record's epoch.** A number means the same Event only within one Record. A Record deleted and created again under the same ID would give a client holding number 40 the 41st Event of another conversation, so every Record has an epoch, chosen when it is created and kept in `session_created` and in its Summary, and a client that sends the epoch it saw is refused when it is not the Record's. We took it from `session_created` and the Summary because both are written already. An adapter keeps it with the rest of the Summary, and the store contract test checks that it does.
- **An open stream follows one epoch.** An instance keeps a Feed for each Record, named by its Session ID and its epoch, not one for each Session ID, so a stream is never handed another Record's Events, and a new Record's streams never wait for the old one's numbers. A stream ends, instead of going on with another Record's numbers, once it learns that its Record was deleted or created again: at once when its own instance deletes the Session, or opens, creates or streams the new Record, and elsewhere at a read of the store, since an idle stream reads only what follows its number.
- **Deltas and the end of a stream.** A delta is not a position, and what it carried ends up whole in a Message, so a reconnect does not send again what was sent before it. A stream that ends says whether to come back and how long to wait, spread at random, so a lost instance does not bring every client back in the same instant.
- **Any instance serves it.** An idle stream reads the Record at intervals, which is the cost of not binding a Session to an instance. The read does not take the stream out of its live mode, so a delta that arrives meanwhile is not lost, and on the writer it finds nothing new. The load balancer's hash keeps most streams on the writer, where Events arrive as they are written.

## 2. A request only grows at its end

At scale, what the model reads costs more than what the service holds ([PHILOSOPHY.md → 4](PHILOSOPHY.md#4-where-is-agent-as-a-service-going)). A provider serves the part of a request it has seen before from its prompt cache, at a fraction of the price, as long as every byte of that part is the same. So each request is the one before with new content after it: what never changes comes first, what grows comes last, and a change is told at the end instead of rewritten at the top.

The same Record giving the same bytes (*The store is the only truth*) is not enough on its own. A rule that sorted the tools again whenever a tool was loaded would give the same bytes for the same Record, and still make the provider read everything after the tools at full price.

A few decisions change bytes already sent, on purpose and at a known moment: the last Round's reasoning is left out once a new Round starts, a compaction changes the tool list, a load adds a tool to the list on a wire API that cannot defer tools natively, and the oldest Segments are merged when they pass their budget. Each says so, and why the cost is worth it.

**Ask:** does this change any byte the provider has already been sent?

### Stable content first, growing content last

- **Instead of:** an order by time or by kind.
- **Why:** the instruction and the Static blocks come first, then the tools sorted by ID, then the Transcript. Because the Record only grows at its end, one Turn's prefix is the next Turn's prefix.

### Nothing that changes every Turn goes into the request

- **Instead of:** a dynamic system prompt, or the current time in every request.
- **Why:** such content is never cached, so it costs its full price on every Turn. A fact the model needs now comes from a tool it calls.

### A change is appended, not rewritten into the prefix

- **Instead of:** rewriting the instruction or the tool list at the top of the request.
- **Why:** a change at the top makes the provider read the whole history again at full price. A change is told to the model in a notice, the next decision.

### What is not the user's words goes to the model as a notice — 2026-10-18

Gotato and the service sometimes have to tell the model something the user did not say: a setting changed, the last Run was interrupted, the user has been away for hours, or something happened outside the conversation. A notice is how.

| Option | For | Against |
|---|---|---|
| **A notice is a Message of its own kind, kept in the Record** (chosen) | The model never takes it for the user, and it never starts a Round, so the reasoning sent back stays. It is appended once and never changes, so the cache and Claude's check of thinking blocks hold. | A fourth kind of Message, and each wire API needs its own form for it. |
| A tagged user Message | The simplest. | It starts a new Round, so Claude and OpenAI lose the reasoning in flight, and the model may take it as the user's request. |
| Text added to each request, never kept | Nothing stored. | Its place and words can change between requests and instances, which breaks the cache, and Claude refuses a thinking block whose prefix changed. |

- **Triggered by Events.** Gotato's own notices follow the Events that cause them, such as `run_ended` or `metadata_changed`, and each names its cause. A notice is still kept as its own Message: its words must never change, it goes in where the conversation allows one rather than where its Event happened, and a service's notice has no Event to come from.
- **One queue with Steer.** A notice waits in the queue that holds a Steer, in the order they arrived, and the queue is emptied right before every model call. So a notice always follows a user Message or a Turn's Tool Results, and never sits between a tool call and its result, which every provider refuses.
- **A notice never starts or extends a Run.** It tells the model something; it asks for no work. A notice that arrives while no Run is going waits for the next one, and goes in after its prompt. A service that wants the model to react at once calls `Continue`.
- **Each wire API in its own form.** Each wire API has a default form: a system message mid-conversation on Anthropic Messages for the models the gateway lists, a developer message on OpenAI's Responses API, and tagged text in the adjacent user Message on Chat Completions. The gateway configuration can override it, and tagged text is the fallback for every model, so no model list is a rule ([FEATURES.md → G-F29](FEATURES.md#g-f29--runtime-notices-)).
- **Facts, not orders.** A notice says what is true; what the model should do about it belongs in the instruction.
- **The time away is one of them.** A message that starts a Run after the Session was idle longer than a threshold (30 minutes by default, set per Agent) is followed by a notice with how long it has been and what time it is now. The model has no clock of its own, and a Segment's times are not the present. The idle time is read from the Record, so it does not depend on a warm copy.

### Cache markers are neutral in the core

- **Instead of:** one provider's cache syntax in the core.
- **Why:** the core marks where a provider may cache, and each wire API maps the marks to its own form, such as Anthropic's `cache_control`.

### One cache key per Session, inherited by a Fork

- **Instead of:** a key per request or per instance.
- **Why:** a provider that keys its cache serves one conversation from one cache, on whichever instance it runs. A Fork shares its parent's prefix, so it shares the key.

### Cache retention is set where the provider is configured

- **Instead of:** a retention on each Agent, or one value for every provider.
- **Why:** how long a provider keeps its cache, and what that costs, belongs to the provider: an hour on Claude, at twice the price of a cache write; 30 minutes on OpenAI's Responses API from GPT-5.6, which is also its default. Each model's gateway configuration sets it (`cache_retention`), and without a setting a recognized provider gets its longest. One environment variable, `GOTATO_GATEWAY_CACHE_RETENTION`, sets it for every model, and it wins over each model's own, so a service changes it in one place. An unrecognized provider is sent no retention, because a compatible server may refuse a field it does not know.

### Tools load one by one from a catalog — 2026-10-25

The tool list comes before the Transcript, so every tool definition in it is read on every request, and a change to it makes the provider read everything after it again. A service that connects MCP servers can offer hundreds of tools. The model should see the few it needs, and loading one should cost as little of the cache as it can ([FEATURES.md → G-F34](FEATURES.md#g-f34--tool-search-)).

| Option | For | Against |
|---|---|---|
| **A Tool is the unit of loading, found through a catalog in the description of one discovery tool** (chosen) | The model loads only what it uses. The catalog is the same bytes while tools load, and a provider's deferred loading keeps the tool list the same bytes too. A ToolSet is a grouping and a source, and a team chooses how much of it the catalog shows. | A search tool to call before the first use, and a fallback for the wire API that has no deferred loading. |
| A ToolSet is the unit, switched on whole (the earlier form) | Few concepts. | A big ToolSet, such as one MCP server, fills the tool list the moment it is switched on, and a count limit on active ToolSets is a guess. |
| Every tool always visible | Nothing to discover. | Hundreds of definitions in every request, and a model that chooses badly among them. |

- **No catalog budget.** The catalog shows what the install mode says, and a team that installs a big ToolSet folds it. A budget would be a number somebody guessed ([A limit guards a real failure](#a-limit-guards-a-real-failure--2026-10-25)).
- **A loaded tool stays until the next compaction.** The tool list changes at that moment anyway, so the model keeps what it called in the Segment being closed or in the kept tail, and the rest unloads.
- **Loading is a fact of the Record,** so every instance offers the same tools after a load, as the store is the only truth.
- **A ToolSet resolves at the start of every Run, never inside one.** A server's tools change (an MCP server's `list_changed`), and a Session can live across many Runs, so the catalog is read again at each Run. Within a Run it is fixed, so the request of every Turn is built from the same catalog. A ToolSet that fails is skipped with a warning, not fatal: one server being down must not stop the others. A loaded tool that its ToolSet no longer carries is unloaded by a stored fact (`tool_unloaded`, `gone`), so the store stays the only truth and every instance builds the same request from the same Record.
- **The cap and the switch come first.** A tool above the Agent's Effect cap is never in the index, and a tool a Session switched off is never loaded: a model cannot search its way past a bound ([The bounds are in the definition](#the-bounds-are-in-the-definition)).

### A Segment is written once, and a Checkpoint points to it — 2026-10-24

- **Instead of:** copying every Segment's Messages into each Checkpoint, or storing a Segment as data and rendering it when a request is built.
- **Why:** a provider's prompt cache reads bytes, and a Segment is only useful if its bytes never change. A Segment is appended once, as its own `segment_closed` Entry, with its rendered Messages and its data: its number, its times, the range of the Record it covers, its tool call lines, its Recap and the tools it used. A Checkpoint lists the numbers of the Segments the Context uses and keeps the tail, with the tail's own Messages, because dropping reasoning rewrites them when the Checkpoint is written. The Entries a Segment covers stay in the log, unread by the Context and readable by their range. Rendering at request time would let a change in the rules change the bytes, and copying would write the same Messages again at every compaction.
- **What it costs.** A load reads every `segment_closed` Entry before the last Checkpoint, including one a later Checkpoint no longer lists, so a load grows with the history. A Record that holds Segments is read only by a Gotato that knows them.

## 3. The Context is a bounded view

What the model sees is a value built on every Turn, not the history sent as it is. A `ContextBuilder` chooses and lays it out, so it can be inspected, and it stays within the model's window however long the Session lives. A Session can last weeks, its user often comes back long after the provider's cache has expired, and a long Run can outgrow the window. Compaction keeps the view bounded and keeps as much of the conversation as it can, and it is part of the design rather than an option: with no settings at all, the Segments have a budget, so a long conversation never grows until every request is refused.

**Ask:** after weeks of use, does the model's view still fit its window, and still hold what the model needs?

### The Transcript and the Context are separate values

- **Instead of:** sending the history to the model as it is.
- **Why:** a `ContextBuilder` builds the Context from the Transcript on every Turn, so what the model sees can be chosen, laid out and inspected.

### Full history by default, and your own `ContextBuilder` for the rest

- **Instead of:** built-in retrieval or memory search.
- **Why:** retrieval and windowing depend on the application's data. Gotato ships full history, Static blocks and Compaction.

### A large Tool Result is spilled, and read in pages — 2026-10-25

A tool can return far more than a Context should hold, and the Context is built from the Record on every Turn. A result over one page, about 8K tokens, would stay in every later request, or be refused for its size ([FEATURES.md → G-F35](FEATURES.md#g-f35--large-tool-results-)).

| Option | For | Against |
|---|---|---|
| **Spill the body into the Record as an Entry of its own, keep a preview and a ref in the Context, and let the model read it in pages** (chosen) | The Context stays small. Nothing is lost: the Record keeps the whole body, and the Message that stays is written once, so the bytes already sent do not change. A load does not read the body. | A tool call to read more, and a body that costs a read from the store. |
| Fail the Run past a size limit (the earlier form) | Simple. | A result the model could have read in pages ends the Run, at a size that has nothing to do with the window. |
| Cut the result and drop the rest | Simple, and no extra call. | The model cannot get what was cut, and cannot tell whether it matters. |

- **The Record is the truth, and a warm Session is a cache.** Any instance reads the same body from the Record, and a warm copy only keeps the bodies it has read.
- **One page is the line.** The threshold and the page of the reader are the same, so a result either fits in the Context or is read page by page.
- **The reader is a sibling of `read_segment`.** The two share their paging. `read_segment`'s name is written into every Segment already stored, whose bytes never change, so the name stays, and a second tool reads results.

### Reasoning goes back for the current Round, where the provider allows it — 2026-10-18

A thinking model writes a lot of reasoning, and some models think on every reply. A Session can live for weeks, so reasoning sent back on every Turn fills the window, and the bill, long after it stopped helping. A Round is a user Message and every reply and Tool Result after it.

| Option | For | Against |
|---|---|---|
| **The current Round's reasoning to Claude and OpenAI, all of it to any other provider** (chosen) | Reasoning stops piling up where the provider allows it. The rule reads only the Record, so every instance sends the same bytes. DeepSeek and unrecognized providers work as they always did. | Once per Round, the last Round's replies are cached again without their reasoning. DeepSeek and unrecognized providers still carry every reasoning artifact until compaction. |
| All reasoning to every provider | The prefix only ever grows. | A weeks-long Session carries all of its reasoning, and Claude's newer models keep and bill all of it. |
| No reasoning at all | The smallest requests. | Claude and OpenAI need the reasoning of a tool loop in progress, and DeepSeek refuses the request. |

- **Where a Round starts.** At the last user Message. A Steer is a user Message, so it starts a new Round. A Continue without a new prompt stays in the Round it resumes. Tool Results have a role of their own and never start one, and neither does a notice, because it is not the user speaking.
- **Which provider.** The gateway's configuration names it (`provider`). Without that, the gateway recognizes only a provider's own address, such as `api.anthropic.com`. It never guesses from a model name, because behind a gateway a model name may be an alias for anything.
- **Why Claude accepts it.** Claude's newest models check that every thinking block is sent after the same system prompt, tools and Messages it was written under. What is left out is always every block from the start of the Transcript up to the Round, and a block once left out is never sent again, so the check holds for every block that is sent. Where the check runs, the gateway also asks Claude to drop a block whose prefix changed instead of refusing the request; `no_drop_block` turns this off.
- **What it costs the cache.** A new Round changes the request from its first thinking block on, once. On Claude, the gateway marks the cache where the last Round started, so the new request still reads the cache up to there. Right after a compaction, that marker moves to the end of the last Segment, so the next compaction's request can read the Segments before from the cache ([below](#compaction-closes-a-segment-before-the-cache-goes-cold--2026-10-19)).
- **A change of model.** Reasoning goes as it is, whichever model wrote it. The provider drops what its model cannot read. Turning reasoning into text instead would make the new model take it for its own answer.
- **Compaction.** A Segment, the stretch of history a compaction closes ([below](#compaction-closes-a-segment-before-the-cache-goes-cold--2026-10-19)), holds no reasoning. The tail a compaction keeps goes to Claude and OpenAI without its reasoning, because the Segment changes the prefix that reasoning was written under, and to DeepSeek and unrecognized providers with it. What Claude and OpenAI need of the reasoning in a tail kept mid-Run, whose tool loop is still going, is a default for each wire API that the gateway configuration can override.

### Compaction closes a Segment, before the cache goes cold — 2026-10-19

A Session's user often comes back long after the provider's cache has expired, and a long Run can outgrow the model's window. Compaction has to keep as much of the conversation as it can, and make the first request after a long pause cheap. A Segment is one stretch of history that a compaction closed, and its Recap is the part the model writes ([FEATURES.md → G-F06](FEATURES.md#g-f06--compaction-)).

| Option | For | Against |
|---|---|---|
| **Close what came since the last Checkpoint into a Segment, and never rewrite a Segment** (chosen) | No summary of a summary until the oldest Segments merge, so nothing drifts. The Segments before keep their bytes, so a provider that caches any prefix reads them from the cache while the tool list stays the same; on Claude that needs a cache marker at the end of the last Segment, which takes the place of the marker at the last Round's start right after a compaction, since Claude takes at most four markers (played by a fake gateway in ROADMAP Phase 10). It matches a Record that only grows. | Segments add up over a long Session, so at last the oldest are merged, which rewrites the prefix once. A merge writes one Recap of the oldest Recaps, and the words of the Segments it merged stay readable with `read_segment`. |
| One summary of everything before the kept tail, written again at every compaction | One block, whose size stays bounded. | Each compaction summarizes the summary before it, so early facts blur or drop out, and the whole prefix changes every time. |
| The provider's own compaction | No code in Gotato. | Its result cannot be read, does not carry over to another model, differs from one provider to the next, and cannot close one Segment at a time. It may become an option later. |

- **Before the cache goes cold.** An idle Session is compacted shortly before its prompt cache expires, or before it leaves memory if its TTL comes first, or if the cache retention is unknown or not well above the lead, where the TTL alone sets the time. The request that writes the Recap still reads the cache, and the user who comes back later pays full price only for a smaller Context.
- **Safe by default.** Segments have a token budget, so the oldest are merged when they pass it, and on an overflow too. They merge into one merged Segment that opens the Context, down to half the budget, so the far past is one Recap and only it is a summary of a summary. With no `context_window` and no ceiling there is still a default budget, 32K tokens, so a long conversation cannot grow until every request is refused.
- **The ceiling is the exception.** A Run about to outgrow the model's window is compacted while the cache is still warm, mid-Run if need be, because it cannot go on otherwise. The same goes for a request the provider refuses as too long, which is compacted and sent again once, and for a compaction asked for on demand.
- **Nothing is lost,** unless the team prunes the entries before a Checkpoint. The Record keeps every Message a Segment covers, and the model can read a Segment back in full with a built-in tool; after such a prune, the tool says the Segment's Messages were removed.
- **The tools change with it.** Only the loaded tools used in the Segment being closed, and those a kept tail still calls, stay loaded ([Tools load one by one from a catalog](#tools-load-one-by-one-from-a-catalog--2026-10-25)). The tool list comes before the Segments, so a change to it means the next request reads none of them from the cache. A compaction is when that costs least: it changes the request anyway, and after an idle compaction the cache has usually expired by the time the user comes back.

### Code builds a Segment, and the model writes only the Recap at its end

- **Instead of:** asking a model to summarize the whole stretch.
- **Why:** what code can keep exactly, it keeps. The user's messages and each Run's final reply stay word for word, and each tool call becomes one line inside its Run, so every answer shows the calls it came from; reasoning, a Run's intermediate text and the tool results are left out. The model writes only what code cannot: the Recap, with the facts the tool results held, the decisions, what is still open, and where a Run stands. It comes last, so a mistake by the model cannot damage the rest, and the model reads it right before the Messages that follow. Code groups a Segment's Messages by the Run that committed them: a Run's final reply is its last assistant Message without tool calls, and a Message that came from no Run, such as history a Record started with, is kept or left out by its own rule, not grouped.

### The Agent's own model writes the Recap, on the cached prefix

- **Instead of:** a separate model, or a request built only to summarize.
- **Why:** the request is the Context the next model call would send, with one plain user message after it that asks for the Recap, so the whole prefix is read from the cache. That message is never stored and carries no tag, because it asks for work rather than stating a fact. A Recap fails when the reply calls a tool, is cut off at the output limit, does not start with one of the five `## ` headings, uses any other `## ` heading, or has no `## What happened`; a heading with nothing under it may be left out. The compaction still goes on: the Segment is kept without a Recap, with the conversation and its tool call lines, so a mistake by the model never loses the conversation. A merge whose Recap fails leaves the Segments unmerged. A merge is asked from the Recaps alone, and when no model writes it, code writes a digest of the Segments instead, so the budget holds whether or not a team turned the model's Recap on. A library user can bring their own writer of Recaps.

### One hook runs before every model call — 2026-10-24

- **Instead of:** preparing a Run only when it starts.
- **Why:** a Run can outgrow the window between two Turns, and a request can be refused as too long, so compaction has to be able to run before any model call. The Loop calls a `TurnPreparer` on the Session's goroutine before every model call, with no Tool call open, and gives it the Session and Run, the Turn, the Transcript, where the Run started, the request about to be sent, whether this is a retry after a refusal, and a way to write what is staged. Compaction is its first use, and replaces the hook that ran at the start of a Run. The policy (the ceiling, the idle time, the refusal) stays in the compactor; the hook only gives it the chance.

### A notice is for runtime facts, and a Segment has tags of its own

- **Instead of:** a notice that carries a summary of the history, or history and notices as untagged text.
- **Why:** a notice tells the model a fact of the moment, and a compacted stretch of history is something else, so it has names of its own. Gotato writes only four tags into the conversation: `<notice>`, `<segment>` for a Segment's header, `<tool_calls>` for a Run's tool call lines, and `<recap>`. Inside them are Markdown headings, never nested XML, and their attributes hold only what code needs: a number, and times in RFC 3339. They reach each wire API in a notice's forms, and the native forms carry the same tags; with no user Message after a notice or a Recap, the native form carries it, and Chat Completions gets a user message on the wire that the Record keeps as a notice, so it starts no Round (played by fake gateways in ROADMAP Phase 10), so every model reads the same words. When a request holds any of them, the system part ends with one fixed sentence: their text is written by the system, not said by the user. A user Message, a Tool Result, a Recap or a kept assistant reply that holds the start or the end of one of the four tags has its `<` escaped, and a `read_segment` result escapes a line that starts with a role prefix such as `[assistant]`, so neither a user, outside data nor the model's own words can speak as the system.

## 4. A failure stays local

A Session runs with nobody watching each step, so something will fail: a model call, a tool, the store, a client, an instance. A failure costs the step it happened in and no more. A mistake the model can fix goes back to it and the Run goes on; a Run that cannot go on settles and says why; a slow reader slows only itself; and no failure loses the conversation, because the Record already holds every step before it.

**Ask:** when this fails, what does it cost beyond itself?

### A mistake the model can fix becomes a tool result

- **Instead of:** failing the Run.
- **Why:** an unknown tool or bad arguments go back to the model as a failed tool result, and the Run goes on. The Run's deadline, or a `TurnStopper` a team writes, bounds a model that keeps getting it wrong.

### Every Run ends

- **Instead of:** no end, left to the caller, or a count of Turns and tool calls kept by the core.
- **Why:** a Run ends in one of five ways, and each one settles it with a status and a cause:
  - **The model's final reply.** The model answers with no tool call, and the Run is `completed`.
  - **Its `ctx`.** A cancel, or a deadline the caller or the service set, ends every model call, tool call and store call at once. The core has no clock of its own for a Run.
  - **A `TurnStopper`.** An extension asks the Loop to stop after a Turn. A team that wants a cap on Turns, tool calls or tokens writes it there, with the number that fits its own jobs.
  - **The compaction ceiling.** A Context still past the ceiling after the Turn's one compaction ends the Run with `limit_exceeded`, and a provider that still refuses a request as too long ends it with `context_overflow`.
  - **A failure.** The model, the provider, a tool, the store or Gotato itself fails in a way the Run cannot go on from.
- The core counts nothing, neither Turns nor tool calls. A call that hangs is ended by `ModelCallDeadline` or `ToolCallDeadline`, and a Run that never settles by its `ctx`. A service gives each Agent one Run deadline (`service.Agent.RunTimeout`), 30 minutes unless the Agent sets another, which a request's own timeout overrides ([A limit guards a real failure](#a-limit-guards-a-real-failure--2026-10-25)).

### A limit guards a real failure — 2026-10-25

A limit is a promise that something bad cannot happen. A cap that fits no real failure either cuts off work that was going well, or never fires. Gotato had four of them to stop a Run that goes on without end: Turns, tool calls, the size of a Tool Result and a Run's time. Each has a better guard.

| Option | For | Against |
|---|---|---|
| **A limit guards a real failure, and sizes follow the model's window** (chosen) | A cap is there because a named failure needs it: a hung call has its deadline, a Context past the window has its ceiling, a Run that must stop has its `ctx`. A number that depends on the job, such as how many Turns a support case may take, is the team's own `TurnStopper`. | A team that wants a cap on Turns writes a few lines. |
| Default counting caps for Turns and tool calls | Every Run is bounded with no code. | One number cannot fit a lookup and a long investigation. A Run that was working is cut off at an arbitrary Turn, and a team that raises the number loses the guard. |
| A cap on the bytes of a Tool Result | One result cannot fill memory. | It fails a Run for something the model could have read in pages, and it fails it at a size unrelated to the window. |

- **A size follows the window.** A Tool Result over one page, about 8K tokens, is spilled into the Record and read back in pages ([FEATURES.md → G-F35](FEATURES.md#g-f35--large-tool-results-)). The compaction ceiling is the window less a reserve. Neither is a fixed byte count that someone chose.
- **What stays, and why.** `MaxMessageBytes` guards memory against one reply that never ends. `ModelCallDeadline` and `ToolCallDeadline` guard a call that hangs; they are marked for review, because a fixed two minutes may not fit a slow model or a long tool. `MaxMessages` and `MaxTranscriptBytes` guard what one load reads.

### Every Run settles with a status and a cause

- **Instead of:** one generic error.
- **Why:** a failure names the part that caused it: the model, the provider, a tool, the store, a limit, an extension, a cancel or Gotato.

### An error says whether it ran and whether a retry may succeed — 2026-10-25

A caller that gets an error has two questions before it reads the code: did anything happen, and may I try again? The first decides whether the same request can go to another instance without running twice. The second decides whether to wait, or to tell the user. Both answers depend on where the error was made, so that is where we decide them.

| Option | For | Against |
|---|---|---|
| **The runtime sets both answers on the error, and every surface translates** (chosen) | A service that embeds the Runner and never meets HTTP reads the same answers as a client behind a load balancer. A new code is classified once, where it is made. | The runtime names two facts that look like transport concerns. |
| Each surface infers them from the code | Nothing in the runtime changes. | HTTP, gRPC and the CLI each keep a copy, and they drift: a draining instance's refusal was not marked "not run" because only HTTP knew one code that was. An embedding service gets no answer at all. |
| The caller infers them from the code | Simple for the runtime. | Every caller learns which codes are safe, and a new code silently breaks the rule. |

- **Three answers on every error.** The code says what happened. `gotato.NotRun(err)` is true when nothing with side effects happened, so the request is safe to send to another instance. `gotato.Retryable(err)` is true when the same request, sent again later, may succeed. Both look through wrapped errors.
- **Not run is a promise, so the default is no.** A store failure may have run, and a Run that reached the model has run. Only a refusal made before a Run starts says yes: draining, past a limit, busy, no such Session.
- **A Run that started never says `completed` and carries an error.** A Run ends with three writes: the reply, the end of the Run (`run_ended`), and the release of the Session. When the reply or the end cannot be written, the Run settles as `failed` with `store_failure`: the store is the truth ([DESIGNS.md → The store is the only truth](#1-the-store-is-the-only-truth)), so a Run the store does not hold is not a success. When only the release fails, every fact of the Run is stored, so it stays `completed`, and an Event warns the operator. There is no status in between. A caller that reads only the status is not misled. A Run refused before it started has no status at all.
- **Surfaces keep one table from code to status.** HTTP, gRPC and the CLI each map every code on purpose, and a test fails when a code has no entry, so a new code cannot be forgotten.

### A Session never waits for a subscriber

- **Instead of:** observers that the Session's goroutine calls and waits for.
- **Why:** one slow client or exporter would slow every step of the Session. A Session puts each Event in its subscribers' queues and moves on. There are three subscriptions: a Run's stream, a Session's stream from any number, and an Instance's stream, which the service's own telemetry reads. Each subscriber has one goroutine that forwards its Events in order. A subscriber that falls too far behind is no longer buffered in memory: it catches up from the Record by number, because the Record only grows, so only deltas can be skipped. An Instance's stream mixes Sessions and has no single number, so it marks a gap with an Instance Event that says how many it skipped.

### A conflict is found before the model is called

- **Instead of:** finding out after a Turn that it cannot be committed.
- **Why:** a Turn costs money and seconds, so a second instance learns that the Session has a writer before it spends either. A writer writes each model call before it makes it, and checks before each write that its own liveness row was renewed in time, so a writer that lost its claim stops there. One case is beyond any check: an instance paused for longer than its row lives may send one request when it wakes, before it notices. Its result can never be written.

## 5. Keep subtracting

Gotato does the least it takes to run Sessions in the cloud. Every addition is weight that each service embedding Gotato carries for as long as it does, so the default answer to a new feature is no.

**Ask:** can a Go service that embeds an agent work without it? If it can, it stays out.

### One Loop, and the model decides the steps

- **Instead of:** graphs, workflows and nodes.
- **Why:** a better model makes the Loop better with no code change, while every node is code that someone keeps ([PHILOSOPHY.md → 1](PHILOSOPHY.md#1-what-actually-changed)). A fixed sequence is written in Go around the Loop.

### Agents are flat

- **Instead of:** sub-agents, supervisors, workers or any other role as a type.
- **Why:** roles and lineage are how an application composes agents, and they live in Session metadata.

### No orchestration between agents

- **Instead of:** multi-agent orchestration, an agent-to-agent protocol such as A2A, or worktrees.
- **Why:** composing agents is ordinary Go code in the application, which already knows its own tasks.

### A Session stays generic

- **Instead of:** typed fields for tasks, projects or workflows.
- **Why:** each application keeps its own structure in metadata, so one Session type serves them all.

### No long-term memory

- **Instead of:** a built-in memory architecture.
- **Why:** what to remember across Sessions differs by application. Gotato provides Sessions, the Context and Compaction, and memory is built on them.

### One implementation means a concrete type

- **Instead of:** an interface drawn before a second implementation exists.
- **Why:** an interface with one implementation is a guess about the future. A caller that wants a fake defines its own small interface, as Go code usually does.

### Extensions wrap the Loop at fixed stages

- **Instead of:** a plugin framework, or a fork of the Loop.
- **Why:** a fixed set of stages keeps the Loop one path that a reader can follow. A need that fits no stage waits for a new stage, added as a reviewed change.

### Library first

- **Instead of:** a required server, daemon or database.
- **Why:** a small `main` with a fake model runs an agent in-process, so the service and the store are choices, not the price of entry.

### Tests never call a network model

- **Instead of:** tests that depend on a live model.
- **Why:** fake and replay models keep every test deterministic, and CI runs with no provider credentials.

### Dependencies point inward

- **Instead of:** behavior that only the CLI or only the service has, or a core that knows about the service.
- **Why:** every behavior lives in the packages first, so the library, the CLI and the service behave the same. Claims, liveness and drain live in the service layer and the store contract, not in the core. `layering_test.go` keeps each layer building only on the layers inside it:

```text
cmd/gotato, gRPC adapter  ──►  service, service/httpapi  ──►  session, modelctx, toolregistry, testkit  ──►  core
                                           MCP adapter  ──────────────────────────────────────────────────►  core
```

The MCP adapter only supplies Tools, so it builds on the core alone and no runtime package imports it.

### The core uses only the standard library

- **Instead of:** vendor SDKs or other libraries that save some code, or one module that carries every dependency.
- **Why:** every dependency lands in every service that embeds Gotato. The root package imports only the standard library, the root module requires only `gopkg.in/yaml.v3`, and the gRPC adapter and the MCP adapter are modules of their own.

### Providers are configured by wire API — 2026-09-17

A provider is configuration: an endpoint, a key, a wire API and its models. Gotato implements each wire API once (`openai-chat-completions`, `openai-responses` and `anthropic-messages`), and any vendor that speaks one of them works without new code. A few providers have rules of their own for reasoning and the cache, so the configuration may also name the provider ([principle 6](#reasoning-goes-back-for-the-current-round-where-the-provider-allows-it--2026-10-18)).

| Option | For | Against |
|---|---|---|
| **One implementation per wire API** (chosen) | A new vendor is a config entry. No vendor SDK in the service. | A vendor with a new wire API waits for its implementation. |
| One adapter per vendor | Each vendor's full feature set. | Code for every vendor, and the list never ends. |
| Vendor SDKs | Maintained by the vendors. | Their dependencies land in every service. |

## 6. Stay inside the boundary

Gotato is a library inside your service. Routing, authentication, databases, telemetry and user interfaces are all needed, and your service already has them. Gotato does not replace them: it does its own job well, and leaves seams for theirs. Where *Keep subtracting* asks whether something is needed at all, this principle asks whose job it is. Routing is needed; it is just not Gotato's.

**Ask:** is this a job only an agent runtime has, or one the service around it already does?

### Gotato builds no infrastructure, and stays within its boundary — 2026-10-16

Running a Session on many instances raises two kinds of problem: getting a request to the right instance, and keeping the Session correct once it is there. The first is infrastructure that every team already runs. Gotato takes only the second: it keeps a Session correct on whichever instance a request reaches, taking over an idle Session and waiting for a busy one, and messages for a running Session reach its writer through the same routing as any request. A caller that cannot route by Session is told where the writer is, and Gotato builds no network between instances to carry the message there.

| Option | For | Against |
|---|---|---|
| **Stay within the boundary: correctness is Gotato's, routing is the infrastructure's** (chosen) | Nothing new to deploy or operate. Works with a plain consistent hash. No Session is ever unreachable. A message that misses the writer is answered `not_writer`, with the writer's advertised address, so the caller or your own client sends it on. | How seamless it feels depends on the team's routing; while a hash changes, a message may need a second request. |
| Forward requests to the writer between instances, always | Every message reaches the writer at once. | Gotato becomes a router, with an internal API and its authentication. What stays is smaller: an opt-in forward by the HTTP and gRPC endpoints, to an address the writer advertised, as the same public request sent once. |
| Redirect the caller to the writer, as the only routing | No forwarding. | Pushes routing onto every caller, and a load balancer cannot follow a redirect to one instance. The hash stays first; the address is only the answer when it missed. |
| Queue every message in the store for the writer | Survives the writer's death. | Gotato becomes a message broker: a write per message, and a writer that polls the store. |
| A placement or membership service of Gotato's own | Central control of where Sessions run. | A separate system to run, which [PROPOSAL.md](PROPOSAL.md) rules out. |

### No authentication

- **Instead of:** built-in authentication, OAuth or a tenant model.
- **Why:** every service already authenticates its callers in its own way, and decides in its own way who may touch which Session. Gotato passes the request's `context.Context`, with its values, to every store and extension call it makes for that request, so the application's own code sees the caller where it enforces its rule. A refusal is `not_found`: a store wrapper returns `session.ErrNotFound` for a Session the caller does not own, which answers 404 on HTTP, `NOT_FOUND` on gRPC and exit 3 on the CLI, and does not reveal that the Session exists. Gotato adds no identity type, no "not allowed" error and no hook, and work it starts on its own, such as an idle compaction, has no caller ([FEATURES.md → G-N22](FEATURES.md#g-n22--access-control-belongs-to-applications)).

### No global scheduling or quotas

- **Instead of:** scheduling resources or provider quotas across services.
- **Why:** that is system policy, and the application owns it. Gotato offers Runs that are cheap and cancellable, bounds its own concurrent Runs, and keeps one Run per Session.

### Gotato owns the shapes, and adapters own storage and telemetry — 2026-10-15

Databases model schemas differently, and teams run different telemetry stacks, so neither belongs in the core.

| Option | For | Against |
|---|---|---|
| **Stable shapes for a Session and its Events, mapped by adapters** (chosen) | Works with any database and any telemetry stack. Adds no dependency to the service. | Each team writes or picks an adapter. |
| Ship a SQL store in the core | Works on day one for SQL users. | Drivers, migrations and one schema for everyone. No help for Mongo. |
| Ship OpenTelemetry in the core | Tracing on day one. | A heavy dependency for every service, including those on another stack. |

### Core types belong to no vendor

- **Instead of:** one vendor SDK's types as the core's types.
- **Why:** the model contract uses Gotato's own request and Event types, and provider data the core does not understand, such as reasoning signatures, passes through untouched.

### No clients and no user interfaces

- **Instead of:** bundled bots, schedulers, front ends or terminal UIs.
- **Why:** callers write their own. Gotato provides the HTTP handler, the gRPC service and the Go API they call, and its own human-facing surface is CLI output.

### Embedding never forces an upgrade

- **Instead of:** following the newest Go release.
- **Why:** a service should not have to upgrade Go to add a library. The root `go.mod` says `go 1.22`.

### A named, versioned contract

- **Instead of:** changes that slip in unannounced.
- **Why:** a service builds against Gotato, so it needs to know what can change under it. The contract is the exported identifiers of the root and standard runtime packages; the CLI's commands, flags, JSON fields and exit codes; the Event types, their names and their fields; the HTTP and gRPC contracts, which carry their own version; the store contract; and the places where many instances behave differently from one. Until the first release, any part of it may change: Gotato keeps no old names and no compatibility shims, and every break has a before-and-after entry in [CHANGELOG.md](CHANGELOG.md), so a break is never silent. From the first release on, additions are free and a break is rare and raises the version. Consumers tolerate unknown Event kinds either way.

## 7. Serve purpose-built agents

Gotato is built for agents whose tools the team chooses and writes, and whose jobs it can predict ([PHILOSOPHY.md → 3](PHILOSOPHY.md#3-how-is-a-cloud-session-different-from-a-local-one)). A general assistant or a coding agent in the cloud needs a machine and a person standing by, and Gotato provides neither. Its defaults err on the safe side: a tool that declares no Effect is taken as destructive, and an Agent never runs a tool above its cap.

**Ask:** does a purpose-built agent need this, or only a general assistant?

### An Agent is a definition, and a Session is the live unit — 2026-10-15

The words now match how the labs' agent APIs use them, so a reader who knows those APIs reads Gotato correctly without a note.

| Option | For | Against |
|---|---|---|
| **Agent = definition, Session = live unit** (chosen) | Matches Claude Managed Agents and the OpenAI Agents API. One Agent running many Sessions reads naturally. | A rename of the code, done in Phase 3. |
| Agent = live unit, with no term "session" | One noun for callers. | The reverse of the labs' usage, so every reader needs a note. |
| Keep the old code names: Agent = the loop for one Run, Session = the stored record | No rename. | The thing a caller talks to has no name of its own. |

### A Session has no machine

- **Instead of:** a shell, a sandbox or a filesystem for each Session.
- **Why:** the work Gotato is for needs tools and data, not a computer ([PHILOSOPHY.md → 2](PHILOSOPHY.md#2-what-belongs-in-a-cloud-session)). Work that needs a machine runs on a laptop, in CI or in a coding product.

### No tool runs commands or code

- **Instead of:** built-in tools that run commands, code or a browser.
- **Why:** Gotato is not a cloud coding agent, and a tool that runs anything is a bound nobody can read.

### Reading a large result needs no machine — 2026-10-26

A Tool Result over one page is spilled into the Record, and the model reads it back in pages ([A large Tool Result is spilled, and read in pages](#a-large-tool-result-is-spilled-and-read-in-pages--2026-10-25)). Often the model wants one line, one field or the lines that match, which a person would do with `grep` or `jq`. Inside a pod with no machine ([A Session has no machine](#a-session-has-no-machine)), the question is what the model is given in its place ([FEATURES.md → G-F36](FEATURES.md#g-f36--reading-large-results-)).

The answer is that the model queries its own Session's Record with a read-only subset of bash: no machine, behavior that matches GNU and bash, and a clear error for anything not supported. It has three goals:

- **Cheap.** Only what the model needs enters the Context.
- **Fast.** A common lookup takes one call.
- **Familiar.** The model learns nothing new, so it is right the first time.

#### The constraints

- No machine, no `exec`, no sandbox. The tool is read-only and sees only its own Session.
- The same Record gives the same output on every instance.
- A command always ends and honours `ctx` cancellation. Memory is bounded.
- **Supported means identical.** Every supported command and flag behaves exactly as GNU's, and every supported piece of syntax exactly as bash. Anything else fails with a clear error that lists what is supported. A silent difference gives the model a wrong answer it cannot notice, while a clear error costs one call.

#### What was weighed

| Option | For | Against |
|---|---|---|
| A machine, container or VM for each Session | Everything a shell does works. | It is the machine this page rules out: a cost per Session, a sandbox to keep safe, and a Session that is no longer cheap to rebuild. |
| OS isolation inside the pod (namespaces, gVisor) | Real commands, with a wall around them. | They need `exec` or privileges that a pod of a service does not have, and a platform team would have to allow them. |
| An in-process WebAssembly sandbox | No privileges needed. | A shrunken machine with its own footprint and cold start in every instance, to do what a few functions do. |
| Code mode, where the model writes code that reads the result | One call can do much. | The model's program, not Gotato, decides the steps, so a Run cannot be replayed step by step. |
| A bash interpreter run as it is | The syntax models know best. | It lets the model express far more than we want to allow: loops that may not end, `exec`, writes. |
| A query language of our own | Exactly what we need. | Models have not seen it, so they use it badly. |
| A virtual bash library with few users | Bash without writing it. | A dependency that few people keep up, in every service that embeds Gotato. |
| Typed reader parameters (`lines`, `grep`, a `json` path) | The standard library only, and each parameter is checked. | The model must learn our parameters, and cannot compose them: what they do not say, it cannot ask. |
| A pipeline language with a parser of our own | Models know the look of it. | Word expansion (quotes, `$var`, globs) is where a re-implementation drifts from bash, and the model cannot see the drift. |
| A filter parameter on every tool call | One call, and the large result never spills. | The model must write the filter before it sees the result, which may be JSON of a shape it does not know. Every tool schema grows a parameter, and MCP schemas would be changed. The full result must be spilled anyway, because a tool with side effects cannot be called again. |
| An own subset of jq | No dependency. | It drifts from jq, which models know best. |
| gron-style flattening of JSON | Plain `grep` finds a field. | Models know jq better, and the flat form is a new shape to learn. |
| Paging only | The simplest. | The model reads all of a result to find one line. Paging stays, as the fallback. |
| **A read-only bash subset over the Session's Record** (chosen) | The model knows it. A common lookup is one pipeline. Nothing runs outside the process. | A set of commands to write and keep, and conformance tests to run. |

#### The decisions

- **Spill first, query after.** The model queries a result it has already seen the start of, and the cost is one more model round trip, only for a result over one page.
- **It sees the Session's own Record as a read-only directory** (an `fs.FS`). `/results/<call_id>` comes first. `/segments/<n>` and `/files/<name>` (the user's files, once files live in the Record) join later under the same rule.
- **Structured data goes through full jq,** by `github.com/itchyny/gojq` (MIT). jq is the JSON tool models know best, and a library that matches it cannot drift. jq can loop, so `ctx` and a bound on the output end it.
- **The syntax is run by `mvdan.cc/sh` (BSD-3), parser and interpreter.** Before a command runs, Gotato walks the syntax tree and refuses loops, functions, command substitution and writing redirects, with an error that suggests a pipeline. Every handler of the interpreter is taken over: command execution goes to Gotato's own Go commands (no `exec`), file opens go to the read-only `fs.FS`, and the environment is empty. Word expansion is where a re-implementation drifts from bash, and the interpreter matches bash there.
- **The commands are written by Gotato.** No Go coreutils is both widely used, GNU-faithful and licensed to embed: u-root aims at small embedded systems and opens OS files, and the Go ports of coreutils are GPL-3.0 and little used. A command earns its place by being what a model reaches for when it reads and looks for data.
- **Protections.** A very long single line, such as minified JSON, is cut with an explicit marker like `[line cut: 1.0 MiB]`, never silently. Output over one page is spilled like any Tool Result, with a preview and a ref, so there is no new concept. Every command answers `--help` with the flags it supports.
- **GNU conformance tests run only in development and CI,** never at runtime.

#### The first commands

| Command | Supported |
|---|---|
| `ls`, `echo`, `base64`, `wc` | |
| `cat` | `-n` |
| `head`, `tail`, `cut`, `tr`, `sort`, `uniq` | |
| `sed` | `-n` with line-number ranges only |
| `grep` | `-i -v -c -n -o -E -F -w -l -r -A -B -C` |
| `jq` | full jq, through gojq |

Later, when a need shows: `awk` (through goawk, MIT), `sed` substitution, `nl`, `column`, `diff` and `comm`, CSV with quoted fields, URL and HTML decoding, here-strings. Never: `date`, `$RANDOM`, environment variables (they break same-Record-same-output), the network, writing files, `xargs`, `tee`, `find -exec`, compiling, git, processes.

#### Where it lives

- **A module of its own** inside the repository, as `adapter/mcp` is: `adapter/bash`. Its engine does not import Gotato (it takes a command string, an `fs.FS` and `ctx`, and returns stdout, stderr and an exit code; it depends on `mvdan.cc/sh`, gojq and the standard library), so it can move to a repository of its own once it is stable.
- **A thin Gotato layer** in the same module wraps the engine as a Tool named `bash`, hands it the Record as `/results`, and spills large output. The tool is described as a read-only bash subset that queries this Session's data, so a model that tries full bash gets a clear error and corrects in one call.
- **The core adds one thing:** it exposes a Session's Record as a read-only `fs.FS`, from the standard library alone. The root module still imports only the standard library. `read_result` stays as the fallback for a service that does not install the module.
- **Review.** When the chat bot of Phase 10 runs, the command list is reviewed against real use. Gotato runs no checks against real providers, so the choice is made on reasoning and reviewed there.

This is not [a tool that runs commands or code](#no-tool-runs-commands-or-code): nothing is executed outside the process, the syntax is checked before it runs, and the tool can read one Session's data and nothing else.

### Tools are Go functions or MCP tools

- **Instead of:** a tool catalog or marketplace that ships with Gotato.
- **Why:** the team chooses and writes its tools. General-purpose tools come from MCP servers and from the application. An MCP server is one ToolSet, brought in by `adapter/mcp`, a module of its own on the official Go SDK, so a service that uses no MCP carries none of it ([FEATURES.md → G-F12](FEATURES.md#g-f12--the-mcp-adapter-)). The adapter maps a server's tools to Tools, takes their Effect from the server's annotations, and builds none of the protocol's other features ([G-N20](FEATURES.md#g-n20--mcp-features-gotato-does-not-build)).

### The bounds are in the definition

- **Instead of:** someone watching each Run, or bounds that change from one conversation to the next.
- **Why:** an Agent's bounds are its named tools and its Effect cap, set in its definition, where a reviewer reads them. A conversation cannot raise them: a Session can switch off one of its Agent's tools, but never add one or lift the cap.

### Every tool declares its Effect, and silence means destructive

- **Instead of:** treating a tool that declares nothing as harmless.
- **Why:** a forgotten declaration should fail closed. The levels, from the least impact to the most, are read, write local, write external and destructive.

### Tools check their own arguments

- **Instead of:** a central policy engine.
- **Why:** a write that is safe for some arguments and not others is best judged by the code that knows them. The tools are the bounds, so they are written with care.

### No approval system — 2026-10-15

Gotato is for purpose-built agents, whose tools the team chooses and writes (reason: [PHILOSOPHY.md → 3](PHILOSOPHY.md#3-how-is-a-cloud-session-different-from-a-local-one)).

| Option | For | Against |
|---|---|---|
| **The Effect cap, plus tools that check their own arguments** (chosen) | No waiting state in the Loop. The bounds are visible in the Agent and the tools. | Tools must be written with care. |
| Built-in approval that pauses a Run for a person | Familiar from general assistants. | Treats a broken stack as normal. A paused Run must survive restarts, which adds a waiting state to every Session. |
| Leave it to extensions only | No new concept. | No bounds by default. |

A team that still wants an extra check can add one with the `PreToolUse` extension. A step that needs a person by design is a tool that hands it over, for example by opening a ticket.

### Different bounds make a different Agent

- **Instead of:** growing one Agent until it is general.
- **Why:** when reach and narrowness pull apart, the narrower answer wins ([PHILOSOPHY.md → When the answers collide](PHILOSOPHY.md#when-the-answers-collide)). Reach comes from more Agents, not bigger ones.

## 8. No instance is special

Gotato is designed for many instances behind your load balancer. One instance is the simplest case of that design, not the case it starts from, and a caller cannot tell which instance served it. Instances coordinate only through the store, so no instance holds anything another lacks. This principle adds the most machinery, so *Keep subtracting* applies inside it: Gotato picks the least machinery that is still correct on many instances.

**Ask:** is this still right when a request reaches any instance?

### One writer per Session, claimed in the store — 2026-10-16

A second instance must stop before it calls the model, and a writer that lost its claim must not append. We studied how seven Go systems do this. The closest are CockroachDB's per-node liveness record, whose epoch ends all of a node's leases at once, and Temporal's range ID, which every write checks.

| Option | For | Against |
|---|---|---|
| **A claim with an epoch, valid while its instance's liveness row is** (chosen) | Needs only conditional writes in your own store. Fences every append. A dead instance's claims all end when its row expires. | A heartbeat write per instance. A takeover after a crash waits for one row to expire. |
| A lease per Run | No liveness row. | Ownership flips between Runs, so a warm Session and a stream have no stable home. |
| A membership protocol, such as gossip | Fast failure detection. | A second system inside Gotato, and no fence on the store without an epoch anyway. |
| A placement service | Central control of placement. | A separate platform to run, which [PROPOSAL.md](PROPOSAL.md) rules out. |

### Every append is conditional on its position, and a claim takes one

- **Instead of:** avoiding conflicts with timeouts and timing, or comparing an epoch beside each append.
- **Why:** clocks drift and processes pause, so only a check inside the write itself stops a writer whose claim was taken. A claim is itself an entry, written at the position after the head its instance read, so a writer that lost its claim always writes to a position that is taken. One rule, write only to a free position, fences it, and every adapter maps that rule to an insert on a unique key.

### Coordination uses only the store's conditional writes

- **Instead of:** a coordination service such as etcd, ZooKeeper or Redis.
- **Why:** a team already runs a database. A second system to coordinate instances is infrastructure that Gotato does not add.

### Liveness is one row per instance, timed by the store's clock

- **Instead of:** a heartbeat per Session, or a comparison of the instances' own clocks.
- **Why:** one write per instance per heartbeat, however many Sessions it holds, and when a dead instance's row expires, every claim it held ends at once. The store's clock is the only one every instance shares.

### No Session is bound to an instance

- **Instead of:** keeping a Session on the instance that activated it.
- **Why:** instances come and go. An idle Session is taken over wherever its request lands, and a busy one is waited for, up to a bound.

### Many instances behave like one, and the differences are written down

- **Instead of:** promising more than one instance can, such as a message that always arrives.
- **Why:** a single instance's behavior is the spec. The few places where many instances differ, such as losing the step in flight when its writer dies, are part of the contract and listed in [FEATURES.md → G-F27](FEATURES.md#g-f27--service-many-instances-).

### A refusal is a safety valve, not a balancer

- **Instead of:** Gotato spreading load itself.
- **Why:** placing Sessions is the job of the load balancer and the autoscaler. An instance refuses a new Session only past a hard limit, and marks the refusal "not run" so that a retry is safe. The limit counts the Sessions an instance has active, because that is what its memory and its Claims grow with, and the Runner knows the count exactly; a team that wants a memory budget sizes the count to it. The refusal comes before anything is loaded, claimed or created, so the instance that refuses has done nothing a retry could repeat.

### A drain finishes before it disconnects

- **Instead of:** closing connections or cancelling Runs first, or letting another instance take over a Session the draining one is using.
- **Why:** a deploy should not lose work. An instance marks its liveness row draining, stops taking new Sessions, lets every Run finish, save and release its claim, and only then closes its streams. Another instance waits for a Session the draining one is using, as for any live writer, because taking it over would fence a Run that is about to be saved.

### An instance removes its liveness row when it stops

- **Instead of:** keeping every ended row, or a sweeper and a new store call that delete rows long expired.
- **Why:** the instance that wrote the row knows when it is done with it, and most rows end there: every rolling deploy and every CLI command stops gently. A removed row reads as not alive, so every claim that names it ends, as when a row expires. A stopped instance never writes again, so its row never comes back. The row of an instance that died stays, expired, which keeps it from ever being renewed.

## 9. A Session can be stopped and seen

A cloud Session runs with nobody watching each step. So it can be stopped at any point, and leaves enough behind to see what it did and what it cost.

**Ask:** if this step goes wrong, do the Events show it, and can it be stopped?

### `context.Context` is the only cancellation

- **Instead of:** a cancellation mechanism of Gotato's own.
- **Why:** Go code already cancels through `ctx`, so every model call, tool call, store call and stream takes one, and stopping a Run reaches all of them.

### Each kind of Event is a type of its own

- **Instead of:** one Event type with a map of fields.
- **Why:** a caller switches on an Event's type and reads fields the compiler checks, as Go code reads the tokens of `encoding/xml`. Every type carries the same header. On the wire, JSON names the type in `kind`, and gRPC uses a `oneof`.

### An Event's name says what happened, in the glossary's words

- **Instead of:** names that mix tenses, or words of their own.
- **Why:** a name is read in code, logs and dashboards, so it should say what happened. A change of state is the thing and a past participle, such as `run_started` or `session_claimed`. A delta is the thing and `delta`, such as `text_delta`, except a Tool's own report of its progress, `tool_progress`. Only the glossary's terms are used: a reply is the model's and a message is the caller's, and an Abort is never a cancel. The Go type is the same name in camel case, `RunStarted`.

### Every number about a Run comes from Events and `Usage`

- **Instead of:** counters kept inside Gotato, or a metrics endpoint of its own.
- **Why:** a team computes cache hits, tokens, failures and timings from its own telemetry, and gets the same numbers Gotato reports.

### The Context can be inspected, with the Turn's own code

- **Instead of:** a separate preview.
- **Why:** anyone can see what the model would receive now without calling it, and because inspection runs the code a Turn runs, the two cannot drift apart.

## When principles collide

When two principles pull apart, this order decides:

1. **The store is the only truth.** A lost conversation breaks trust in the service.
2. **A request only grows at its end.** What the model reads is most of what a Session costs, and a byte changed in the prefix makes the provider read everything after it again.
3. **Keep subtracting.** Weight is paid by every service that embeds Gotato, for as long as it does.

The Effect cap is not part of the trade: an Agent never runs a tool above its cap to save a Turn or a write.
