# Philosophy

Agents are moving off developers' machines and into the systems they serve. The work they do there is not new. What is new is how little it takes: a loop around a capable model now does work that used to need a graph framework, and most of that work needs tools and data, not a machine.

> Agents can handle sophisticated tasks, but their implementation is often straightforward. They are typically just LLMs using tools based on environmental feedback in a loop.
>
> — Erik S. and Barry Zhang, *Building effective agents*, Anthropic, 2024

Gotato is built for that kind of agent: purpose-built, running inside an ordinary service, and light enough to have many of. This page asks four questions about that world and answers each one. The answers are reasoned for the cloud. A developer's own tool can embed Gotato, and the answers may weigh differently on a person's machine.

## 1. What actually changed?

**The model keeps taking over the harness, so the harness should stay thin.**

The work agents do in a service is not new. Teams built it before, mostly in Python or TypeScript, on graph frameworks such as LangGraph, Deep Agents and Langflow. In our experience it worked, more or less, at a high price: costly and slow to run, hard to build and debug, and not clearly better than a plain loop.

The graphs were there for a reason. They made up for weaker models:

- a model that could not plan got its plan drawn as a graph;
- a model that lost track got a node to hold its place;
- a model that picked the wrong tool got a branch that picked for it.

Each node encoded something the model could not do *at the time*. Models keep getting better at exactly these things: planning, choosing tools and recovering from errors. Each improvement turns another part of the harness into dead weight that someone still has to maintain. A thin loop ages the other way: each better model makes it better, with no code change.

The labs say the same about their own harnesses:

> A common thread across this work is that harnesses encode assumptions about what Claude can’t do on its own. However, those assumptions need to be frequently questioned because they can go stale as models improve.
>
> — Lance Martin, Gabe Cemaj and Michael Cohen, *Scaling Managed Agents: Decoupling the brain from the hands*, Anthropic, 2026

The pattern is older than agents. Across decades of AI research, knowledge built in by hand won early and lost later to general methods:

> We have to learn the bitter lesson that building in how we think we think does not work in the long run.
>
> — Rich Sutton, *The Bitter Lesson*, 2019

**The other side.** Graphs give determinism, control and an audit trail, and regulated work needs to know which step runs when. That is true, and the determinism already has a home: the service's own code. Rules, pipeline steps and the order of checks stay in ordinary Go. The loop is called for the cases the code cannot settle, as one step inside it. A team does not need a graph around the model, because it already has a program around it. This is also why the loop belongs in the service's own language: it is one more function the code calls.

**What it costs.** Gotato gives up the drawn flow and the control per node that graph frameworks offer. A team that wants a fixed sequence writes it in Go. The stance also bets that models keep improving. If they stall, the loop stays thin, and the extra care goes into the tools and the instruction instead of into a graph.

## 2. What belongs in a cloud session?

**Cloud agent sessions are not for everything, and Gotato does not try to copy a remote dev environment.**

One popular direction is to move a developer's whole session into the cloud, environment and all, so that it keeps running when the laptop closes and can be checked from a phone. A developer's machine is harder to move than it looks. It is a small operating system built up over years:

- toolchains, browsers and Xcode with its simulators;
- credentials and VPN access, caches and local services;
- sometimes a GPU.

A container holds what someone wrote down. A laptop is mostly what nobody wrote down. Copying it to the cloud means rebuilding that system online, one missing piece at a time.

Even a perfect copy still needs the person. A remote session cannot open a browser for you to look at a page, type your password or approve a sign-in prompt. Those steps come back to your own device anyway. People have done their development on laptops for decades, and a laptop is a rich environment. A cloud copy of it is a poorer one that still needs you.

Coding moved to the cloud first, yet coding needs the most environment, which makes it the worst fit. The best fit needs no environment at all: looking up an order, investigating an alert, deciding a hard case from the service's own data. That work needs tools and data, not a computer:

> An agent that answers questions or uses tools to access external services may not need its own compute or files.
>
> — OpenAI, *Agents API: architecture*

**The other side.** Cloud dev environments and cloud coding sessions work well for many web and backend projects that already build in CI, and running many attempts in parallel is a real gain. The gain is small next to its machinery, though: sandboxes, snapshots, remote browsers, environment sync and credential brokers, all to run in the cloud what mostly runs fine on a laptop. Coding products can carry that machinery. Gotato does not compete with them.

**What it costs.** A Gotato agent does not build code, run a browser or execute programs. Work that needs those stays on a laptop, in CI, or in a coding product.

## 3. How is a cloud session different from a local one?

**A general agent needs people. A purpose-built agent that needs people needs fixing.**

A local session borrows a person. A cloud session has to work without one:

| | Local session | Cloud session |
|---|---|---|
| Runs on | the person's own machine | a service, on whichever instance is free |
| Who sees the steps | the person, one by one | nobody; the sender sees the answer |
| Conversations | one at a time | many at once, most of them idle |
| Lives as long as | the person keeps it open | the conversation needs, longer than any process |

A person may well start a cloud session, by messaging a bot or filing a ticket. What changes is that nobody is there to approve each step.

General agents plan for that gap with people. A lab product serves anyone, with any request and thousands of plugins. It cannot know the tools or the jobs in advance, so it plans for a person to step in, and that is right for it:

> High-risk actions: Actions that are sensitive, irreversible, or have high stakes should trigger human oversight until confidence in the agent’s reliability grows.
>
> — OpenAI, *A practical guide to building agents*, 2025

A purpose-built agent is different. It runs privately, with a fixed set of tools, for jobs the team can predict. Every tool, credential and data source was chosen on purpose. Its bounds come from what it is given, not from someone watching it work. If it still hits a wall, such as an expired token, a missing tool or an MCP server that is down, something in that set is broken. The fix belongs in the stack, not in a pause that waits for a person.

A wall is not the same as a hand-off. A refund above a limit, or a change to production, can need a person by design. That is a step in the business process: the step is handed over, for example by opening a ticket, and the session finishes its job.

A change to a production system is the clearest case. Most of what such an agent does ends in a report or a decision handed back to the service: investigating comes before acting. We take the stance that an agent does not change a production system on its own. The change is handed over, as a ticket or a message to the service, and the service's own code or a person makes it. Gotato ships no tool that changes a system. A team that writes one owns that choice, and the Effect cap bounds it.

The gap shows in time as well. A cloud session outlives the instance that serves it, so the instance is disposable and the conversation is not:

> The harness also became cattle. Because the session log sits outside the harness, nothing in the harness needs to survive a crash.
>
> — Lance Martin, Gabe Cemaj and Michael Cohen, *Scaling Managed Agents: Decoupling the brain from the hands*, Anthropic, 2026

**The other side.** Requests drift, and models fail even when the tools are right. A bot built for order questions gets asked about holidays. Declining is part of its design: the instruction and the tools define its scope, and "that is not something I handle" is an answer, not a person stepping in. When the model gets a case wrong, the fix is a better instruction, better tools or a better model. A person pausing every run hides the failure instead of fixing it.

**What it costs.** A purpose-built agent does less than a general one, and teams spend their care on the tools they give it. Teams may also keep adding tools until the agent is general again, and then it needs people again. Splitting it into smaller agents may be the answer. It also gives up the agent that repairs a production system itself: the repair waits for the service or a person. This is the stance we are least sure of.

## 4. Where is Agent-as-a-Service going?

**Agent services will scale with the number of users, not with the number of sandboxes.**

This is an optimistic view, and it is stated as one. It starts from the work purpose-built agents already do well. Each kind is work that rules cannot write down and one model call cannot settle:

| Work | What it looks like |
|---|---|
| The hard cases inside a pipeline | A user uploads hundreds of files for an identity check. Rules clear most of them, and a single model call clears most of the rest. When that call is not confident, an agent looks at past submissions and other sources, calls tools and decides. An `if` cannot express that step, one call is not enough, and running an agent on every file would be slow and expensive. |
| Investigations | On a schedule or an alert, an agent looks for dirty data with one clear goal, reads the state of a system and appends a short report for people to read later. Or it investigates an error, writes up what happened and tells the people responsible. |
| Conversations with users | The service's own tools and records answer them: rebooking a canceled flight, explaining a double charge, advising within a known policy in travel, health or law. |

From there, two pictures of the future:

| Picture | What it looks like |
|---|---|
| Services that look after themselves | Shops, ticketing systems and travel sites run as they do today, and agents inside them watch, investigate and report, and later decide, the way an SRE or an operator does. |
| Purpose-built agents for whole populations | A travel planner, a health adviser or a policy consultant gets one conversation per user, most of them idle at any moment. |

The second picture is out of reach for a machine per conversation. A million open conversations at 1 GiB each need about a petabyte of memory, and 1 GiB is the floor that Anthropic's hosting guide gives for one agent instance. A conversation that is only a record in a store takes memory only while it is loaded. Memory then follows the conversations that are working, not the ones that are open.

We consider that at that scale, what the model reads costs more than what the service holds. Most of what a session sends to the model on each turn, it already sent on the turn before, so the biggest lever is a stable prefix that the provider can cache:

> If I had to choose just one metric, I'd argue that the KV-cache hit rate is the single most important metric for a production-stage AI agent.
>
> — Yichao "Peak" Ji, *Context Engineering for AI Agents: Lessons from Building Manus*, Manus, 2025

**The other side.** Optimism is not evidence. Regulated work needs models that are reliable enough, operations work needs teams that trust agents, and population-scale traffic strains model capacity and cost as much as memory. All three are real, and none of them is solved by giving each conversation a bigger machine. They are solved, if at all, by better models, narrower agents and cheaper turns. Gotato works on the last two.

**What it costs.** Building for population scale taxes small deployments: a team that only ever runs a handful of conversations carries the design without needing it.

## Sources

Every quote was compared with the original. Web pages were downloaded and their text searched for the exact sentence. PDFs were converted to text and searched the same way. The year of *A practical guide to building agents* comes from the PDF's metadata, since its text states none.

- Erik S. and Barry Zhang. *Building effective agents*. Anthropic, 2024-12-19. https://www.anthropic.com/engineering/building-effective-agents
- Lance Martin, Gabe Cemaj and Michael Cohen. *Scaling Managed Agents: Decoupling the brain from the hands*. Anthropic, 2026-04-08. https://www.anthropic.com/engineering/managed-agents
- Rich Sutton. *The Bitter Lesson*. 2019-03-13. http://www.incompleteideas.net/IncIdeas/BitterLesson.html
- OpenAI. *Agents API: architecture*. https://developers.openai.com/api/docs/guides/agents-api/architecture
- OpenAI. *A practical guide to building agents*. 2025. https://cdn.openai.com/business-guides-and-resources/a-practical-guide-to-building-agents.pdf
- Yichao "Peak" Ji. *Context Engineering for AI Agents: Lessons from Building Manus*. Manus, 2025-07-18. https://manus.im/blog/Context-Engineering-for-AI-Agents-Lessons-from-Building-Manus
- Anthropic. *Hosting the Agent SDK*. https://code.claude.com/docs/en/agent-sdk/hosting
