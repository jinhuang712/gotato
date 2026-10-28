# 032. Compaction closes what came since the last Checkpoint into a Segment, and never rewrites a Segment

Decided: 2026-10-19    Status: current

## The problem
A Session's user often comes back long after the provider's cache has expired, and a long Run can outgrow the window. Compaction has to keep as much of the conversation as it can and make the first request after a pause cheap.

## The choice
Each compaction closes the history since the last Checkpoint into a Segment, whose bytes never change. The oldest Segments are merged when they pass their budget.

## Not chosen
- One summary of everything before the kept tail, written again at every compaction: one block of bounded size, but each compaction summarizes the summary before it, so early facts blur or drop out, and the whole prefix changes every time.
- The provider's own compaction: no code in Gotato, but its result cannot be read, does not carry over to another model, differs between providers, and cannot close one Segment at a time. It may become an option.

## Consequences
There is no summary of a summary until the oldest Segments merge, so nothing drifts. The Segments before keep their bytes, so a provider that caches any prefix reads them from the cache. It matches a Record that only grows. Segments add up over a long Session, so at last the oldest are merged, which rewrites the prefix once. A merge writes one Recap of the oldest Recaps, and the words of the Segments it merged stay readable with `read_segment`.
