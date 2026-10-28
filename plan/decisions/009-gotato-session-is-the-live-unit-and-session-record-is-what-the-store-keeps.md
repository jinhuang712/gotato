# 009. `gotato.Session` is the live unit, and `session.Record` is what the store keeps

Decided: 2026-10-16    Status: current

## The problem
The object a caller builds and talks to has an ID, a Status and a state machine, a queue for Steer and FollowUp, a Transcript, a `ContextBuilder` and tools. It is what the glossary calls a Session. The stored thing needs another name.

## The choice
The live unit is `gotato.Session`, one concrete type with no interfaces. The stored thing is `session.Record`. A live Session's ID is its Session ID, which comes from its Record or is generated when there is none, and every Event carries it as `session_id`.

## Not chosen
- `gotato.Loop`: the Loop is a concept, the cycle inside a Session, not a thing a caller holds.
- A role name such as `gotato.Executor`: the smallest change, but the Session would either wrap the Executor or the type would be renamed again.

## Consequences
The type is named after the thing a caller holds, and the name stays as the Session grows a warm life in the Runner. A caller that wants a fake defines its own small interface. The warm copy, its TTL and its timers belong to the Runner, not to `gotato.Session`.
