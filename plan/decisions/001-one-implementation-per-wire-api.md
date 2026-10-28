# 001. A provider is configuration, and Gotato implements each wire API once

Decided: 2026-09-17    Status: current

## The problem
Many vendors offer models, and the list keeps growing. Code for each vendor never ends, and each vendor SDK lands its dependencies in every service that embeds Gotato.

## The choice
A provider is an endpoint, a key, a wire API and its models. Gotato implements each wire API once (`openai-chat-completions`, `openai-responses` and `anthropic-messages`), and any vendor that speaks one of them is a configuration entry. A few providers have their own rules for reasoning and the cache, so the configuration may also name the provider.

## Not chosen
- One adapter per vendor: gives each vendor's full feature set, but needs code for every vendor and the list never ends.
- Vendor SDKs: maintained by the vendors, but their dependencies land in every service.

## Consequences
A new vendor costs a config entry and no vendor SDK enters the service. A vendor with a new wire API waits for its implementation.
