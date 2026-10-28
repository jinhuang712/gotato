# 008. Only the Agent sets the Effect cap

Decided: 2026-10-16    Status: current

## The problem
Someone has to set the cap. It could be the Agent's definition, or also each Session.

## The choice
Only the Agent sets the cap. A Session can neither raise it nor switch on a hidden tool, and may only switch off a tool of its Agent.

## Not chosen
- A Session can lower the cap too: one Agent could serve several jobs, but we found no case where one purpose-built Agent needs different bounds per conversation. A different bound is a different job, so a different Agent.

## Consequences
The bounds are in one place, the Agent's definition, and a reviewer reads them there. Two jobs with different bounds need two Agents.
