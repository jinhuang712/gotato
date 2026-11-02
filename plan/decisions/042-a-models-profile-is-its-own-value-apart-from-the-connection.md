# 042. A model's profile is its own value, apart from the connection to it

Decided: 2026-11-02    Status: current

## The problem
[001](001-one-implementation-per-wire-api.md) says a provider is an endpoint, a key, a wire API and its models. `gateway.Config` is one flat struct for one model: the connection (endpoint, key, headers, retries, timeouts) and what the model is (window, provider, cache retention, overflow wording, deferred form) sit among twenty keys, each optional. The window is one of them, and left out it means 0, "unknown", and compaction then has no ceiling, with no word to the team ([038](038-a-limit-guards-a-real-failure.md): a Context past the window has its ceiling). A service that never set it found out only by reading the code (D-034, D-020). Some facts about a model are also held by name inside the code (`thinkingPrefixModels`), because a model has no place of its own to be described.

## The choice
A model is described by a `ModelProfile`, a value apart from the connection: its name, window, provider, cache retention, overflow phrases and deferred form. The window is stated: a number, or "unknown" written out. A connection holds the endpoint, key, headers, retries and timeouts. `gateway.New` takes a connection and a profile, or a reference to one in a set of profiles the team keeps. The team supplies the profiles; Gotato ships no table and knows no gateway by name: where a team gets a window from is its own business. The flat `Config` keeps working as one connection with one inline profile, so no caller breaks; stating the window is asked only of the new form.

## Not chosen
- A model pool that picks a model and falls back to another: that is routing, which stays with the infrastructure ([024](024-gotato-builds-no-infrastructure.md)). A profile says what a model is; it does not choose one.
- A table of windows inside Gotato: models change, and a stale window puts the ceiling in the wrong place, which is harder to find than a missing one.
- A module that reads a window from one gateway's model information: which gateway a team runs is not Gotato's concern, and each such module is a vendor to follow ([001](001-one-implementation-per-wire-api.md)).
- Keep the flat `Config` and warn when the window is 0: the library has no logger to warn with, and the mixing stays.
- Make the window required in the flat `Config` now: it breaks every caller that has none.

## Consequences
Leaving the window out becomes something a team writes down, not something that happens. A team with several models writes each profile once. The cost is a new type and a second way to build a `Client`, with the old form kept until a later release removes it. The ceiling stays a hard line whoever sets it: a Run still past it after a compaction ends, and a team that wants a cost cap writes its own `TurnStopper` ([038](038-a-limit-guards-a-real-failure.md)). Open: where "unknown" is spelled; whether a Session can take a profile per Run (not checked: today an Agent holds one `Model`).
