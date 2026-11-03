# 052. The footprint is a measure and has no limit

Decided: 2026-11-03    Status: current

## The problem
A reflection bug put 1.6 MB into the reference service (10.6 MB, and 9.0 MB after the fix) and was found by a measurement made by hand. The footprint goal only records a value, so nothing failed. The question was whether it should fail past a limit, under [guideline 5](../../DESIGNS.md#5-keep-subtracting).

## The choice
The footprint stays a measure: it is taken at each checkpoint, set against Google ADK for Go, and read by a person. It has no limit and no check that fails.

## Not chosen
- A threshold on the stripped binary, such as 10 MB: it fails at once, but the size depends on the platform and the Go version, so a fixed number gives false alarms, and the number itself would be a guess.
- A benchmark against the previous release, failing past a percentage: the first release recorded no size to compare with, and the percentage is another guess.

## Consequences
A jump in size is found by whoever reads the measure at a checkpoint, and nothing stops it before. If a jump goes unseen again, this is the decision to revisit.
