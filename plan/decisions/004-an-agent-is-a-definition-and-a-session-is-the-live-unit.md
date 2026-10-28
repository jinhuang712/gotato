# 004. An Agent is a definition, and a Session is the live unit

Decided: 2026-10-15    Status: current

## The problem
Agent APIs in wide use name a definition and its running instance differently from Gotato's first code. A reader who knows those APIs would need a note for every term.

## The choice
An Agent is a definition. A Session is the live unit that runs it, and many Sessions can run one Agent.

## Not chosen
- Agent as the live unit, with no term "session": one noun for callers, but the reverse of common usage, so every reader needs a note.
- Agent as the loop for one Run and Session as the stored record: no rename, but the thing a caller talks to has no name of its own.

## Consequences
One Agent running many Sessions reads naturally, and the words match the APIs a reader already knows. The code carries the names (`service.Agent`, `gotato.Session`, [016](016-gotato-session-is-the-live-unit-and-session-record-is-what-the-store-keeps.md)).
