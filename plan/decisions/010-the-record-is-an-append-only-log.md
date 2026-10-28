# 010. The Record is an append-only log

Decided: 2026-10-16    Status: current

## The problem
A save has to cost the size of what changed, and a stale copy has to fail instead of overwriting a newer one.

## The choice
The Record is append only, with Checkpoints for compaction. Every append names the position it follows.

## Not chosen
- Rewrite the whole Record, with a version check: the simplest contract, but a save costs the whole Session, megabytes for long ones.
- Append, plus a rewrite for compaction: compaction stays a replacement, but there are two write paths and the rewrite brings the cost back.

## Consequences
A save costs one step, two writers cannot append the same sequence number, every adapter maps it to an insert, and compaction loses nothing. The Record grows, and a team bounds its storage.
