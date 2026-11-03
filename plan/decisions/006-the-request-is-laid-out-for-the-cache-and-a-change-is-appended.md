# 006. The request is laid out for the provider's cache, and a change is appended

Decided: 2026-10-16    Status: current

## The problem
A provider serves the part of a request it has seen before from its prompt cache, at a fraction of the price, but only while every byte of that part is the same. What the model reads costs more than what the service holds, so a prefix that moves, a timestamp in the prompt or a changed tool list makes the provider read the whole history again at full price. Cache syntax and retention also differ from one provider to the next.

## The choice
Stable content comes first and growing content last: the instruction with any reference text, then the tools sorted by ID, then the Transcript. Nothing that changes every Turn enters the request, and a change is told at the end instead of rewritten at the top: a tool the model loads is told in the Tool Result that loaded it, and the tool list keeps its bytes. The core marks where a provider may cache in neutral marks, and each wire API maps them to its own form. One cache key serves a Session, and a Fork inherits its parent's, because it shares the prefix. How long a provider keeps its cache, and what that costs, is set where the provider is configured (`cache_retention`): a recognized provider gets its longest, and an unrecognized one is sent none, because a compatible server may refuse a field it does not know.

## Not chosen
- An order by time or by kind: reads naturally, but one Turn's prefix is no longer the next Turn's.
- A dynamic system prompt, or the current time in every request: always current, but never cached, so it costs its full price on every Turn. A fact the model needs now comes from a tool it calls.
- Rewriting the instruction or the tool list at the top when it changes: the simplest edit, but the provider reads the whole history again. A change a service makes to an Agent in the middle of a Session still does this; the idea of appending it is kept in [FEATURES.md → Later](../../FEATURES.md#later), to be built when the cost shows.
- One provider's cache syntax in the core: no mapping layer, but the core is tied to one vendor.
- A cache key per request or per instance: a provider that keys its cache would then serve one conversation from many caches.
- A retention on each Agent, or one value for every provider: one setting, but how long a cache lives belongs to the provider, and the values differ (an hour on Claude at twice the price of a cache write, 30 minutes on OpenAI's Responses API from GPT-5.6).

## Consequences
Every request is the one before with new content after it, so the provider serves most of it from its cache, on whichever instance sends it. A few decisions change bytes already sent, on purpose and at a known moment, and each says so (see [DESIGNS.md → 2](../../DESIGNS.md#2-a-request-only-grows-at-its-end)). A change to an Agent's instruction in the middle of a Session is the one place the top still moves.
