# 011. One writer per Session, by a claim with an epoch

Decided: 2026-10-16    Status: current

## The problem
A second instance must stop before it calls the model, and a writer that lost its claim must not append. We studied how seven Go systems do this. The closest are CockroachDB's per-node liveness record, whose epoch ends all of a node's leases at once, and Temporal's range ID, which every write checks.

## The choice
A Claim with an epoch, valid while its instance's Liveness row is. A Claim is an Entry written at the position after the head its instance read, so a writer that lost its Claim always writes to a position that is taken.

## Not chosen
- A lease per Run: needs no liveness row, but ownership flips between Runs, so a warm Session and a stream have no stable home.
- A membership protocol such as gossip: fast failure detection, but a second system inside Gotato, and no fence on the store without an epoch anyway.
- A placement service: central control, but a separate platform to run, which [PROPOSAL.md](../../PROPOSAL.md) rules out.

## Consequences
It needs only conditional writes in the team's own store, and it fences every append. A dead instance's Claims all end when its row expires. It costs a heartbeat write per instance, and a takeover after a crash waits for one row to expire.
