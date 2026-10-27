# Gotato — Pruning before 0.1.0

The record of [ROADMAP.md → Phase 10](ROADMAP.md#phase-10--pruning): for each step, the inventory, what the maintainer decided item by item, and why. A step's list is written here before anything is removed, and each item names the commit that carried it out.

How the phase works:

- **One step at a time.** A step starts once the one before it has landed, because it looks only at what is left.
- **Evidence first.** Each item says who uses it outside tests, across every module that uses Gotato (the root, `adapter/grpc`, `adapter/mcp`, `examples/larkbot`).
- **One decision per item:** keep, move (out of the public surface or into the module that uses it), merge, or remove.
- **No new feature** while the phase runs.

## Step 1 — Surfaces

Inventory of 2026-10-27. "Users" counts code outside `_test.go` files, in every module.

| Item | Code lines | Users | Proposed kind | Decision |
|---|---|---|---|---|
| `adapter/grpc` (module) | 1,195 written, 8,124 generated | only its own binary | remove: about 90% of its operations repeat `service/httpapi`, and no example or service uses it | removed (G-N27), f86c812 |
| `adapter/grpc/cmd/gotato-grpc` | 176 | none | remove: it runs the test kit's fake models and has no test | removed (G-N27), f86c812 |
| `service/httpapi` | 931 | the CLI, `bench/refservice`, larkbot | product: the transport the guides and examples use | open |
| `cmd/gotato` | 1,639 | — | product, as a local tool for a file store; it is never a client of a server | open |
| Echo and demo models in shipped binaries | — | `cmd/gotato`, `gotato-grpc`, larkbot | move out of `testkit`: a shipped binary should not link test fakes | open |
| `testkit` | 2,055 | the binaries above, `service/workload`, larkbot | split: the fakes and recorders stay, the reference-workload tasks leave the public surface | open |
| `testkit/storetest` | 730 | none yet | product: FEATURES promises it to store-adapter authors | open |
| `toolregistry` | 283 | none | remove: `gotato.WithTools`, ToolSets and `Runner.SetToolActive` already cover it; the README quickstart and getting-started guide teach it | open |
| `test/fakegateway` | 1,833 | our tests | our tests; listed in the guides as a test tool | open |
| `test/fakegateway/cmd/fakegateway` | 43 | none | open: larkbot's cluster may run it | open |
| `service/workload`, `cmd/gotato-workload` | 1,129, 239 | each other | our tests: the GOALS measurements | open |
| `bench/refservice` (module) | 82 | — | our tests: G-B10 and G-B12 | open |
| `adapter/mcp` (module) | 693 | larkbot | product: MCP is how Gotato reaches tools it does not build | open |
| `examples/larkbot` (module) | about 1,400 for the bot, about 3,700 for its harness | — | example; its harness is open | open |
| Core directory `gotato-core`, package `gotato` | 205 importing files, all in the root module | — | open: keep, or rename before the release | open |
| `investigation/`, `.serena/`, `.claude/`, `.pi/` | untracked | — | leave the repository, and `.gitignore` them | open |

The product packages `gateway`, `modelctx`, `session` and `service` are kept, and steps 2 and 3 look inside them.
