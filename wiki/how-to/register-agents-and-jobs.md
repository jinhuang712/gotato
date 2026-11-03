# Register Agents and jobs

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Register Agents and jobs

By the end of this page your service registers the Agents it runs and the jobs that run again, in code or from your own database. You need [Run Sessions on many pods](run-sessions-on-many-pods.md).

## Another instruction is another Agent

A Session has no instruction of its own: the instruction, the model, the tools, the Effect cap, the Sampling and the compaction ceiling all come from its Agent. To answer one message in French, or to give one kind of job a tighter cap, register another Agent and name it on the request.

```go
profiles := profile.NewRegistry(nil)
err := profiles.RegisterAgentProfileInline(profile.AgentProfile{Name: "default", Model: m, Instruction: "You answer questions about open tickets.", Tools: tools})
err = profiles.RegisterAgentProfileInline(profile.AgentProfile{Name: "french", Model: m, Instruction: "You answer questions about open tickets, in French.", Tools: tools})
runner, err := host.New(host.Config{
    Store:        store.Store{Records: st, Liveness: st},
    Profiles:     profiles,
    DefaultAgent: "default",
})

// Answer this message in French. The Session now runs "french", and stays on it
// until a request names another Agent.
out, err := runner.Run(ctx, host.RunRequest{SessionID: id, Agent: "french", Prompt: "what is open today?"})
```

`RunRequest.Agent` is the one way to change a Session's Agent, and it is saved with the Run (`SkipSave` keeps the Session as it was). An AgentProfile holds typed fields and no function that runs on the Session: `Instruction`, `Tools`, `ToolSets` (each a `profile.ToolSet{Set, Mode}`: listed, folded or active), `ToolSources`, `ToolSearcher`, `MaxEffect`, `Sampling`, `Extensions`, `ContextBuilder`, `Limits`, `ReadBack`, `RunTimeout` and `Compact`. There is no `AgentProfile.Options`: the Runner builds every `gotato.SessionOption` itself, so an Agent cannot replace the Writer or raise its own cap. `ReadBack` is the one hole, and only for the read-back tool.

## A job that runs again is a TaskSpec

A report every morning has its own background and wants narrower bounds than the Agent it runs. Register it as a `profile.TaskSpec`: it names its AgentProfile, a Background, and bounds that can only narrow it (a subset of the Tools, an Effect cap no higher, a timeout no longer, a `MaxTokens` no higher).

```go
err = profiles.RegisterTaskSpecInline(profile.TaskSpec{
    Name:       "morning-report",
    Profile:    "default",
    Background: "Report the tickets that opened since yesterday.",
    MaxEffect:  gotato.ToolEffectRead,
})
out, err := runner.Run(ctx, host.RunRequest{Task: "morning-report", Prompt: "Go."})
```

A Run of a TaskSpec opens a new Session, so it takes no `SessionID` and no `Agent`. The Background is added once after the instruction, and the Session keeps the TaskSpec as it was, so another pod builds it the same after you change the registered spec. A spec that widens its profile is refused (`profile.ErrWidens`). `RunRequest.TaskSpec` carries a spec as a value instead of by name. `CreateSession` takes the same `Task` and `TaskSpec` and opens the same Session without running it, so a scheduler creates it with an ID it derives (the job and the day, say), runs it by that ID, and a retry of either call is safe: a second create of the ID is `already_exists`. Over HTTP, `POST /v1/sessions` takes `{"task": "morning-report", "id": "..."}`, a registered name and never a value, and `GET /v1/tasks` lists the names.

## Definitions from your own store

A definition can live in your database instead of your code. Fill a `profile.Catalog` with the models, tools and ToolSets your code offers, and register a name whose document is read from your `store.Definitions`:

```go
catalog, err := profile.NewCatalog(map[string]gotato.Model{"main": m}, tools, nil)
profiles := profile.NewRegistry(catalog)
err = profiles.RegisterAgentProfileWithStore("support", defs)  // defs is a store.Definitions
err = profiles.RegisterTaskSpecWithStore("nightly", defs)
```

The stored document is data only (a model name, Tool names, numbers and text); it selects from the Catalog and never adds to it. Each name is registered once, and a row whose name is not registered is not a definition. [Write a store](write-a-store-adapter.md) says how to build `defs`.

## The four registrations

A service registers the Agents it runs in a `profile.Registry`, which it gives to `host.Config.Profiles` ([decision 044](../../plan/decisions/044-an-agent-profile-and-a-task-spec-are-registered-inline-or-from-a-store.md)). There are four registrations, two for each kind of definition: inline, as a Go value, or from a store, by name. Each name is registered once, and a later registration replaces the earlier.

| Registration | What it registers |
|---|---|
| `RegisterAgentProfileInline(p)` | a `profile.AgentProfile` as a value, under its `Name` |
| `RegisterTaskSpecInline(t)` | a `profile.TaskSpec`, a job that runs again, as a value |
| `RegisterAgentProfileWithStore(name, defs)` | the name of an AgentProfile whose document is read from a `store.Definitions` at each lookup |
| `RegisterTaskSpecWithStore(name, defs)` | the same for a TaskSpec |

```go
profiles := profile.NewRegistry(nil)
err := profiles.RegisterAgentProfileInline(profile.AgentProfile{Name: "support", Model: m, Tools: tools})
err = profiles.RegisterTaskSpecInline(profile.TaskSpec{Name: "morning-report", Profile: "support", Background: "Report on yesterday's tickets."})
runner, err := host.New(host.Config{Store: st, Profiles: profiles, DefaultAgent: "support"})
out, err := runner.Run(ctx, host.RunRequest{Task: "morning-report", Prompt: "Go."})
```

A TaskSpec only narrows its profile (a subset of the Tools, a lower Effect cap, a shorter timeout, a lower `MaxTokens`); one that widens is refused with `profile.ErrWidens`. A Session looks its AgentProfile up by name each time it is built, so a replaced profile reaches new Sessions and never a Run in flight.

## Definitions and the Catalog

`store.Definitions` is a store of documents by kind and name, which a service implements over its own database so that an AgentProfile or a TaskSpec lives there and not in code. It has two methods, `SaveDefinition` and `LoadDefinition` (`ErrNotFound` when none), keeps bytes, and is not a part of `store.Store`. The stored document is data only: a model name, Tool and ToolSet names, numbers and text. The `profile.Catalog`, built with `profile.NewCatalog(models, tools, toolSets)`, holds what those names can point to, so a stored definition selects what your code offers and never adds to it. A row whose name is not registered is not a definition. `store.NewMemoryDefinitions` and `store.NewFileDefinitions(dir)` ship, and `storetest.RunDefinitions` proves your own ([write a store](write-a-store-adapter.md)).

```go
catalog, err := profile.NewCatalog(map[string]gotato.Model{"main": m}, tools, toolSets)
profiles := profile.NewRegistry(catalog)
err = profiles.RegisterAgentProfileWithStore("support", defs) // defs is your store.Definitions
```

## See also

- [How profiles and tasks work](../../specs/profile.md)
- [Agent profiles and tasks, as the user sees them](../../FEATURES.md#f3-agent-profiles-and-tasks)
- [Write a store adapter, for the definitions seam](write-a-store-adapter.md)
