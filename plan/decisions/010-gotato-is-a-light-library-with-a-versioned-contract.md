# 010. Gotato is a light library, tested with fake models, with a versioned contract

Decided: 2026-10-16    Status: current

## The problem
Every dependency, required component and unannounced break lands in every service that embeds Gotato. A service should be able to start small, test without credentials, upgrade Go on its own schedule and know what can change under it.

## The choice
Library first: a small `main` with a fake model runs an agent in-process, and the service and the store are choices. Tests never call a network model: fake and replay models keep them deterministic, and CI needs no credentials. Dependencies point inward: every behavior lives in the packages first, so the library and the service behave the same, and `gotato/layering_test.go` keeps each layer (a service, then `service` and `service/httpapi`, then `session`, `modelctx` and `testkit`, then the core) building only on the layers inside it. The core uses only the standard library, the root module requires only `gopkg.in/yaml.v3`, and the MCP adapter is a module of its own. Core types are Gotato's own, and provider data the core does not understand, such as reasoning signatures, passes through untouched. The root `go.mod` says `go 1.22`. The contract is named and versioned: the exported identifiers of the core and standard runtime packages, the Event types and their fields, the HTTP contract (which carries its own version), the store contract, and the places where many instances behave differently from one. Until the first release any of it may change, with no old names and no shims, and every break has a before-and-after entry in [CHANGELOG.md](../../CHANGELOG.md). From the first release additions are free, and a break is rare, has its entry and raises the version.

## Not chosen
- A required server, daemon or database: more built in, but it is the price of entry.
- Tests that depend on a live model: closer to production, but slow, costly and flaky.
- Behavior that only the service has, or only the test CLI has, or a core that knows about the service: it drifts between the library and the service.
- Vendor SDKs or other libraries that save some code, or one module that carries every dependency: less code to write, but every dependency lands in every embedding service.
- One vendor SDK's types as the core's types: less mapping, but the core is tied to one vendor.
- Following the newest Go release: newer features, but a service should not have to upgrade Go to add a library.
- Changes that slip in unannounced.

## Consequences
A service pulls in only what it uses, and an agent can be run and tested in a few lines. The cost is that Gotato writes small things itself and keeps a mapping from its own types to each provider's. Consumers tolerate unknown Event kinds either way.
