# Control who may touch a Session

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Control who may touch a Session

By the end of this page only the callers that own a Session can reach it. You need [Run Sessions on many pods](run-sessions-on-many-pods.md).

## Who may touch a Session

Who the caller is, and which Sessions it may touch, are your service's. Gotato has no identity type and no "not allowed" error; it passes each request's `context.Context`, with its values, to every store and extension call for that request. So the check goes in a wrapper of your store:

- **Answer not found.** A wrapper returns `record.ErrNotFound` for a Session the caller does not own; HTTP answers 404, which does not reveal that the Session exists.
- **Guard the store, and every call is guarded.** A Run, a Stop, a Steer, a Follow up, an Interrupt and a notice all read through the store, so a wrapper that checks load, log, append, delete and list covers them all. A wrapper that embeds a `store.Records` hides its `Lister`; implement `List` on the wrapper too, or listing answers `not_supported`.
- **Work Gotato starts on its own has no caller.** An idle compaction and a liveness renewal carry no request's values, and a following Run keeps the values of the request that started the Run before it, without its cancellation. Decide what an empty caller means in your rule.

## See also

- [Why Gotato has no access control of its own](../../plan/decisions/025-callers-credentials-and-quotas-belong-to-the-service.md)
- [Know who asked](know-who-asked.md)
