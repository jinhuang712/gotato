# Shut down without losing a Run

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Shut down without losing a Run

By the end of this page a pod stops on SIGTERM without losing a Run in flight. You need [Run Sessions on many pods](run-sessions-on-many-pods.md).

## Drain, then close

On SIGTERM, call `runner.Drain(ctx)`: the pod stops taking new Runs, lets each Run finish, save and release its Session, and answers 503 on `/readyz` so the load balancer sends traffic elsewhere. Then `runner.Close(ctx)` removes the pod's row, and other pods take its Sessions over at once. In the maintainer's rolling-deploy runs on a local cluster, no message was lost this way.

The complete program, with the order of the three steps shown in a run, is [A minimal main](../tutorials/a-minimal-main.md).

## See also

- [How drain works](../../specs/host.md)
- [Drain, as the user sees it](../../FEATURES.md#f99-drain)
