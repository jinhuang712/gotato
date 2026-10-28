# 013. A conformance suite ships with the store contract

Decided: 2026-10-16    Status: current

## The problem
The store is the truth and each team backs it with its own adapter, so a team needs a way to know its adapter keeps what Gotato relies on.

## The choice
The contract ships with a conformance suite, `session/storetest`, a package of its own beside the Store it tests. `storetest.Run(t, backing)` runs every check on a new backing store. The memory and file stores run the same suite.

## Not chosen
- The contract in prose only: nothing to maintain, but each adapter reads the prose its own way, and a wrong reading shows up as a lost Turn.
- A database adapter in the core: works on day one for one database, but rejected by [003](003-gotato-owns-the-shapes-and-adapters-own-storage.md).

## Consequences
An adapter learns where it falls short with one call. The suite is part of the contract, so it changes with it. Keeping it in its own package means importing `session` does not link the `testing` package into a program.
