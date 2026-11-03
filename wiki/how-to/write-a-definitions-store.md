# Write a definitions store

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Write a definitions store

By the end of this page your service keeps its AgentProfile and TaskSpec definitions in its own database. You need [Write a store adapter](write-a-store-adapter.md) and [Register Agents and jobs](register-agents-and-jobs.md).

## Implement `store.Definitions`

A service that keeps its AgentProfile and TaskSpec definitions in its own database implements a second, smaller contract, `store.Definitions`. It is not a part of `store.Store`, which has no field for it: you pass it to `RegisterAgentProfileWithStore` or `RegisterTaskSpecWithStore` on a `profile.Registry`, and a service that registers its Agents in code needs none of it.

```go
type Definitions interface {
	SaveDefinition(ctx context.Context, kind, name string, doc []byte) error
	LoadDefinition(ctx context.Context, kind, name string) ([]byte, error) // errors.Is ErrNotFound when none
}
```

- **A document is bytes.** The store keeps what it was given, copies it in and out, and never reads it; `profile` owns the format.
- **Keys are refused alike.** Call `store.CheckDefinitionKey(kind, name)` first, and return its `ErrInvalidID` for an empty key, one over `store.MaxDefinitionKeyBytes`, or one with a control character, a path separator, `..` or a leading `.`.
- **Prove it** with `storetest.RunDefinitions(t, backing)`, in the same shape as the other suites. `store.NewMemoryDefinitions()` and `store.NewFileDefinitions(dir)` are the reference stores.

## See also

- [How definitions are registered](../../specs/profile.md)
- [How the store contract works](../../specs/store.md)
