# 041. A bash for large results lives in its own project

Decided: 2026-10-27    Status: current

## The problem
Often the model wants one line, one field or the lines that match in a spilled Tool Result, which a person would do with `grep` or `jq`. A Session has no machine ([DESIGNS.md](../../DESIGNS.md#a-session-has-no-machine)), so the model has no `grep`.

## The choice
Gotato keeps out a read-only subset of bash over the Session's Record. The engine lives in its own project, [go-bash](https://github.com/jinhuang712/go-bash): a command string, an `fs.FS` and `ctx` in; stdout, stderr and an exit code out. It does not import Gotato. MCP tools and `read_result` ([040](040-a-large-tool-result-is-spilled-and-read-in-pages.md)) are enough for now.

## Not chosen
- Build the bash into Gotato. It was built and tested. It is about 9,500 lines of commands, a jq on a library that is not jq, and conformance tests against GNU that run only in CI, none of it the agent runtime. It also needs two hooks in the core that nothing else uses: one gives a tool the Session's Record, the other shows the Record as a directory.

## Consequences
Paging is slower than a query, but it works on every result. If a bash for large results returns, it keeps these rules: no machine, no `exec`, no sandbox; read-only on its own Session's data; the same Record gives the same output on every instance; a command always ends, honours `ctx` and uses bounded memory; every supported command, flag and piece of syntax behaves exactly as GNU's and bash's, and anything else fails with a clear error; and spill comes first, query after, with `read_result` as the fallback.
