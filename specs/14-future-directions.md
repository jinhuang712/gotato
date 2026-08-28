# 14. Future Directions

**Status:** Exploration

> New capabilities enter the layer that owns their state and failure semantics.

## 1. Core extensions

Possible Core-compatible additions include richer content, context policies, budget allocation, and additional local Tool or Routine policies. They must preserve one loop, one Agent state owner, and one terminal Event.

## 2. Model platform

A Model platform may add:

```text
multi-provider routing
fallback and circuit breaking
quota and cost policy
provider health
batching or caching
```

These remain below or beside the Core Model contract. A Model cache must account for transcript, Model version, Tools, sampling, and external state.

## 3. Durable Hosted state

A Host may add persistent Agent state, Run checkpoints, Event cursors, reconnection, leases, and resumable delivery. Repeated Model calls and Tool side effects require explicit idempotency semantics before resume is normative.

## 4. Reserved: Multi-Pod Conversation ownership

The initial PoC deliberately assumes one Host process in one Pod. Multi-Pod continuity is a separate future design area. Hosted deployments may later add keyed routing, distributed ownership, or durable restoration. A Kubernetes Service's ordinary load balancing is not a conversation guarantee.

## 5. Remote Agent Routines

A Routine Executor may create a child Run through another Host while preserving identity, Context cancellation, limits, Events, and one settled Result.

## 6. Gateway and governance

Authentication, authorization, approvals, audit, credentials, sandbox policies, tenant quotas, and edge routing may integrate at Gateway, Host, or adapter boundaries. They must not enter Core through transport types or hidden global state.

## 7. Promotion rule

A direction becomes normative only after a concrete owner, validated use case, compatibility impact, and deterministic acceptance tests are identified.
