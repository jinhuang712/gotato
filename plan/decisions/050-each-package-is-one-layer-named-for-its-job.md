# 050. Each package is one layer, named for its job

Decided: 2026-11-02    Status: current

## The problem
The package names do not say what is inside them. `service` holds the definitions of Agents, the hosting of many Sessions on many instances and an HTTP API. `session` holds no Session: the live Session is in `gotato`, and `session` holds the Record, the Store contract and the stores. Storage is split between `session` and its suite. `modelctx` is the Context a Record yields, yet sits apart from the Record and imports it. `gateway` bridges to a model and is named for something else. A reader cannot tell from the tree where a job lives, which is how layers got mixed ([DESIGNS → 10](../../DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced)).

## The choice
One package per layer, each named for its job, each importing only the layers below it:

| Package | Its job | Was |
|---|---|---|
| `gotato` | the agent: Session, Loop, Model, Tool, Event; runs with options alone | `gotato` |
| `ref` | the form of a reference, a contract Segments carry forever | `internal/text` in part ([046](046-compaction-is-a-fixed-frame-around-parts-a-user-can-replace.md)) |
| `record` | Gotato's Record: Entry, Segment, Checkpoint, and the Context it yields, compaction and read-back | `session` in part, and `modelctx` |
| `store` | saving: `Records`, `Liveness`, `Definitions`, the Writer and loading, the memory and file stores, one `storetest` | `session` in part |
| `profile` | optional definitions: AgentProfile, TaskSpec, their stored shapes, the Catalog, registration inline or from a store ([044](044-an-agent-profile-and-a-task-spec-are-registered-inline-or-from-a-store.md)) | `service` in part |
| `model` | the bridge to a model over HTTP: wire APIs, connection, retry, classifier ([045](045-the-gateway-is-layered-and-each-layer-can-be-replaced.md)) | `gateway` |
| `host` | hosting many Sessions on many instances: Claims, heartbeat, warm copies, drain, `Admitter` ([048](048-the-runner-and-the-session-are-built-from-parts-and-host-plumbing-is-internal.md)) | `service` |
| `adapter/http` | an optional HTTP API over `host`, its own module | `service/httpapi` |

The order is `host` → `profile` → `store` → `record` → `gotato` → `ref`; `model` and the adapters sit beside them and import down. `store` imports `record`, never the reverse: the Record does not know how it is saved. The core needs no profile; `host` needs one registered in code at least, since any instance must rebuild a Session by name ([principle 8](../../DESIGNS.md#8-no-instance-is-special)).

## Not chosen
- A package named `context`: it collides with the standard library's in nearly every file.
- AgentProfile and TaskSpec in the core: the core must run without them.
- An `agent` package: the core is the agent.
- A store package per stored thing: the store saves; it has no sub-layer per caller.
- Keeping `httpapi` in the root module: a transport is the service's own ([principle 6](../../DESIGNS.md#6-stay-inside-the-boundary)), so it is an adapter, like `mcp`.
- Renames only, packages as they are: the mixed jobs stay mixed.

## Consequences
Every import path changes, and each is a break with a CHANGELOG entry; the layering tests learn the new order. `record` grows large, since the Context lives with the Record it comes from. The moves land after the decisions that reshape what moves (044 to 049), so each file moves once.

1. `modelctx` into `record`; the store half of `session` into `store`, the rest into `record`.
2. `gateway` to `model`; `service` to `host`; `service/httpapi` to `adapter/http` with its own module.
3. `profile` as 044 builds it.
4. The layering tests, guides, specs, glossary and FEATURES follow.
