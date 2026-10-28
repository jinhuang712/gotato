# 022. A Session is a goroutine over its Record, cheap to drop and rebuild

Decided: 2026-10-16    Status: current

## The problem
The store is the truth ([002](002-the-store-is-the-truth-and-memory-is-a-cache.md)), so a live Session is a view of its Record. For one instance to hold many Sessions, most of them idle, and for any instance to rebuild one, a live Session must cost little to build and to lose.

## The choice
A Session's identity is its Record's ID, so whichever instance builds it, it is the same Session. A live Session is a goroutine, which costs kilobytes and starts in microseconds. It holds only what is cheap to rebuild: model clients and connections belong to the instance, and its Sessions share them. A Session is built when needed. A load starts from the last Checkpoint, so a Session that lives long stays cheap to rebuild, and the Context depends only on the Record and the Agent, so every instance builds the same Context and the provider's cache serves it whichever instance sends it. An idle Session leaves memory at its TTL, 60 minutes unless its Agent sets another, or sooner when the instance needs the room, and it is compacted shortly before. A Fork writes the store and activates nothing: the new Session is a new Record, so whichever instance its first request reaches activates it.

## Not chosen
- A live object with an identity of its own.
- A process, a subprocess or a sandbox for each Session: isolation, but one instance could then hold few Sessions.
- A Session that opens its own subprocesses or holds long-lived connections: anything expensive in a Session makes dropping it expensive.
- A pool of prepared Sessions: building one is cheap enough that a pool would only add state to keep right.
- A Session that stays in memory until someone destroys it: memory is a cache, so an idle Session gives it back.
- Replaying the whole history on a load, or state that lives on one instance.
- Copying a live Session in memory to make a Fork.

## Consequences
Dropping a Session loses nothing already written, and any instance rebuilds it at the cost of the part since its last Checkpoint. How a warm copy is kept is in [036](036-a-warm-copy-is-a-cache-not-a-lock.md).
