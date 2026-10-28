# 009. One Loop, extended at fixed stages, with a concrete type until a second one exists

Decided: 2026-10-16    Status: current

## The problem
A better model makes the Loop better with no code change, while every node of a graph is code someone keeps. Every interface drawn early is a guess about the future, and every extension point is weight for each embedding service.

## The choice
There is one Loop, and the model decides the steps ([PHILOSOPHY.md → 1](../../PHILOSOPHY.md#1-what-actually-changed)). Extensions wrap the Loop at a fixed set of stages, so the Loop stays one path a reader can follow. An interface is drawn when a second implementation exists; until then a type is concrete, and a caller that wants a fake defines its own small interface.

## Not chosen
- Graphs, workflows and nodes: a visible plan, but a fixed sequence is written in Go around the Loop.
- A plugin framework, or a fork of the Loop: open to any need, but the Loop is then no longer one path. A need that fits no stage waits for a new stage, added as a reviewed change.
- An interface drawn before a second implementation exists: a guess about the future.

## Consequences
The Loop is short enough to read in one sitting, and a new stage is a reviewed change. A team that needs a fixed sequence writes it around the Loop.
