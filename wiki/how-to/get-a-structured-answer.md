# Get a structured answer

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Get a structured answer

By the end of this page an Agent hands your service a value of a shape you choose. You need the [first page](../tutorials/getting-started.md).

## Make the shape a tool

Gotato has no structured-output mode. Give the Agent a tool whose arguments are the shape you want, such as `submit_ticket`, and say in its instruction to call it with the result. The arguments are checked against the tool's schema before it runs, and arguments that do not fit go back to the model as a failed result, so it corrects them. The tool receives the value decoded and keeps it where your service keeps the Run's result. The model usually answers once more after the call, so an instruction such as "after submitting, reply only 'Done'" keeps that answer short.

## See also

- [Tools, as the user sees them](../../FEATURES.md#f6-tools)
