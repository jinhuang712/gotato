# 047. A value a layer owns is set only through that layer, and any other way is refused

Decided: 2026-11-02    Status: current

## The problem
[Principle 10](../../DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced) says nothing plugged in bypasses a layer that owns a guarantee, yet four paths do, silently. Keys under `gotato.` are read as settings, and `CreateSession`, `RunRequest.Metadata`, `Update` and httpapi pass any key, so a remote caller can replace the instruction, move the ceiling or cache key, or switch the Agent. `Agent.Options` run after the Runner's own, so they can replace the Writer, the cap ([015](015-only-the-agent-sets-the-effect-cap.md)) or tools a Session switched off. A Transcript's capabilities are found by type assertion, so a wrapper that forgets one loses durability. `CreateSession`, `Fork` and `Record` return a detached `*session.Record` whose `Set` is stored nowhere.

## The choice
Each value has one owner and one typed way in. Any other way fails with `invalid_argument` before anything is written; nothing is dropped or overridden.

- **The definition** ([044](044-an-agent-profile-and-a-task-spec-are-registered-inline-or-from-a-store.md)'s AgentProfile) owns instruction, model, tools, cap, Sampling and run policy, ceiling included. `Agent.Options` goes; its uses become fields (`ToolSets []ToolSet` with a `Mode` of listed, folded or active, `Sampling gotato.Sampling` of [045](045-the-gateway-is-layered-and-each-layer-can-be-replaced.md), `Extensions []any`, `ToolSearcher`), and the Runner builds every `SessionOption`. A Session has no instruction or ceiling of its own: another one is another AgentProfile, and one job's text is a TaskSpec's Background.
- **The Runner** owns every key under `ReservedPrefix` (`"gotato."`); a caller's key there fails with `ErrReservedKey`. The Agent changes only by `RunRequest.Agent`, a tool switch only by `SetToolActive`, the cache key only by `Fork`. The `Meta*` constants become unexported.
- **`gotato.Session`**: `NewSession` refuses a single-valued option given twice (`WithTranscript`, `WithInstruction`, `WithMaxEffect`, `WithToolFilter`, `WithSampling`, ...), naming it. A Transcript declares `Capabilities() Capabilities{ID string; Journal Journal; Durable, Spill bool; Events EventSource; Loaded LoadedToolSource}`. An embedding wrapper forwards it; any other says so in code. `NewSession` refuses `Durable` without Journal, Events and Loaded. A Session that stores nothing declares it.
- **Values, not handles**: `CreateSession(ctx, CreateSessionRequest) (session.RecordInfo, error)` replaces both create calls; `Fork` returns a `RecordInfo`, `Record` a `session.Document`; `Runner.Store()` goes.

No authorization hook ([025](025-callers-credentials-and-quotas-belong-to-the-service.md)): the service's handler filters Agents and tools, and a store wrapper answers `not_found`.

## Not chosen
- Dropping reserved keys, or filtering only in httpapi: a silent drop is the same bug, and in-process callers keep the path.
- `Agent.Options` applied first: the Runner wins, and the option is lost silently.
- `Agent.Options` guarded by the twice-refusal: `Runner.Tools` misses a cap set by option, and a store cannot keep closures (044).
- A per-Session instruction or ceiling setting: the definition is no longer read in one place; a TaskSpec covers it.
- Assertion plus a wrapper conformance check: it catches only where someone runs it.
- A read-only Record interface: a second type over `RecordInfo` and `Document`.
- An `Authorizer` on every Runner method: a second place to decide who may.

## Consequences
Each value has one place to set it, an Agent's instruction and cap come from its definition alone, and a wrapper that loses durability fails at `NewSession`, not weeks later. It breaks every Agent with `Options`, every Transcript wrapper and every caller with settings in metadata; such a caller registers an AgentProfile or TaskSpec. Stored `gotato.instruction` and `gotato.compact_ceiling` are ignored (0.3.0 is unreleased; no migration).

1. gotato: `Capabilities` replaces the assertions in `session.go`, `spill.go`, `toolregistry.go`, `toolrefresh.go`; `session.Record`, `session.Writer` and the Runner's `unjournaled` declare theirs. `NewSession` refuses repeats.
2. service: `Agent.Options` becomes typed fields; `buildSession` builds every option, so it no longer learns ToolSets from the live Session.
3. service: `ReservedPrefix` and `ErrReservedKey` in `CreateSession`, `RunRequest.Metadata`, `RunRequest.Update`; stop reading the two keys.
4. service: `CreateSessionRequest`, `RecordInfo` and `Document` returns; remove `Store()`.
5. httpapi answers a reserved key with 400; the CLI registers an AgentProfile for its instruction and ceiling flags; `run_instruction_test.go`, specs, glossary and principle 10's list follow.
