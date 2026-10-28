# 016. Reasoning goes back for the current Round where the provider allows it

Decided: 2026-10-18    Status: current

## The problem
A thinking model writes a lot of reasoning. A Session can live for weeks, so reasoning sent back on every Turn fills the window, and the bill, long after it stopped helping.

## The choice
Claude and OpenAI get the current Round's reasoning, and any other provider gets all of it. A Round is a user Message and every reply and Tool Result after it. The rule reads only the Record, so every instance sends the same bytes.

## Not chosen
- All reasoning to every provider: the prefix only grows, but a weeks-long Session carries all of its reasoning, and Claude's newer models keep and bill all of it.
- No reasoning at all: the smallest requests, but Claude and OpenAI need the reasoning of a tool loop in progress, and DeepSeek refuses the request.

## Consequences
Reasoning stops piling up where the provider allows it. Once per Round, the last Round's replies are cached again without their reasoning. DeepSeek and unrecognized providers still carry every reasoning artifact until compaction.
