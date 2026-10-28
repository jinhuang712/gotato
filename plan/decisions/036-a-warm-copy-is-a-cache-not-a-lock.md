# 036. A warm copy is a cache with a head check, not a lock

Decided: 2026-10-25    Status: current

## The problem
After a Run, the live Session can stay in memory so the next message finds its queue, tools and Transcript ready. The question is what the copy may claim while it waits.

## The choice
The copy holds no Claim while idle. Before a Run on a warm copy, the instance compares the Record's head in the store with the copy's, and reloads if another instance wrote the Session.

## Not chosen
- The warm copy holds the Claim: no head check, but an idle Session is bound to one instance for its whole TTL, a takeover must wait or fence, a deploy must release every idle Session, and it brings back the pin that [002](002-the-store-is-the-truth-and-memory-is-a-cache.md) rules out.
- No warm copy, load and save around every Run: the fewest moving parts, but a load for every Run, no Session for a message that arrives between Runs, and an idle compaction needs a service timer.

## Consequences
Any instance can take the Session over and nothing is pinned. A stale copy costs one read of the head and a reload, and memory stays reclaimable: the longest-idle copy can be evicted at once. Each Run on a warm copy costs a read of the head.
