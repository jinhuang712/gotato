# 033. Code builds a Segment, and the Agent's own model writes only the Recap

Decided: 2026-10-19    Status: current

## The problem
A Segment should keep what code can keep exactly, and a model that summarizes a whole stretch can lose it. The request that asks for the Recap should be cheap, and the text Gotato writes into the conversation must not be something a user, a tool or the model can forge.

## The choice
Code builds the Segment: the user's messages and each Run's final reply stay word for word, and each tool call becomes one line inside its Run; reasoning, a Run's intermediate text and the tool results are left out. The model writes only what code cannot, the Recap, with the facts the tool results held, the decisions, what is still open and where a Run stands. It comes last, so a mistake by the model cannot damage the rest. The Agent's own model writes it, on the cached prefix: the request is the Context the next model call would send, with one plain user message after it that asks for the Recap, never stored and carrying no tag. A Recap that fails does not stop the compaction: the Segment is kept without one, with the conversation and its tool call lines, and a merge of Segments is written from the Recaps alone, by code when no model writes it. Gotato writes only four tags into the conversation: `<notice>` for a runtime fact, `<segment>`, `<tool_calls>` and `<recap>`, with Markdown headings inside, and a system sentence says their text is written by the system. Any text that imitates a tag is escaped.

## Not chosen
- Asking a model to summarize the whole stretch: less code, but what the user said is no longer exact.
- A separate model, or a request built only to summarize: a different cache, so the whole prefix is read at full price.
- A notice that carries a summary of the history, or history and notices as untagged text: a notice tells the model a fact of the moment, and a compacted stretch of history is something else.

## Consequences
A mistake by the model costs a Recap and never the conversation, and the Recap request reads the cache. The detail of what is kept and how it fails is in [FEATURES.md → G-F06](../../FEATURES.md#g-f06--compaction-).
