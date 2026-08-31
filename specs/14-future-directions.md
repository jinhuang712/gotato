# 14. Future Directions

**Status:** Exploration

> **New capabilities belong in the layer that owns their state and failure semantics.**

## 1. Core extensions

Possible Core-compatible additions include richer content, context policies, budget allocation, and additional local Tool or Agent Routine policies. They must preserve one Loop per Agent goroutine, private state confinement, channel boundaries, and one terminal Event per Run.

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

## 4. Reserved: Multi-Pod Conversation routing

The initial PoC deliberately assumes one Host process in one Pod. Multi-Pod continuity is a separate future design area. Hosted deployments may later add keyed routing, distributed ownership, or durable restoration. A Kubernetes Service's ordinary load balancing is not a conversation guarantee.

## 5. Remote Agent Routines

A Host may create or connect an independent Agent goroutine through another Host while preserving command identity, channel-equivalent correlation, cancellation policy, limits, Events, and one settled Result. Remote placement does not create a parent/child resource hierarchy.

## 6. Gateway and governance

Authentication, authorization, approvals, audit, credentials, sandbox policies, tenant quotas, and edge routing may integrate at Gateway, Host, or adapter boundaries. They must not enter Core through protocol types or hidden global state.

## 7. Promotion rule

A direction becomes normative only after a concrete owner, validated use case, compatibility impact, and deterministic acceptance tests are identified.
