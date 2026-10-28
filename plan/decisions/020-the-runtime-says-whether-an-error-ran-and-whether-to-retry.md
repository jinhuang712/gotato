# 020. The runtime sets whether an error ran and whether a retry may succeed

Decided: 2026-10-25    Status: current

## The problem
A caller that gets an error has two questions before it reads the code: did anything happen, and may I try again? The first decides whether the same request can go to another instance without running twice. Both answers depend on where the error was made.

## The choice
The runtime sets both answers on the error (`gotato.NotRun(err)`, `gotato.Retryable(err)`), and every surface translates them.

## Not chosen
- Each surface infers them from the code: nothing changes in the runtime, but HTTP and each embedding service keep a copy, and the copies drift. An embedding service gets no answer at all.
- The caller infers them from the code: simple for the runtime, but every caller learns which codes are safe, and a new code silently breaks the rule.

## Consequences
A service that embeds the Runner and never meets HTTP reads the same answers as a client behind a load balancer, and a new code is classified once, where it is made. The runtime names two facts that look like transport concerns.
