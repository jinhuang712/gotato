# 028. Every step is written as it happens, as facts and a log

Decided: 2026-10-17    Status: current

## The problem
A crash should lose as little as it can, and a rebuild should read only what it needs.

## The choice
Every step is appended when it happens. Facts are what a rebuild reads: Messages, tool calls that started, metadata changes, Claims and Checkpoints. Log entries record what happened: a Run's start and end, usage, model calls, retries, and the parts of a streaming reply.

## Not chosen
- Write every Turn at its end: one write per Turn, but a crash loses the Turn in flight and the record that a tool started.
- Write every Run at its end: one write per Run, but a crash loses the whole Run.

## Consequences
A crash loses at most the step in flight, and a tool that started is known to have started. A rebuild reads only the facts. The price is a write for every step: eight for a short Run with one tool call.
