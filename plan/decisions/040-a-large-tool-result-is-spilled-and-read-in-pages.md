# 040. A large Tool Result is spilled into the Record, and read in pages

Decided: 2026-10-25    Status: current

## The problem
A tool can return far more than a Context should hold, and the Context is built from the Record on every Turn. A result over one page, about 8K tokens, would stay in every later request or be refused for its size.

## The choice
The body is spilled into the Record as an Entry of its own. The Context keeps a preview and a ref, and the model reads the rest in pages with `read_result`.

## Not chosen
- Fail the Run past a size limit: simple, but a result the model could have read in pages ends the Run at a size unrelated to the window.
- Cut the result and drop the rest: simple and no extra call, but the model cannot get what was cut and cannot tell whether it matters.

## Consequences
The Context stays small and nothing is lost: the Record keeps the whole body, and the Message that stays is written once, so the bytes already sent do not change. A load does not read the body. It costs a tool call to read more, and a read from the store for the body.
