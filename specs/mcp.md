# MCP adapter

A team that already runs tools as MCP servers should not wrap each one in Go. The MCP adapter turns one server into one tool set of the core, so its tools reach a Session through the same catalog, Effect cap and checks as any other tool. It builds tools only, on the official MCP Go SDK, and it is a Go module of its own, so a service without MCP carries none of it. It is the `adapter/mcp/` part of the [map](ARCHITECTURE.md).

## One server, one tool set, one connection

A server is named once, and its tools are `<name>.<tool>` to the model. Every Session of the instance that installs the same tool set shares one connection to the server, and for a subprocess one process.

```text
 Sessions of one instance              the tool set                         MCP server
   a Run starts: resolve ───────────▶ connect once (10 s at most) ───────▶ initialize
                                      list the tools ─────────────────────▶ tools/list
   ◀── tools, with Effect and schema                                          │
   a call, with the Run's context ──▶ tools/call + progress token ─────────▶ │
   ◀── Tool Result; progress as Events ◀──────────────────────────────────────┘
          the session is forgotten (404) ─▶ connect again, send the call once more
```

Creating the tool set connects to nothing, so a server that is down never fails the setup. The first resolution connects, bounded by a connect timeout, 10 seconds by default. Callers that arrive while it connects wait for it, each only as long as its own context. A connection that closes, or a session the server forgot after a restart (it answers 404), is dropped and made again. A call the server answered with 404 is sent once more on the new session, since the server never ran it; any other failed call goes back to the caller. The code that made the tool set closes it; a Session never does.

Two transports are offered. Streamable HTTP is used without state: nothing of the server is kept for a Gotato Session, so any instance can make any call. A subprocess speaks MCP over its standard input and output, and a new command is made at every connect. A stateful server behind a round-robin balancer can answer 404 to a session another replica holds; the adapter tries once more on a new session, and what still fails reaches the model as a tool error, so such a server should be stateless or sit behind session affinity.

## The tool list

At every resolution, once per Run, the adapter asks the server for its tools, unless a list cache is set: then a list is kept for that long, until the server says its list changed or the tool set connected again. The default asks every time, which sees a change even from a server that cannot notify.

Each tool on the list passes four steps before the core sees it:

```text
 server's tool ─▶ filter ─▶ rename ─▶ Effect ─▶ slim schema ─▶ a tool of the core
                  keep it?   the name   from the      drop what costs tokens
                             the model  annotations   and says nothing
                             sees
```

The filter sees the tool as the server sent it, under the server's own name. A rename changes only the name the model sees: the Effect, the filter and the call to the server keep the server's own name. A tool whose new name is empty or taken is left out. The schema loses `$schema`, `$id`, `examples` and a title that only repeats a name, and its keys are sorted, so the bytes are the same on every instance. A tool the server describes wrongly, or one the filter or a rename leaves out, is reported with the reason, and one bad entry never takes the good tools of the server with it. A panic in the filter or the rename leaves that tool out and goes no further.

The tool set's description in the catalog is the team's own text, or else the server's instructions as they were at the first connect. It stays the same across reconnects, because it is part of the prompt and a changing one would be a new prefix.

## Effect from the annotations

A tool's Effect is chosen in a fixed order:

1. the Effect the team set for that tool, by its name on the server;
2. the Effect its annotations give;
3. the Effect the team set for a server that sends no annotations at all;
4. `destructive`.

The annotations map with the MCP specification's defaults for a hint left out: read-only is `read`; not destructive in a closed world is `write_local`; not destructive in an open world is `write_external`; anything else is `destructive`. A tool with annotations that declare nothing is not a tool without annotations: the defaults make it destructive. A tool's name is never read as its Effect.

The adapter's tools are not sequential, and the core groups calls without looking at their Effect, so when a Session allows tools in parallel a server's writes run beside its reads. A team that wants them alone marks every tool above `read` as sequential with one option.

## A call

A call passes the Run's context to the SDK, so stopping the Run cancels the call. The server's progress notifications become the tool's progress Events.

What comes back is mapped to what the model can read:

| The server sends | The model reads |
|---|---|
| text | a text part |
| an image | an image part |
| structured content | a JSON part; a text block that only repeats it is dropped |
| an embedded text resource | its text under a header with its URI |
| an embedded image | an image part |
| audio, a resource link, another resource | one line that names the content and its size |
| a result marked as an error | a failed Tool Result whose content the model reads |

`_meta`, on the result and on each block, is copied as strings without the key that marks Gotato's own text, so a server cannot write tagged text as the system. A large text result is spilled like any other.

A transport or protocol failure never reaches the model in the SDK's words, which name URLs and hosts: the model reads only that the server did not take the call, and the SDK's error stays on the cause for logs. A tool that asks for input in the middle of a call, through elicitation, sampling or roots, gets no answer, because there is no person to ask, and the call ends with a failed result that says so.

## Authentication

Authentication is the HTTP client the team passes: a fixed header, a refreshing token or mutual TLS. A round tripper sees each request's context, so it can send the end user's token on each call though the connection is shared. The client's overall timeout must stay unset, because it cuts the long-lived event stream the transport keeps open; a response-header timeout and the Run's context bound a call instead. The MCP authorization flow is not offered.

## What this buys you
- A team's MCP servers become tools with no Go wrapper, under the same cap and checks as its own tools.
- One connection per server per instance, with no state of a Session on the server, so any instance can make any call.
- A server that is down, lists a bad tool or restarts costs that server's tools for one Run, never the Session.

## See also
[ARCHITECTURE.md](ARCHITECTURE.md), the guidelines [stay inside the boundary](../DESIGNS.md#6-stay-inside-the-boundary) and [what an Agent may do is read from its definition](../DESIGNS.md#7-what-an-agent-may-do-is-read-from-its-definition), [FEATURES.md](../FEATURES.md) ([F13: MCP adapter](../FEATURES.md#f13-mcp-adapter)), decisions [011: Bounds and tool output](../plan/decisions/011-an-agents-bounds-are-its-tools-and-its-effect-cap.md), [025: Callers, credentials, quotas](../plan/decisions/025-callers-credentials-and-quotas-belong-to-the-service.md), [026: No machine, client or catalog](../plan/decisions/026-gotato-ships-no-machine-no-client-and-no-tool-catalog.md) and [039: Tools load from a catalog](../plan/decisions/039-tools-load-one-by-one-from-a-catalog.md).
