# 056. A stored Event is sent by the Writer that wrote it

Decided: 2026-11-04    Status: current

## The problem
A Session's stored Events reached its Feed from two senders: the live Session sent what it wrote, and the host's Writer sent what it wrote outside the Session (a claim, a release, a notice, a compaction). Two senders could interleave, so a subscriber had to sort and drop duplicates on top of the catch-up it needs anyway when another Instance wrote.

## The choice
Every stored Event is sent by the Writer that wrote it, right after the write, under the Writer's lock: a Run's Steps and the host's writes between Runs alike. A live Session given a host's Feed sends only its deltas, which are never written. A Session with no Writer, in library use, sends its stored Events itself, since nothing else writes them.

## Not chosen
- Only the live Session sends: many Events are written between Runs, when no Session is in memory (a notice, a compaction, a claim, a release, a create, a fork, a delete). Building a Session to send each costs a resolution of its ToolSets and a known Agent, and leaving them unsent breaks the Instance stream, which has no catch-up.

## Consequences
A Session's Feed has one sender of stored Events, in the order of their numbers. The catch-up from the Record stays, for the writes of another Instance. A publisher runs the subscribers' filters inside the Writer's lock, so a filter must not call back into that Writer. A caller of `WithPublisher` that also forwarded a Session's own Events gets them twice and drops its forwarding.
