# 002. The store is the truth, and memory is a cache

Decided: 2026-10-15    Status: current

## The problem
A Session behaves like a live unit while it is in use and like a record while it waits. A message that arrives mid-Run needs a queue, a stream and a status to meet. Anything held only in memory is lost with its instance.

## The choice
A live unit is kept warm while it is used, and the store is the truth. The unit is rebuilt from the Record by any instance, and the idle TTL only frees memory.

## Not chosen
- Nothing in memory, load and save around every Run: the fewest moving parts, but the Session has no queue of its own, so a message that arrives mid-Run can only wait or be rejected.
- A long-lived unit in memory that owns its state: fast, and natural for queues and streams, but lost with its instance and it pins each conversation to one instance.

## Consequences
Easy: a queue, a stream and a status per Session, and the loss of any instance costs nothing already written. Hard: a write per step, and machinery to load a Session and to let it go. A Session's identity is its Record, not the memory that holds it.
