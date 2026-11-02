# 044. An Agent profile and a task spec are registered inline or from a store

Decided: 2026-11-02    Status: current

## The problem
`service.Agent` mixes what an Agent can do, how its Sessions are hosted and Go values in one struct. The Runner fills its Agents once in `service.New`, so a service cannot add, replace or load one without a new Runner, and a definition kept in a database has no way in. A job that runs again and again (a report each morning) has no place for its own background and narrower bounds.

## The choice
An optional package, `profile` ([050](050-each-package-is-one-layer-named-for-its-job.md)), holds two definitions. The core runs without either.
- An **AgentProfile** (today's `Agent`) is a kind of Agent, like a process: name, instruction, model, tools and ToolSets, Effect cap, Sampling ([045](045-the-gateway-is-layered-and-each-layer-can-be-replaced.md)), compaction parts and limits, and a Run timeout. How the host keeps its Sessions (warm time, time-away notice) is the host's setting, not the profile's.
- A **TaskSpec** is a job that runs again, like a thread of that process: a name, its AgentProfile, a Background, and bounds that only narrow: a subset of the tools, a cap no higher, a timeout no longer, a `MaxTokens` no higher; its other Sampling fields may differ. A profile or a spec sets Sampling only, never a model's Facts.

A run of a TaskSpec opens a new Session. Its Background is added once, after the instruction, and the TaskSpec is kept in the Record as it was, so every instance reads the same Session after the spec changes. A Session looks up its AgentProfile by name each time it is built, so a replaced profile reaches new and rebuilt Sessions, never a Run in flight.

Each definition is registered one of two ways, each name once, a later registration replacing an earlier: inline, as a value (`RegisterAgentProfileInline(p)`, `RegisterTaskSpecInline(t)`), or from a store, by name (`RegisterAgentProfileWithStore(name, defs)`, `RegisterTaskSpecWithStore(name, defs)`). `defs` is a `store.Definitions`, which saves and reads a document by kind and name and knows nothing of profiles. `profile` owns the documents (`agent_profile_doc.go`, `task_spec_doc.go`): data only, tool and ToolSet names and a model name, resolved through a **Catalog** the service fills with its Go tools and models, so a stored definition selects what the code offers and never adds to it. After any value is resolved, inline or stored, one check refuses a TaskSpec that widens its profile and a name the Catalog lacks. A `RunRequest` names a TaskSpec in `Task`, passes one in `TaskSpec`, or neither.

## Not chosen
- AgentProfile and TaskSpec in the core: the core must run on options alone.
- Keep the name `Agent`: most libraries mean the running thing.
- One registration call that looks in code, then in a store: two explicit ways say where each name comes from, with no order to get wrong.
- A store that answers for every name not registered: nothing needs it, and a new row would become a usable Agent unseen.
- Gotato loading from a database: the query, its cache and the connection are the service's ([003](003-gotato-owns-the-shapes-and-adapters-own-storage.md), [024](024-gotato-builds-no-infrastructure.md)).
- Storing an AgentProfile as it is: it holds Go values.
- A TaskSpec that widens: whoever writes a row could pass the cap ([015](015-only-the-agent-sets-the-effect-cap.md)).
- A TaskSpec that carries on an earlier Session: nothing needs it yet.
- A Background per Run: it changes the start of every request ([006](006-the-request-is-laid-out-for-the-cache-and-a-change-is-appended.md)).
- Where a TaskSpec's result goes, and when it runs: the service's scheduler and outbox.

## Consequences
Definitions live in code, in a database, or both, and the host asks one registry. A row in a store becomes usable once its name is registered, limited to the Catalog. The cost is a break where `service.Agent`, `Config.Agents` and `Runner.Agent` were, and a Catalog to fill for stored definitions. Stored Records still read: the JSON name `agent` stays.

1. `AgentProfile` replaces `Agent`; warm and time-away settings move to the host's config.
2. `profile.Registry` with the four registrations, used by the host in place of `Config.Agents`.
3. `TaskSpec`, its narrowing check, `RunRequest.Task` and `RunRequest.TaskSpec`, its snapshot in the Record.
4. `store.Definitions`, its memory store and suite; the two documents and the `Catalog`.
