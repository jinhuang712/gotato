# 014. A tool above the Effect cap is hidden from the model

Decided: 2026-10-16    Status: current

## The problem
The cap decides which tools a model may use. A tool above it can be refused when called, or never shown.

## The choice
A tool above the cap is hidden: it is in no model request, no inspected Context and no tool listing. A model that calls a hidden name gets a failed result that says the Session does not allow it.

## Not chosen
- Offer every tool and refuse a call above the cap: the model learns why a call failed, but it keeps choosing tools it may not use, and each refusal costs a Turn.

## Consequences
The model cannot plan around a tool it never sees, and spends no Turn on a call that fails. There are fewer tool definitions in the Context.
