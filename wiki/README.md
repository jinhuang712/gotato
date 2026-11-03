# Gotato wiki

How-to pages for people who use Gotato in a Go service. The wiki lives in the repository and changes with the code: a page is updated in the same change as the behavior it describes. Why Gotato exists and what it does are in the [README](../README.md); how a part works inside is in [specs/](../specs/ARCHITECTURE.md).

## Start here

- [Install](install.md): adding Gotato to your `go.mod`.
- [Getting started](getting-started.md): a Session, a tool and the Events of a Run.
- [A minimal main](minimal-main.md): a complete program, and the order to shut it down in.

## Build an agent

- [Write a tool and a model](write-a-tool-and-model.md): a Tool, a ToolSet and a Model wrapper.
- [Test with fakes](test-with-fakes.md): scripted models and tools, no network.
- [Extension points](extension-points.md): which seam changes what.
- [Packages](packages.md): which packages to import, and what leaving one out costs.

## Run it in a service

- [Embed Gotato in a service](embed-in-a-service.md): the Runner, many pods and shutting down.
- [HTTP in five minutes](http-in-five-minutes.md): a service you can `curl`.
- [Write a store](write-a-store.md): the Store contract and the suite that checks an adapter.
- [An example chat bot](example-chat-bot.md): several instances behind a chat platform.
