# Profiles

A service runs more than one kind of agent, and some jobs come back every day. The model, the instruction, the tools and the bounds of each should be written once, found by name from any Instance, and changeable without a deploy when the service wants that. Package `profile` holds these definitions and the Registry that serves them. It is the `profile` layer of the [map](ARCHITECTURE.md): the host asks it, and the core runs without it.

## TL;DR
- An AgentProfile is a kind of Agent. A TaskSpec is a job that runs again, and it only narrows the AgentProfile it names.
- A Registry holds both by name, and each name comes from one place: a Go value, or a document read from a store of the service's own at every lookup.
- A stored document is data only. It names a model, Tools and ToolSets, which a Catalog the service fills resolves, so a row selects what the code offers and never adds to it.
- A Session looks its AgentProfile up by name each time it is built. A Session opened for a TaskSpec keeps that TaskSpec in its Record, so every Instance builds it the same.

## Two definitions

An AgentProfile is like a process and a TaskSpec like a thread of it. The first says what an agent can do, and the second says what one job of it may do.

```text
 AgentProfile   name, instruction, model, Tools, ToolSets (with a mode), Effect cap,
                Sampling, compaction parts, limits, Run timeout, ReadBack (optional)
      ▲ narrowed by
 TaskSpec       name, its AgentProfile, a Background,
                a subset of its Tools and ToolSets, a cap no higher, a timeout no longer, MaxTokens no higher
```

- **Typed fields only.** A profile holds values, not options that run after the Runner's own, and the Runner builds every `SessionOption` from it. So nothing a profile or a stored row carries can replace the Writer or raise the Effect cap (`session.md`, *Who may set what*).
- **No Session state.** A profile is shared by every Session of its kind and holds no Run state. How the host keeps Sessions (`Config.Warm`, `Config.TimeAwayAfter`) is the host's setting, the same for every profile.
- **Sampling, never Facts.** A profile or a TaskSpec sets Sampling hints only. What a model is, its window and its cache retention, belongs to the Model's own `ModelConfig` (`model.md`).

## Registering a definition

Each name is registered once, and a later registration replaces the earlier. There are four registrations, two for each kind.

```text
 host.Config{Profiles, DefaultAgent}
   │ asks by name, each time a Session is built
   ▼
 profile.Registry
   ├─ inline          a Go value                         RegisterAgentProfileInline, RegisterTaskSpecInline
   └─ from a store    a name; the document is read from   RegisterAgentProfileWithStore, RegisterTaskSpecWithStore
                      a store.Definitions at each lookup
                         └─▶ decoded, then resolved through the Catalog
```

There is no fallback store, no chain and no lookup that tries code and then a store: each name says where it comes from, and a row that is not registered by name is not a definition. The store is the service's own, so its query, its cache and its connection are not Gotato's. An inline profile is checked when it is registered (a Model, a known cap and mode, no nil ToolSet or Extension). A stored one is checked when it is read, so a row written after its name was registered is found at the next lookup.

## A stored document is data only

A document holds text and numbers, and names what the Catalog resolves.

| In the document | Resolved from |
|---|---|
| `model`, `tools`, `toolsets` (with a mode) | the Catalog: models by name, Tools by ID, ToolSets by name |
| `instruction`, `max_effect`, `sampling`, `run_timeout`, `limits` | read as they are |
| `compact`: `ceiling_tokens`, `segment_budget_tokens`, `drop_reasoning`, `location` | the numbers of the compaction policy |

The Catalog is built once with `profile.NewCatalog(models, tools, toolSets)`, which refuses a nil value, an empty name and a name two Tools or two ToolSets share. A stored definition selects from it and never adds to it, and a name it lacks is refused with `invalid_argument`. A document with a field the type does not know is refused too, so a typo cannot pass as a setting. The values that are Go code are set inline only: ToolSources, Extensions, a ToolSearcher, a ContextBuilder, a ReadBack and the parts of `Compact`. Encoding a profile that holds one returns `ErrNotData`. A store keeps a document under a kind (`agent_profile` or `task_spec`) and the name, and the document's own `name`, when present, must match the key.

## Narrowing

A TaskSpec may say less than its profile and never more. The check runs once the profile is resolved, whether it came inline or from a store, and again for a Session built from its Record.

| Bound | Narrower means |
|---|---|
| Tools, ToolSets | some of the profile's, by ID or name; none named keeps all, an empty list keeps none |
| Effect cap | no higher; a profile with no cap has nothing to widen |
| Run timeout | no longer than the profile's, which is 30 minutes when it sets none |
| `MaxTokens` | no higher; the other Sampling fields may differ |

A TaskSpec that widens fails with `ErrWidens` (code `invalid_argument`) naming the bound, and a name neither the profile nor the Catalog knows fails with `ErrUnknownName`. Once it passes, the TaskSpec is applied: its Background goes after the instruction, after a blank line, its Tools and ToolSets replace the profile's, and its cap, timeout and Sampling go over it. A TaskSpec that names Tools drops the profile's ToolSources, whose Tools cannot be named. This is what keeps the Effect cap safe: whoever writes a row can only lower it (`tools.md`).

## A lookup for every build

A Session looks its AgentProfile up by name each time the Runner builds it. A profile registered or replaced since reaches new and rebuilt Sessions, on any Instance that shares the store, and never a Run in flight, which keeps the value it was built from. A request that names no Agent runs `Config.DefaultAgent`. A name nothing is registered under is refused before anything is written: `ErrUnknownAgent` for a profile and `ErrUnknownTask` for a TaskSpec, both `invalid_argument` and not run. A Session whose stored Agent is no longer registered is refused the same way until a Run names another.

## A Session of a TaskSpec

A Session is opened for a TaskSpec when it is created, by `CreateSessionRequest.Task` (a registered name) or `.TaskSpec` (a value), or by a Run with no Session, `RunRequest.Task` or `.TaskSpec`. Both go through one function, so they write the same Record, and a caller who chooses the Session ID gets a create it can retry. It resolves the spec, checks that it narrows its profile and keeps it in the Session's metadata under `gotato.task_spec` as a JSON document (`TaskSpecDoc`), next to `gotato.agent`, the TaskSpec's profile.

```text
 create(Task: "nightly-report")
   1 the Registry gives the TaskSpec          (or the value in the request)
   2 Narrowed: the profile, checked, applied  ─▶ refused here, before anything is written
   3 session_created ─ metadata: gotato.agent, gotato.task_spec     one write
 every later build, on any Instance:
   decode gotato.task_spec ─▶ Narrowed against the profile looked up by name ─▶ the live Session
```

The Record's copy, and not the registered spec, builds the Session afterwards, so a spec that changes reaches only new Sessions. A TaskSpec Session takes no `Agent` at creation and refuses a later `RunRequest.Agent`, and a Run of a TaskSpec takes no `SessionID`. The Background is part of the first bytes of every request and is never added per Run, which keeps the prompt cache warm. Where a TaskSpec's result goes, and when it runs, is the service's scheduler and not Gotato's. The `Admission` an Admitter gets names the TaskSpec (`Task`), so a policy can treat a nightly job apart from a chat.

Over HTTP, `POST /v1/sessions` takes `task`, a registered name, beside `agent`, `id` and `metadata`, and excludes `agent`. The body never carries a TaskSpec value, since a definition reaches a service through its code or its store. `GET /v1/agents` and `GET /v1/tasks` list the registered names.

## What this buys you
- A definition lives in code, in a database, or both, and the host asks one Registry.
- A row in a store cannot give an Agent a tool the code does not hold, or raise its cap.
- A job's Background and narrower bounds have a home, and every Instance builds its Session alike after the spec changes.

## See also
[ARCHITECTURE.md](ARCHITECTURE.md), the principle [each layer does one job, and each default can be replaced](../DESIGNS.md#10-each-layer-does-one-job-and-each-default-can-be-replaced), [FEATURES.md](../FEATURES.md) (F3: Agent profiles and tasks), decisions [044](../plan/decisions/044-an-agent-profile-and-a-task-spec-are-registered-inline-or-from-a-store.md), [051](../plan/decisions/051-a-session-is-opened-for-a-task-spec-when-it-is-created.md), [047](../plan/decisions/047-a-value-a-layer-owns-is-set-only-through-that-layer.md), [015](../plan/decisions/015-only-the-agent-sets-the-effect-cap.md) and [050](../plan/decisions/050-each-package-is-one-layer-named-for-its-job.md).
