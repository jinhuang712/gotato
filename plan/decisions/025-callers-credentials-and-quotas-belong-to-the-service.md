# 025. A caller's identity, credential and quota belong to the service around Gotato

Decided: 2026-10-16    Status: current

## The problem
Every service already authenticates its callers in its own way, decides in its own way who may touch which Session, and owns its own quotas. Gotato could build none of it better than the service that has it.

## The choice
Gotato has no authentication. It passes the request's `context.Context`, with its values, to every store and extension call it makes for that request, so the application's own code sees the caller where it enforces its rule. A refusal is `not_found`: a store wrapper returns `session.ErrNotFound` for a Session the caller does not own, which answers 404 and does not reveal that the Session exists. Gotato adds no identity type, no "not allowed" error and no hook, and work it starts on its own, such as an idle compaction, has no caller ([FEATURES.md → G-N22](../../FEATURES.md#g-n22--access-control-belongs-to-applications)). A caller's credential travels in the `ctx` and Gotato stores none, in the Record or anywhere else. A tool reads it from the `ctx` when it calls out; the MCP adapter does this through the team's `http.Client`, whose `RoundTripper` reads the caller from `req.Context()` on every `tools/call`. The model's own credential is different: the gateway reads a provider's API key from its configuration once, and it is the service's. So a tool can act with the caller's authority and the model call runs with the service's. Gotato does no global scheduling or quotas: it offers Runs that are cheap and cancelable, bounds its own concurrent Runs and keeps one Run per Session.

## Not chosen
- Built-in authentication, OAuth or a tenant model: one way for every service, but each already has its own.
- A credential store, or a token kept in an Agent or a Session: a convenience, but a secret in the Record.
- Scheduling resources or provider quotas across services: that is system policy, and the application owns it.

## Consequences
A service enforces access with the code it already has. The cost is that Gotato offers no "who may" answer of its own.
