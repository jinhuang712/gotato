# Findings from real use

Each entry was checked against this repository (first pass on 2026-10-29). Add new entries below, do not rewrite old ones.

Verdict: **confirmed** (the claim holds), **partly** (part of it holds, see the note), **wrong** (the repository already covers it).

Remove an item from *Backlog* in the commit that resolves it, as in [TODO.md](TODO.md).

## Backlog

### P0


### P1


### P2


### New since the first pass



- [ ] **D-025 · Tool arguments are base64 in a Record.** Verdict: partly. `ToolUse.Arguments` and `ToolCall.Arguments` are `[]byte`, so JSON shows base64; no spec fixes that form. Changing to `json.RawMessage` is a break: arguments that are not valid JSON are stored on purpose (preflight checks `json.Valid`) and `RawMessage` would fail to commit them, and old Records hold base64 strings that would read as a JSON string. Fix without a break: keep `[]byte`; add an optional `arguments` JSON field (or a short summary) to `tool_started`; say in FEATURES.md that arguments are bytes, base64 in JSON.





- [ ] **D-035 · Observability: `compaction_ended` shows no window and hides a ceiling of 0, and `context_built` has no per-part size.** Verdict: partly (suggestion, not a defect; corrected on 2026-11-02 after reading the code). The `Compaction` value a `compaction_ended` carries already has `ceiling_tokens`, but it is `omitempty`, so a ceiling that is off (0) is left out and cannot be told from "not recorded", and it is not set on a merge Checkpoint; there is no window field at all. `ContextBuilt` carries `strategy`, `messages`, `source_messages`, `selected_messages`, `prefix_hash`, `prefix_messages`, `system_bytes` and `tools`; the sizes before and after a compaction are bytes ÷ 4. A team cannot tell how far a Session is from its ceiling from the Record. Decided 2026-11-02: the share of each part in a Turn's Context matters, so add an estimate per part (instruction, tool definitions, Segments, history, Tool Results), named an estimate, to `context_built`, and show the parts and their share in `inspect`. Additive only. The window and a ceiling of 0 on `compaction_ended` are dropped: they need a new argument on three public `Record` methods, which is not worth it.
- [ ] **D-036 · Decision wanted: clearing old Tool Results in the middle of a Run.** Not a finding of a defect. Gotato has compaction (a Segment, written once) and spill (a page over 32 KiB) and nothing between them. A mechanism that hides old results from later requests would change bytes the provider already cached (decisions 034, 040). The measured cost of a long input is about +2 s of first-token time over 44k tokens (D-033), so the gain in latency is bounded; the gain in money is not measured. Write the decision, and decide whether the compaction ceiling (D-034) already answers it, before any code.


- [ ] **D-038 · A compaction ceiling is a hard failure line, and a low one ends Runs.** Verdict: confirmed by measurement and by reading the code (2026-11-02). Measured on a real service with `CeilingTokens` set to 30,000 and no Recap writer: a Run of 7 Turns and 20 tool calls compacted twice (`past_ceiling`), the second time from about 35K estimated tokens to about 22K by the Record's figure, and then ended `limit_exceeded: the Context is still past the ceiling after its compaction (about 30046 tokens, ceiling 30000)`. Without a Recap a compaction shrank the Context by only 18% to 37%, and one large Tool Result in the last Turn (spilled) pushed it back to the ceiling. By the code (`modelctx/auto_compact.go`): the check before a compaction uses the usage the provider last reported plus an estimate of what came after, or an estimate of the Messages plus the request overhead; the check after the compaction is a fresh estimate of the Messages plus `requestOverhead` (instruction and tool definitions, bytes over 4), and the Run ends `limit_exceeded` if it is still over, if the compaction failed, or if there was nothing to compact. The two figures differ because the Record's figure is a size of the Messages and the error's figure adds the overhead of the instruction and the tool definitions (a gap of about 8K tokens here); this is read from the code, the exact field of the Record's figure was not traced. The last Turn is always kept, so a big last Turn cannot be compacted away. The failure is documented (FEATURES.md and specs/compaction.md say a Context still past the ceiling ends the Run `limit_exceeded`) but nowhere is it said that a ceiling set below the window makes a Run fail, nor how low is too low. Fix: say in the compaction section that the ceiling is a hard line, not a cost knob, and that it must stay above the baseline after a compaction (overhead of the instruction and tools, the kept tail of up to a quarter of the ceiling, a Recap if any) plus the largest Tool Result the Run can hold in one Turn (below the 32 KiB spill page, so up to about 8K tokens); the last part is a rule of thumb, not measured. Decided 2026-11-02: no code change. A ceiling stays a hard line, whoever set it; a team that wants a cost cap writes a `TurnStopper` (decision 038).
- [ ] **D-039 · After a compaction, a loaded tool the Session never called is unloaded, and its first call fails once.** Verdict: **confirmed by measurement**, expected behavior and not a defect (checked in the Record of a real Run). After a compaction on a real service the next Turn made 5 calls to tools that returned `was not loaded, so this call did not run`, and the following Turns called them again successfully. FEATURES.md says the tools the closed Segment called and the kept tail calls stay loaded and the others unload (`tool_unloaded`), and that on the list form a load costs a cache read, which is why tools unload at a compaction and nowhere else; a tool of an active ToolSet never unloads. So a compaction can cost one Turn when the model calls a tool it only found, never used. Measured: at the first compaction the Session kept 3 tools that had really been called and wrote 23 `tool_unloaded` (`why: compaction`) for every tool that had only been loaded through `search_tools`; the 5 tools whose first call failed had been loaded in Turns 1 to 3, never started, and were all in that batch; the call failed once and succeeded after the next load, at the cost of one idle Turn. The second compaction kept 8 and unloaded 1. The failed call's text already says `It is loaded now; call it again.`; loading and running it on the spot is not planned (see the roadmap). Fix: one sentence in the compaction and tool-search sections that a compaction can unload tools and the next call to one costs a round; `WithActiveToolSet` keeps a set loaded. Pairs with D-018.

### P3 (docs)
