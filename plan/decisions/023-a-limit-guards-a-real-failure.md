# 023. A limit guards a real failure, and sizes follow the model's window

Decided: 2026-10-25    Status: current

## The problem
A cap that fits no real failure either cuts off work that was going well or never fires. Four caps stopped a Run that goes on without end: Turns, tool calls, the size of a Tool Result and a Run's time.

## The choice
A cap exists because a named failure needs it: a hung call has its deadline, a Context past the window has its ceiling, a Run that must stop has its `ctx`. A number that depends on the job, such as how many Turns a support case may take, is the team's own `TurnStopper`.

## Not chosen
- Default counting caps for Turns and tool calls: every Run is bounded with no code, but one number cannot fit a lookup and a long investigation, and a team that raises it loses the guard.
- A cap on the bytes of a Tool Result: one result cannot fill memory, but it fails a Run for something the model could have read in pages, at a size unrelated to the window.

## Consequences
A team that wants a cap on Turns writes a few lines. A size follows the window: a Tool Result over one page is spilled ([025](025-a-large-tool-result-is-spilled-and-read-in-pages.md)), and the compaction ceiling is the window less a reserve.
