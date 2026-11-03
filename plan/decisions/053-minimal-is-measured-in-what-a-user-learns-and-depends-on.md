# 053. Minimal is measured in what a user learns and depends on, not in lines

Decided: 2026-11-03    Status: current

## The problem
Gotato calls itself minimal, and [guideline 5](../../DESIGNS.md#5-keep-subtracting) asks only whether a feature should exist. On 2026-11-03 the shipped packages held about 32,000 lines (22,000 without comments and blank lines) and the core package `gotato` exported 217 names, and nothing said whether that was still minimal.

## The choice
Minimal is measured in what a user has to learn and depend on: the terms of the glossary, the exported names, the settings, the modules it pulls in and the footprint it leaves. Code that keeps a guarantee is fine when it is the least that keeps it. A feature that stays is built with the fewest names and settings that do its job.

## Not chosen
- Lines of code as the measure: a guarantee such as one writer per Session or compaction makes code of its own, and a limit on lines pushes toward shorter code, not fewer concepts.
- Only the dependencies and the footprint, as before: they stayed small while the exported names grew, so they missed the weight a user feels.

## Consequences
A review can ask of each name and setting whether it earns its place, and the numbers can be tracked. The line count may keep growing while the concepts stay few, and that is accepted.
