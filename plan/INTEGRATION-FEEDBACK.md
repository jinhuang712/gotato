# Findings from real use

Each entry was checked against this repository (first pass on 2026-10-29). Add new entries below, do not rewrite old ones.

Verdict: **confirmed** (the claim holds), **partly** (part of it holds, see the note), **wrong** (the repository already covers it).

Remove an item from *Backlog* in the commit that resolves it, as in [TODO.md](TODO.md).

## Backlog

### P0


### P1


### P2


### New since the first pass








- [ ] **D-035 · Observability: `compaction_ended` shows no window and hides a ceiling of 0, and `context_built` has no per-part size.** Verdict: partly (suggestion, not a defect; corrected on 2026-11-02 after reading the code). The `Compaction` value a `compaction_ended` carries already has `ceiling_tokens`, but it is `omitempty`, so a ceiling that is off (0) is left out and cannot be told from "not recorded", and it is not set on a merge Checkpoint; there is no window field at all. `ContextBuilt` carries `strategy`, `messages`, `source_messages`, `selected_messages`, `prefix_hash`, `prefix_messages`, `system_bytes` and `tools`; the sizes before and after a compaction are bytes ÷ 4. A team cannot tell how far a Session is from its ceiling from the Record. Decided 2026-11-02: the share of each part in a Turn's Context matters, so add an estimate per part (instruction, tool definitions, Segments, history, Tool Results), named an estimate, to `context_built`, and show the parts and their share in `inspect`. Additive only. The window and a ceiling of 0 on `compaction_ended` are dropped: they need a new argument on three public `Record` methods, which is not worth it.
- [ ] **D-036 · Decision wanted: clearing old Tool Results in the middle of a Run.** Not a finding of a defect. Gotato has compaction (a Segment, written once) and spill (a page over 32 KiB) and nothing between them. A mechanism that hides old results from later requests would change bytes the provider already cached (decisions 034, 040). The measured cost of a long input is about +2 s of first-token time over 44k tokens (D-033), so the gain in latency is bounded; the gain in money is not measured. Write the decision, and decide whether the compaction ceiling (D-034) already answers it, before any code.



### P3 (docs)
