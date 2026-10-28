# 024. Correctness is Gotato's, routing is the infrastructure's

Decided: 2026-10-16    Status: current

## The problem
Running a Session on many instances raises two problems: getting a request to the right instance, and keeping the Session correct once it is there. The first is infrastructure that every team already runs.

## The choice
Gotato takes only the second. It keeps a Session correct on whichever instance a request reaches: it takes over an idle Session and waits for a busy one. A message for a running Session reaches its writer through the same routing as any request. A message that misses the writer is answered `not_writer` with the writer's advertised address, so the caller or the team's own client sends it on. The HTTP endpoints can forward it once, as the same public request, when a team opts in.

## Not chosen
- Forward requests between instances, always: every message reaches the writer at once, but Gotato becomes a router with an internal API and its authentication.
- Redirect the caller as the only routing: no forwarding, but it pushes routing onto every caller, and a load balancer cannot follow a redirect to one instance. The hash stays first.
- Queue every message in the store for the writer: survives the writer's death, but Gotato becomes a message broker.
- A placement or membership service of Gotato's own: central control, but a separate system to run, which [PROPOSAL.md](../../PROPOSAL.md) rules out.

## Consequences
There is nothing new to deploy, it works with a plain consistent hash, and no Session is ever unreachable. How seamless it feels depends on the team's routing, and while a hash changes a message may need a second request.
