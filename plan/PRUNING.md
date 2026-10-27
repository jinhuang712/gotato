# Gotato — Pruning before 0.1.0

The record of [ROADMAP.md → Phase 10](ROADMAP.md#phase-10--pruning): for each step, the inventory, what the maintainer decided item by item, and why. A step's list is written here before anything is removed, and each item names the commit that carried it out.

How the phase works:

- **One step at a time.** A step starts once the one before it has landed, because it looks only at what is left.
- **Evidence first.** Each item says who uses it outside tests, across every module that uses Gotato (the root, `adapter/grpc`, `adapter/mcp`, `examples/larkbot`).
- **One decision per item:** keep, move (out of the public surface or into the module that uses it), merge, or remove.
- **No new feature** while the phase runs.

## Step 1 — Surfaces

Inventory in progress.
