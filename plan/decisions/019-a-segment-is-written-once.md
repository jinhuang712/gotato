# 019. A Segment is written once, and a Checkpoint points to it

Decided: 2026-10-24    Status: current

## The problem
A provider's prompt cache reads bytes, so a Segment is only useful if its bytes never change.

## The choice
A Segment is appended once, as its own `segment_closed` Entry, with its rendered Messages and its data. A Checkpoint lists the numbers of the Segments the Context uses and keeps the tail with the tail's own Messages.

## Not chosen
- Copy every Segment's Messages into each Checkpoint: writes the same Messages again at every compaction.
- Store a Segment as data and render it when a request is built: a change in the rules would change the bytes.

## Consequences
A Segment's bytes are fixed at the moment it is written. A load reads every `segment_closed` Entry before the last Checkpoint, including one a later Checkpoint no longer lists, so a load grows with the history. A Record that holds Segments is read only by a Gotato that knows them.
