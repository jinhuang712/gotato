# Gotato wiki

How-to pages for people who use Gotato in a Go service. The wiki lives in the repository and changes with the code: a page is updated in the same change as the behavior it describes. Why Gotato exists and what it does are in the [README](../README.md); how a part works inside is in [specs/](../specs/ARCHITECTURE.md).

## I want to…

| I want to | Go to |
|---|---|
| try Gotato in ten minutes | [Getting started](tutorials/getting-started.md) |
| keep conversations when a pod dies | [Run Sessions on many pods](how-to/run-sessions-on-many-pods.md) |
| give the agent a tool | [Write a tool and a model](how-to/write-a-tool-and-model.md) |
| call a model of my own | [Write a tool and a model](how-to/write-a-tool-and-model.md), [State what your model is](how-to/state-what-your-model-is.md) |
| test my agent without a provider | [Test with fakes](how-to/test-with-fakes.md) |
| serve the agent over HTTP | [Serve over HTTP](how-to/serve-over-http.md) |
| stop a pod without losing a Run | [Shut down without losing a Run](how-to/shut-down-without-losing-a-run.md) |
| keep Sessions in my own database | [Write a store adapter](how-to/write-a-store-adapter.md) |
| limit what a tenant can run | [Limit Runs per tenant](how-to/limit-runs-per-tenant.md) |
| have a person approve a step | [Ask a person before a step](how-to/ask-a-person-before-a-step.md) |
| find the seam that changes a default | [Extension points](reference/extension-points.md) |
| know which packages to import | [Packages](reference/packages.md) |

## Tutorials

Learn by doing, from start to a working result.

- [Getting started](tutorials/getting-started.md): a Session, a tool and the Events of a Run.
- [A minimal main](tutorials/a-minimal-main.md): a complete program, and the order to shut it down in.
- [HTTP in five minutes](tutorials/http-in-five-minutes.md): a service you can `curl`.
- [A chat bot](tutorials/a-chat-bot.md): several instances behind a chat platform.

## How-to

One task on each page. Each assumes you have done [Getting started](tutorials/getting-started.md).

### Build an agent

- [Install](how-to/install.md): adding Gotato to your `go.mod`.
- [Write a tool and a model](how-to/write-a-tool-and-model.md): a Tool, a ToolSet and a Model wrapper.
- [State what your model is](how-to/state-what-your-model-is.md): its window, cache retention and reasoning rule.
- [Add tools that change between Turns](how-to/add-tools-that-change-between-turns.md): sources, groups and your own search.
- [Change what the model sees](how-to/change-what-the-model-sees.md): a builder, a transformer, a block, a rewrite.
- [Get a structured answer](how-to/get-a-structured-answer.md): a value of a shape you choose.
- [Test with fakes](how-to/test-with-fakes.md): scripted models and tools, no network.

### Run it in a service

- [Run Sessions on many pods](how-to/run-sessions-on-many-pods.md): the Runner, a store and any pod.
- [Register Agents and jobs](how-to/register-agents-and-jobs.md): in code or from your database.
- [Serve over HTTP](how-to/serve-over-http.md): mount the handler.
- [Shut down without losing a Run](how-to/shut-down-without-losing-a-run.md): drain, then close.
- [Write a store adapter](how-to/write-a-store-adapter.md): the Store contract and the suite that checks it.

### Keep it under control

- [Add checks and stops to the Loop](how-to/add-checks-and-stops.md): refuse a call, change a result, end a Run.
- [Ask a person before a step](how-to/ask-a-person-before-a-step.md): wait for one, or hand over a ticket.
- [Limit Runs per tenant](how-to/limit-runs-per-tenant.md): decide whether a Run may start.
- [Know who asked](how-to/know-who-asked.md): the caller in the `ctx`.
- [Control who may touch a Session](how-to/control-who-may-touch-a-session.md): a wrapper around your store.

### Go deeper

- [Tune compaction](how-to/tune-compaction.md): when, where, how and who writes the Recap.
- [Compact your own Transcript](how-to/compact-your-own-transcript.md): a store that is not a `record.Record`.
- [Read back from your own store](how-to/read-back-from-your-own-store.md): spilled results and old Segments.
- [Customize the model bridge](how-to/customize-the-model-bridge.md): the wire, the connection, the retry, the error.

## Reference

Look a thing up.

- [Extension points](reference/extension-points.md): which seam changes what, and which page explains it.
- [Packages](reference/packages.md): which to import, what leaving one out costs, and who imports whom.

## How the wiki is kept

- **One page, one thing.** A tutorial teaches, a how-to finishes one task, a reference is looked up. Why it works this way is not here: the specs explain the mechanism, [PHILOSOPHY](../PHILOSOPHY.md) the reasons.
- **The wiki holds no fact that another document owns.** What a feature does is in [FEATURES](../FEATURES.md), the defaults of the host are in the [RUNBOOK](../RUNBOOK.md), how a part works is in its spec. A page points to them.
- **Every page** opens with a breadcrumb and "By the end of this page…", states what it needs, and ends with a See also.
- **Every block of code** is run by a test the page names, or the page says it has not been run.
- **A page changes in the same change as the behavior it describes,** like the other documents.
- **A new page** starts from [the template](_template.md) and is added to this page.
