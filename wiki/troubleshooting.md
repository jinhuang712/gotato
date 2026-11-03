# Troubleshooting

> [Wiki](README.md) › Troubleshooting

By the end of this page you know what an error or a surprise means and what to do next. Find the row that matches what you see. This page is a way in: the table of every error code with its HTTP status is in the [RUNBOOK](../RUNBOOK.md), and what each feature does is in [FEATURES](../FEATURES.md).

Every error says two things beside its code: whether the request **ran**, and whether sending it again **may succeed**. Read those first. [Tell an error apart](how-to/tell-an-error-apart.md) shows them as two headers, and in Go they are `gotato.NotRun(err)` and `gotato.Retryable(err)`.

## A call is refused

These come back before a Run starts, so nothing ran.

| You see | Because | Do this |
|---|---|---|
| `busy` (409) | The Session already has a Run, here or on another Instance. | Send it again after the Run ends. A request for a Session this Instance is already running fails at once, unless `Bounds.Queue` is set to wait. |
| `not_writer` (409), with a `Gotato-Writer` header | A message (a Steer, a notice, a Stop, a delete) reached an Instance that is not the writer of the Session's Run. | Send it to the address in the header, or turn on forwarding. No address is named unless each Instance sets `host.Config.Address`. See [Forward messages to the writer](how-to/forward-messages-to-the-writer.md). |
| `draining` (503) | The Instance is shutting down and takes no new Run. | Send the request to another Instance. Your load balancer does it by itself once `/readyz` answers 503. |
| `overloaded` (503) | The Instance holds as many Sessions as `Bounds.MaxActiveSessions` allows, and every one is running. | Retry on another Instance. If every Instance says it, raise the limit or add Instances. |
| `limit_exceeded` (429) | Your `Admitter`, or `Limits{MaxActiveRuns}`, refused the Run, or a queue of waiting messages is full. | A refused Run is retryable: later, or elsewhere. A full queue is not: send fewer messages. See [Limit Runs per tenant](how-to/limit-runs-per-tenant.md). |
| `invalid_argument` (400), naming a key | You set a metadata key that starts with `gotato.`. Those belong to the Runner. | Use keys of your own. To change a Session's Agent, name it on the request (`RunRequest.Agent`). See [Run Sessions on many pods](how-to/run-sessions-on-many-pods.md). |
| `invalid_argument`, "unknown Agent" or "unknown task" | The name is not registered. A row in a store is not a definition until its name is registered. | Register the name. See [Register Agents and jobs](how-to/register-agents-and-jobs.md). |
| `invalid_argument`, a task "widens" its profile | A TaskSpec asks for more than its AgentProfile has: a tool it lacks, a higher Effect cap, a longer timeout. | Narrow the TaskSpec, or widen the profile. |
| `not_found` (404) on a Run | The Session was never created. Over HTTP a Run needs a Session that exists. | Create it first. In Go, a Run with no `SessionID` creates one. See [HTTP in five minutes](tutorials/http-in-five-minutes.md). |
| `already_exists` (409) | The Session ID you chose is taken. | Choose another, or read the Session that has it. For a scheduler this is how a retry stays safe. |

## A Run ends badly

The Run settled, and its status and code say how.

| You see | Because | Do this |
|---|---|---|
| `failed`, `context_overflow` | The provider still refuses the request as too long after a compaction. | Check that the model's window is stated. With an unknown window, compaction runs only after a refusal. See [State what your model is](how-to/state-what-your-model-is.md). |
| `failed`, `limit_exceeded`, after a compaction | The Context is still past the ceiling after the Turn's compaction. | The instruction, the tool definitions and the kept tail are what compaction cannot remove: make one of them smaller, or raise the window. See [the ceiling](../FEATURES.md#f54-the-ceiling). |
| `failed`, `model_failure` | The provider failed, after the retries. | Read whether it is retryable and what the provider said. See [Customize the model bridge](how-to/customize-the-model-bridge.md). |
| `failed`, `model_protocol_failure` | The stream ended without its last event, or a tool call came with no ID. Not retryable. | Treat it as a bug in the provider adapter, and keep the Session's Events for the report. |
| `failed`, `store_failure`, or `host.ErrNotPersisted` | A write of the Run failed. A reply was made, but the Record does not hold it. | Send the request again and look at the store. |
| `failed`, cause `interrupted` | The Instance that ran it stopped before the Run ended, for example in a crash. | Read the Record and check what the last tool call did before you start again. A tool with an external effect should be safe to repeat. See [the RUNBOOK](../RUNBOOK.md#a-run-was-interrupted). |
| `canceled` | The caller went away, or sent a Stop. | Expected. Every Turn it committed is kept. |
| `deadline_exceeded` | The Run passed its deadline, or a model or tool call passed its own. | A call past its own deadline fails and the Run goes on. See [A Run's deadline](../FEATURES.md#f14-a-runs-deadline) and [Limits](../FEATURES.md#f17-limits). |

## The model does something you did not expect

| You see | Because | Do this |
|---|---|---|
| The cache hit rate is 0 | The provider cannot match your prefix. The cache key is not sent where the provider reads it, or the request is too short to be cached. The chat wire sends no cache markers. | Name the cache key field or header, and state the cache retention. See [Customize the model bridge](how-to/customize-the-model-bridge.md) and [State what your model is](how-to/state-what-your-model-is.md). |
| The model never calls a tool you gave it | The Effect cap hides it. A tool with no declared Effect counts as destructive, so a read-only cap hides it, and a call to it fails with "the Session does not allow it". | Declare the tool's Effect, or raise the Agent's cap. See [Write a tool](how-to/write-a-tool.md). |
| The model is told a tool "was not loaded" and to call it again | The tool is in a catalog, and a load happens on the first call. The call did not run. | Expected: it costs one more round. See [Tool search](../FEATURES.md#f66-tool-search). |
| A tool of a ToolSet is missing for a Run | The set failed to resolve and was skipped whole. | The next Run tries again. See [A set that fails](../FEATURES.md#f652-a-set-that-fails). |
| The model reads `tool failed` and no reason | The error you returned is not public, because its text may carry a secret. | Return an error with a `Public()` method, or a failed result with a `SafeError`. See [Write a tool](how-to/write-a-tool.md). |
| The model cannot find an old tool result | After a compaction the result left the Context, and the model reads it back with `read_result`. A Session built by hand has no read-back tool. | Install one with `gotato.WithReadBack`. A hosted Session has it. See [Read back from your own store](how-to/read-back-from-your-own-store.md). |
| A tool the model loaded is gone | A compaction unloads the tools that were not called in the closed Segment or the kept tail. | The model loads it again with `search_tools`. |
| A reply stops at the output limit | The Run settles as it stands unless continuing is allowed, which it is not by default. | Allow a continue, or raise the output limit in the Sampling. See [the continue notice](../FEATURES.md#f442-the-continue-notice). |

## The program refuses to start

| You see | Because | Do this |
|---|---|---|
| `model.New` refuses a window left out | A model's window must be stated: leaving it out is never an accident. | Pass `gotato.WindowTokens(n)`, or write `gotato.WindowUnknown()`. See [State what your model is](how-to/state-what-your-model-is.md). |
| `NewSession` fails: an option "was given twice" | A single-valued option such as `WithTranscript` or `WithInstruction` was given two times. | Give it once. In a service the Runner builds every option from the AgentProfile, and the profile has no `Options` field. |
| `NewSession` refuses a Transcript | A wrapper hides a capability its Transcript declares, such as the Journal. | Forward `Capabilities()`. See [Compact your own Transcript](how-to/compact-your-own-transcript.md). |
| A tool is refused for its ID | A tool of your own may not take a name under a ToolSet's namespace, repeat another tool's ID, or be called `search_tools`. | Rename the tool. |
| `go mod tidy` drops the `require`, and `go doc` finds nothing | No file imports the module yet. | Write the import first, then run `go mod tidy`. See [Install](how-to/install.md). |
| The compiler says `http` is declared twice | Gotato's HTTP adapter has the package name `http`, like `net/http`. | Import it with an alias: `httpapi`. |

## An instance or a deploy misbehaves

These belong to whoever keeps the service running, and the [RUNBOOK](../RUNBOOK.md) has them:

- [`/readyz` answers 503 with `draining`](../RUNBOOK.md#an-instance-is-draining)
- [`/readyz` answers 503 with `lapsed`](../RUNBOOK.md#an-instance-lost-its-liveness-row)
- [A Session stays `busy` after its Instance died](../RUNBOOK.md#an-instance-died)
- [A Run ended `interrupted`](../RUNBOOK.md#a-run-was-interrupted)

## See also

- [Every error code, with its HTTP status](../RUNBOOK.md#a-request-is-refused)
- [How errors are classified](../specs/errors.md)
- [What fails, and what the user sees](../FEATURES.md#when-something-fails)
