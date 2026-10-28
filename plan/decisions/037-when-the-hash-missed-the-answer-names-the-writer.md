# 037. When the hash missed, the answer names the writer

Decided: 2026-10-25    Status: current

## The problem
Decision [021](021-messages-reach-the-writer-and-take-effect-in-its-process.md) relies on the load balancer's hash. A caller that cannot route by Session, such as a chat bot whose platform delivers events to any instance, can still reach an instance that is not the writer.

## The choice
The instance answers `not_writer` and tells the caller where the writer is. A caller with a client sends the message on, and HTTP can do it when a team opts in.

## Not chosen
- Queue the message in the store: it survives the writer's death and any instance can accept it, but it is the write per message and polling writer that [021](021-messages-reach-the-writer-and-take-effect-in-its-process.md) rejected.
- Instances forward among themselves, always: every caller reaches the writer without knowing, but Gotato becomes a router (see [024](024-gotato-builds-no-infrastructure.md)).

## Consequences
Nothing waits in the store and no network is added between instances. The message is not lost, because it did not run and the caller still holds it. A caller that cannot follow an address gets an error, and the writer must advertise one.
