# 015. Every Entry is an Event, and the Record is the Session's Event log

Decided: 2026-10-18    Status: current

## The problem
A Session's Record and its Events tell the same story. Kept apart, they say it twice under two sets of names, and an Event number that counts within one Run means nothing to another instance.

## The choice
Every Entry is an Event, and the Record is the Session's Event log. An Event's number is its Entry's position in the Record.

## Not chosen
- Events replace the log entries only: half the duplication goes, but facts and Events still say the same things twice.
- Events kept apart from the Record: no change, but everything is said twice and only one instance can resume a stream.

## Consequences
There is one set of names and one copy, and every instance reads the same number, so a stream resumes anywhere. The number counts the Record, not the Run, so the HTTP contract carries a version for it.
