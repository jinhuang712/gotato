# 012. A Run is stopped through its context, and the Record is the trace

Decided: 2026-10-16    Status: current

## The problem
A cloud Session runs with nobody watching each step. It must be stoppable at any point, and it must leave enough behind to see what it did.

## The choice
`context.Context` is the only cancellation: every model call, tool call, store call and stream takes one, so stopping a Run reaches all of them. `Abort` stops one Run, and `Drain` then `Close` stops one Instance; both end through `ctx`, and the Run settles as `canceled` with every Turn it committed. Which Runs belong together is a role, and roles belong to applications, so an application that keeps its group in `Session.Metadata` lists the Sessions, aborts each and revokes its own credentials. The Record is the trace: it already holds every step as an Entry, in order, and only the Session's `session.Writer` appends to it, under the Instance's Claim. A tool gets its `ctx`, its call and a progress function, and the model gets only what the Loop commits, so neither can write to the Record. This is a promise about Gotato's code, not about the store: whoever operates the store can still change or delete a Record, and Entries are not signed. A team that needs more wraps the store, as for access control.

## Not chosen
- A cancellation mechanism of Gotato's own: Go code already cancels through `ctx`.
- A kill switch for a group of Agents inside the runtime: one call for an application, but a group is a role.
- A separate audit log, or a trace the Session can write to: a second copy that can drift from the Record.

## Consequences
Stopping and tracing add no mechanism of their own. The price is that Gotato has no call that stops a group, and that tamper evidence is the store's job.
