# 017. A notice is a Message of its own kind, kept in the Record

Decided: 2026-10-18    Status: current

## The problem
Gotato and the service sometimes have to tell the model something the user did not say: the last reply was cut at the output limit, the user has been away for hours, or something happened outside the conversation.

## The choice
A notice is a Message of its own kind, appended once to the Record and never changed. Each wire API carries it as tagged text in a user turn.

## Not chosen
- A tagged user Message: the simplest, but it starts a new Round, so Claude and OpenAI lose the reasoning in flight, and the model may take it as the user's request.
- Text added to each request and never kept: nothing stored, but its place and words can change between requests and instances, which breaks the cache, and Claude refuses a thinking block whose prefix changed.

## Consequences
The model never takes a notice for the user, and it never starts a Round. The cache and Claude's check of thinking blocks hold. It is a fourth kind of Message, which each wire API carries in a user turn.
