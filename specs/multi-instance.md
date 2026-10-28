# Many Instances

A service runs as many Instances behind a Load balancer, and any of them may get any request. Two of them must never write the same Session at once, an Instance may die in the middle of a Run, and a deploy should lose no work. Gotato solves this with the Store alone: no Instance is special, and no Instance holds anything the others cannot read.

## TL;DR
- The Store is the only place Instances meet. A Claim, written into the Record at a free position, makes one Instance the Writer, and the Store refuses any later write from another.
- Each Instance keeps one Liveness row, timed by the Store's clock. When it expires, every Claim of that Instance ends at once, so a crashed Instance's Sessions can be taken over.
- A Warm Session is a cache and holds no Claim. A message that misses the Writer gets `not_writer` with the Writer's address, and a Drain finishes work before an Instance leaves.

## One Store, no Instance talks to another

Every Instance reads and writes the same Store; none connects to another.

```text
                    Load balancer  (hash of the session-id)
              ┌──────────┼──────────┐
              ▼          ▼          ▼
         Instance A  Instance B  Instance C     no arrows between them
              └──────────┼──────────┘
                         ▼
         Store   Records (append-only logs)  +  Liveness rows (one per Instance)
```

The hash sends most requests for a Session to one Instance, but that is routing and not correctness: if it misses, or an Instance leaves, the request lands elsewhere and still works. The Store's conditional writes are the only coordination, so a team needs no etcd or Redis next to its database. Gotato spreads no load. An Instance refuses a new Session only past its hard limit, and says Not run, so a retry elsewhere is safe.

## A Claim makes one Writer

A Claim is an Entry in the Record, written at the position after the head its Instance read.

```text
position   41          ...    47          48                49
Record     Claim(A,1)  ...    reply       Claim(B,2)        message
                                ▲            ▲
           A wrote at 47 ──────┘            └ B read head 47 and wrote its Claim at 48

           A, paused, wakes up:  Append(after 47)  ──▶  position 48 is taken  ──▶  conflict, A stops
```

The Store accepts a write only to a free position, so one rule fences every append, and each adapter maps it to an insert on a unique key. A timeout could not do this, because a paused process wakes up believing it still owns the Record. The Epoch grows when another Instance claims the Record; the same Instance claiming again for its next Run keeps its Epoch. If two Instances open one Session at the same moment, only one Claim lands, and the other gets a conflict and looks again. A Claim blocks others only while the Record is busy, and a release (`session_released`) frees it.

## An Instance comes and goes

An Instance proves it is alive with one row, however many Sessions it holds.

```text
Instance A   ·──·──·──·──·──·──·──·─┐   a beat every TTL/3 renews the row to "now + TTL"
                                    │   by the Store's clock
                                    ├─ Drain: row marked draining, no new Run, no takeover;
                                    │         Runs finish, save, release (at the deadline: stopped first)
                                    └─ Close: row removed ─▶ A's Claims end at once

Instance B   ·──·──·──✕                 crash or pause
                       │ B's own guard: refuse writes once the last beat is too old
                       └ row expires ─▶ every Claim of B is dead at once ─▶ others may take over
```

One write per Instance per beat, and one expiry ends every Claim, so the cost does not grow with the Sessions. Before each write the Writer checks that its row was renewed in time, a little before the Store would expire it, so a paused Instance stops itself first. A row that has expired never comes back: the Instance registers a new name, its old Writers are refused for good, and its warm copies are dropped. An Instance paused longer than its row lives may still send one model request when it wakes, but its result can never be written. A Drain waits for Runs; one that passes its deadline is stopped, saved and released, and the caller is told to reconnect.

## Taking a Session over

Who may claim a Session depends on whether its last Writer is still using it.

```text
 request for Session S lands on B
   │ B draining? ─ yes ─▶ draining (Not run, Retryable)
   ▼
 read S: last Claim, and is it busy?
   ├─ not busy (released) ─────────▶ claim at once            (why: idle)
   ├─ busy, holder's row alive ────▶ wait, look again, backoff up to a bound
   │                                   ├─ released meanwhile ─▶ claim        (why: waited)
   │                                   └─ still busy ─────────▶ busy, retry_after_ms (Not run, Retryable)
   └─ busy, holder's row dead ─────▶ claim                    (why: writer_gone)
                                       the Run that never ended ends as failed, cause `interrupted`;
                                       queued messages are dropped; a half reply stays as a Cut-off reply
```

A live holder is waited for, even a draining one, because taking its Session would fence a Run that is about to be saved. A dead holder costs one expiry, the wait of a crash. After a takeover the new Writer loads from the last Checkpoint and ends what the old one left, so the Record never shows a Run that is still going on a dead Instance. What the dead Writer only had in memory, a queued message or the Step in flight, is gone, and its caller sends it again.

## A Warm Session is a cache

After a Run the Instance keeps the Record and its Writer in memory, with no Claim.

```text
 Run ends ─▶ release (busy = false) ─▶ warm copy: Record + Writer, no Claim, timers armed
                                         │ idle compaction before the cache expires or the TTL
                                         │ TTL ends, or the Instance is full ─▶ copy dropped
 next request, on this Instance:
   cancel any idle compaction (the request is served first)
   head check: does the Store hold Entries after my head?
     no  ─▶ Resume: write a Claim again (same Epoch) ─▶ run on the copy, nothing loaded
     yes ─▶ another Instance wrote: drop the copy, load from the last Checkpoint
```

The copy holds no Claim, so another Instance can take the Session over at any time and nothing is pinned to this one; the head check finds out in one cheap read. If the Resume loses a race, the copy falls back to a load. Warm Sessions count toward the Instance's limit of active Sessions, and the longest-idle one is evicted first, so a Run is refused only when every live Session is running. A copy whose Writer failed is never kept.

## A message that misses the Writer

A message for a Run goes to the Writer; an Instance that is not the Writer says so and names it.

```text
 Client        Instance B (not the Writer)        Store             Instance A (Writer)
   │ Steer ───────▶ │ no Run of S here              │                      │
   │                │ ── read S ───────────────────▶│                      │
   │                │ ◀─ busy, Claim A, A alive, A's address               │
   │ ◀─ not_writer: Not run, Retryable, A's address (forwarding off)       │
   │                │ forwarding on, address allowed:                      │
   │                │ ── same request + Gotato-Forwarded ─────────────────▶│
   │ ◀──────────────────────────── A's answer, relayed ────────────────────│
```

`not_writer` is the answer for a Steer, a FollowUp, an Interrupt, a Notice or an Abort, because each takes effect in the Writer's own process. It says Not run, so the caller can send it again, to the address or through the Load balancer. Forwarding is an option of the HTTP layer: it goes only to an address the team allows, never follows a redirect, and never forwards a request that was forwarded already. A new Run on a Session another Instance is running is not answered `not_writer`: it waits for the Writer, up to a bound, then gets `busy`.

## What this buys you
- A deploy or a crash loses at most the Step in flight and the messages that were only queued; every committed Step is in the Store.
- Any Instance can serve any request, so scaling, rolling and autoscaling stay with your infrastructure.
- A Session never has two Writers: a paused or cut-off Instance cannot write, and a caller can tell a safe retry from a risky one.

## See also
[FEATURES.md → G-F27](../FEATURES.md#g-f27--service-many-instances-), decisions [002](../plan/decisions/002-the-store-is-the-truth-and-memory-is-a-cache.md), [011](../plan/decisions/011-one-writer-per-session-claimed-in-the-store.md), [013](../plan/decisions/013-gotato-builds-no-infrastructure.md), [021](../plan/decisions/021-a-warm-copy-is-a-cache-not-a-lock.md) and [022](../plan/decisions/022-when-the-hash-missed-the-answer-names-the-writer.md), [GLOSSARY.md](../GLOSSARY.md). The Record, the Store contract and the errors have specs of their own: [record.md](record.md) and [errors.md](errors.md).
