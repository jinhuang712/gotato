# 005. The Effect cap and tools that check their own arguments replace an approval system

Decided: 2026-10-15    Status: current

## The problem
Purpose-built agents have tools the team chooses and writes. A Run that pauses for a person adds a waiting state to every Session, and treats a broken stack as normal.

## The choice
The bounds are the Agent's named tools and its Effect cap, plus tools that check their own arguments. A team that wants an extra check adds one with the `PreToolUse` extension. A step that needs a person by design is a tool that hands it over, for example by opening a ticket.

## Not chosen
- Built-in approval that pauses a Run for a person: familiar from general assistants, but a paused Run must survive restarts, which adds a waiting state to every Session.
- Extensions only: no new concept, but no bounds by default.

## Consequences
There is no waiting state in the Loop, and the bounds are visible in the Agent and its tools. Tools must be written with care.
