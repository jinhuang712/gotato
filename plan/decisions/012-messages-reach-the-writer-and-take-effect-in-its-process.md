# 012. Steer lands at a Turn boundary, Interrupt and Stop at once, and a late message starts a new Run

Decided: 2026-10-16    Status: current

## The problem
A Session's messages reach its writer through the load balancer's hash, so each kind can take effect in the writer's own process. The question is what happens to a message that arrives during a Run, and whether it waits in the store.

## The choice
Steer takes effect at a Turn boundary. Interrupt and Stop take effect at once. A message that arrives after a Run has ended starts a new Run. Nothing waits in the store.

## Not chosen
- Queue every message in the store first: it survives the writer's death, but costs a write per message and a writer that polls the store.
- Reject a message while a Run is going: the simplest, but a caller cannot steer or stop a Run.

## Consequences
The behavior is the same as one instance. A message accepted but not yet committed is lost if the writer dies, and the caller sends it again. For a message that reaches an instance that is not the writer, see [022](022-when-the-hash-missed-the-answer-names-the-writer.md).
