# 027. Instances coordinate through the store's conditional writes, with one liveness row each

Decided: 2026-10-16    Status: current

## The problem
Clocks drift, processes pause, and an instance can die in the middle of a Run. Two instances must never write one Session, and coordination must need nothing a team does not already run.

## The choice
Every append is conditional on its position: the store accepts a write only to a free position. A claim is itself an Entry, written at the position after the head its instance read, so a writer that lost its claim always writes to a position that is taken; every adapter maps the one rule to an insert on a unique key. Only the store's conditional writes coordinate. Liveness is one row per instance, timed by the store's clock: one write per instance per heartbeat however many Sessions it holds, and when a dead instance's row expires every claim it held ends at once. An instance removes its row when it stops, which reads as not alive, so its claims end as on an expiry; the row of an instance that died stays, expired. A conflict is found before the model is called: a writer writes each model call before it makes it, and checks before each write that its own row was renewed in time, so a writer that lost its claim stops there. One case is beyond any check: an instance paused for longer than its row lives may send one request when it wakes. Its result can never be written.

## Not chosen
- Avoiding conflicts with timeouts and timing, or comparing an epoch beside each append: clocks drift and processes pause, so only a check inside the write itself stops a writer whose claim was taken.
- A coordination service such as etcd, ZooKeeper or Redis: a team already runs a database, and a second system is infrastructure that Gotato does not add.
- A heartbeat per Session, or a comparison of the instances' own clocks: the store's clock is the only one every instance shares.
- Keeping every ended row, or a sweeper and a new store call that delete rows long expired: the instance that wrote the row knows when it is done with it.
- Finding out after a Turn that it cannot be committed: a Turn costs money and seconds.

## Consequences
A deploy or a crash loses at most the step in flight, and a Session never has two writers. The claim itself is [019](019-one-writer-per-session-claimed-in-the-store.md).
