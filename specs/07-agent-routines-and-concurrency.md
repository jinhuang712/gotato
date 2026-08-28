# 07. Agent Routines, Concurrency, and Cancellation

**Status:** Draft

> Core serializes each Agent; bounded child work and Hosted orchestration provide concurrency around that invariant.

## 1. Agent exclusivity

One Agent has at most one active mutating Run. Concurrent Prompt and Continue calls return a typed busy error. Different Agent instances may run concurrently.

## 2. Run Context

```text
Run Context
  ├── Model stream
  ├── Tool Uses
  ├── Extensions and observers
  └── Agent Routines
```

Every owned child receives this Context or a shorter derived deadline. No child may extend its parent lifetime.

## 3. Internal concurrency

Parallel Tools require a positive configured bound. Tool completion Events follow actual completion order; transcript commitment is source ordered. Steering and Follow-up queues are concurrency-safe and preserve acceptance order.

Every internal channel has explicit capacity, one owner, one producer responsible for close, and Context-aware blocking. Channels are not public API.

## 4. Routine

A Routine contains:

```text
Routine ID
parent Run ID
child Agent distinct from parent and siblings
child Run and Context
status
settled immutable Result
```

A Routine is a managed child Run, not a goroutine. A local implementation may use goroutines.

## 5. Spawn

Application-controlled spawn uses an Agent factory. A Model-controlled `spawn_agent` Tool may wrap the same API and follows the ordinary Tool lifecycle. Spawn reserves bounds before child creation; rejected spawn creates no child and emits no `routine_started`.

## 6. Lifecycle and settlement

```text
Created → Queued → Running
                    ├── Completed
                    ├── Failed
                    └── Cancelled
```

A Routine settles once. Repeated waits return the same immutable result. Child `agent_end` and parent-facing Routine terminal Event are both required in their scopes.

## 7. Cancellation

Parent cancellation cancels every owned Routine Context. Routine cancellation reaches child Model, Tools, Extensions, observers, and nested Routines. Group policy determines sibling cancellation.

## 8. Limits

The coordinator MUST enforce:

```text
Routines per parent Run
concurrently active Routines
nesting depth
child deadline
child Turns and Tool Calls
result and progress volume
```

Counters are reserved atomically before factory invocation. Failed factory creation releases reservations and settles the Routine as failed.

## 9. Groups

A bounded Routine Group supports:

```text
collect_all
fail_fast
collect_partial
first_success
```

Results are indexed by spawn order. Completion Events use actual completion order. A Group waits for started children to settle before releasing resources.

## 10. Events and correlation

Parent Events include `routine_started`, `routine_completed`, `routine_failed`, and `routine_cancelled`, with Routine ID, parent Run ID, child Run ID, and name. Detailed child Events retain child Run sequence and may be separately projected by a Host.

## 11. Hosted placement

Local placement uses child Core instances. A future remote Routine Executor may use a remote Host, but must preserve identity, limits, cancellation, Events, and single settlement. Remote placement is not a new Core loop.

## 12. Host concurrency

Host admission and scheduling of many Agent Runs are distinct from Core's per-Agent exclusivity. Host limits include active streams, active Runs, queued requests, per-Agent capacity, and delivery resources. Exact preset values are Host configuration.
