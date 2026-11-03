# Limit Runs per tenant

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Limit Runs per tenant

By the end of this page your service decides whether a Run may start, for example with a limit for each tenant. You need [Run Sessions on many pods](run-sessions-on-many-pods.md).

## Decide whether a Run may start

Two things decide whether a Run starts on a pod, in this order ([decision 048](../../plan/decisions/048-the-runner-and-the-session-are-built-from-parts-and-host-plumbing-is-internal.md)).

`host.Bounds` guards the pod itself: `MaxActiveSessions` (the Sessions it keeps, active and warm, with the eviction of the longest-idle warm copy before a Run is refused as `overloaded`), `MaxWarmSessions`, and a `Queue` policy for a Session that is busy (`reject` or `wait`). A draining pod refuses first. These are not a policy: they run whatever the `Admitter` says.

`host.Admitter` is the policy of the service, and the one place a Runner asks it. `Admit(ctx, host.Admission, host.Load) (release func(), err error)` is given the Run (`Admission{SessionID, Agent, Task}`: the AgentProfile and, for a Run of a TaskSpec, the TaskSpec's name), what the pod holds (`Load{ActiveRuns, ActiveSessions, WarmSessions}`), and the caller's `ctx`, which carries who asks. The default is `host.Limits{MaxActiveRuns: n}`: refuse past n Runs in flight, and no limit at zero. A limit for each tenant is a few lines:

```go
type perTenant struct {
	mu    sync.Mutex
	limit map[string]int
	held  map[string]int
}

func (p *perTenant) Admit(ctx context.Context, _ host.Admission, _ host.Load) (func(), error) {
	tenant, _ := ctx.Value(tenantKey{}).(string)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.held[tenant] >= p.limit[tenant] {
		return nil, gotato.ErrorOf(gotato.CodeLimitExceeded, "tenant is at its limit of runs")
	}
	p.held[tenant]++
	return func() { p.mu.Lock(); p.held[tenant]--; p.mu.Unlock() }, nil // the Runner calls it once when the Run ends
}
```

A refusal is the error `Admit` returns, marked not run, so the load balancer can send the Run to another pod; an error that is not typed is answered as `limit_exceeded` (HTTP 429). `Admit` runs with the Runner's table of Runs locked, so it must return quickly and must not call the Runner; the `Load` it sees and the Run it admits are one step. A Run that follows another with the messages the first left is part of the Run already admitted and is not asked again.

## See also

- [How admission works](../../specs/host.md)
- [Admission, as the user sees it](../../FEATURES.md#f98-admission)
- [Know who asked](know-who-asked.md)
