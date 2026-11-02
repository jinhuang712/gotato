# Findings from real use

Each entry was checked against this repository (first pass on 2026-10-29). Add new entries below, do not rewrite old ones.

Verdict: **confirmed** (the claim holds), **partly** (part of it holds, see the note), **wrong** (the repository already covers it).

Remove an item from *Backlog* in the commit that resolves it, as in [TODO.md](TODO.md).

## Backlog

### P0


### P1


### P2


### New since the first pass








- [ ] **D-036 · Decision wanted: clearing old Tool Results in the middle of a Run.** Not a finding of a defect. Gotato has compaction (a Segment, written once) and spill (a page over 32 KiB) and nothing between them. A mechanism that hides old results from later requests would change bytes the provider already cached (decisions 034, 040). The measured cost of a long input is about +2 s of first-token time over 44k tokens (D-033), so the gain in latency is bounded; the gain in money is not measured. Write the decision, and decide whether the compaction ceiling (D-034) already answers it, before any code.



### P3 (docs)
