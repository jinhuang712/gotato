# 011. An Agent's bounds are its named tools and its Effect cap, and tool output is data

Decided: 2026-10-16    Status: current

## The problem
Nobody watches each step of a cloud Session, and its tools are written by the team. What a Session may do must be readable in one place, and neither a conversation, a Tool Result nor the model itself may widen it.

## The choice
The bounds are in the Agent's definition, where a reviewer reads them: its named tools and its Effect cap. A conversation can switch off one of its Agent's tools, but never add one or lift the cap. Every tool declares its Effect (read, write local, write external, destructive), and silence means destructive. Tools check their own arguments. Tool output is data: a Tool Result reaches the model as a tool-result message tied to its call, never as the system instruction or a user message. The one thing Gotato enforces is that a result cannot speak as the system: text that imitates one of Gotato's four tags (`notice`, `segment`, `tool_calls`, `recap`) is escaped when the request is built, in a result, in `SafeError`, in a user message or in the model's own reply, while the Record keeps the original. This does not make a result safe to obey, and Gotato does not detect persuading words; the damage is limited by the bounds a result cannot raise. When reach and narrowness pull apart, the narrower answer wins, and reach comes from more Agents, not bigger ones ([PHILOSOPHY.md → When the answers collide](../../PHILOSOPHY.md#when-the-answers-collide)).

## Not chosen
- Someone watching each Run, or bounds that change from one conversation to the next: flexible, but not reviewable.
- Treating a tool that declares nothing as harmless: convenient, but a forgotten declaration should fail closed.
- A central policy engine: one place for rules, but a write that is safe for some arguments and not others is best judged by the code that knows them.
- Trusting what a tool returns, or filtering it for hostile text: a filter is a promise Gotato cannot keep.
- Growing one Agent until it is general: one Agent to run, but its bounds are then too wide to read.

## Consequences
The bounds of a Run can be read from its Agent alone, and a tool above the cap is never shown to the model ([014](014-the-effect-cap-hides-tools-above-it.md), [015](015-only-the-agent-sets-the-effect-cap.md), [005](005-no-approval-system.md)). Tools must be written with care, because they are the bounds.
