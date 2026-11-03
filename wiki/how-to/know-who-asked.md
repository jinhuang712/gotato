# Know who asked

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Know who asked

By the end of this page a tool or an extension knows which caller started the Run. You need [Run Sessions on many pods](run-sessions-on-many-pods.md).

## Knowing who asked

A Tool cannot learn who asked from the request: `ToolUse` has no field for it, and nothing in `RunRequest` reaches it. `Metadata` applies when a Session is created, `Update` writes Session metadata, and `RequestID` goes only into `run_started`. What reaches `Before` and `Execute` is the caller's `ctx`. Put the asker into it before `Runner.Run`:

```go
type askerKey struct{}

ctx = context.WithValue(ctx, askerKey{}, "ann")
runner.Run(ctx, host.RunRequest{SessionID: id, Prompt: text})

// in a PreToolUse extension, or in a Tool's Execute:
who, _ := ctx.Value(askerKey{}).(string)
```

- **It lasts one Run.** The value is the one of the call that started the Run.
- **Askers of one Session do not mix.** A Session runs one Run at a time, so each Run sees the value of its own caller.
- **A Run that follows another** (it starts from messages the first Run left queued) runs under the first Run's context without its cancel, so it sees the first caller's value, not the sender's of those messages.

`TestAValueInTheCallersCtxReachesBeforeAndExecute` in [host/host_test.go](../../host/host_test.go) pins this.

## See also

- [Control who may touch a Session](control-who-may-touch-a-session.md)
- [Limit Runs per tenant](limit-runs-per-tenant.md)
