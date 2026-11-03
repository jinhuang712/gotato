# 049. The store contract holds only what every store must do

Decided: 2026-11-02    Status: current

## The problem
`session.Store` mixes layers. `MaxNameBytes` keeps a FileStore name under 255 bytes yet binds every store, so a SQL store refuses 23 CJK characters; the checks are unexported, so adapters copy them. `Store` embeds `Liveness`, so two databases need a hand-written composite. `List` has no paging. `RecordInfo`, a listing view, also carries rebuild state, which `fromLoaded` reads with `Load` then `Log`, leaning on `ErrConflict` when they disagree.

## The choice
The contract keeps what principles 1 and 8 need: one conditional write per batch, the epoch fence before the position, a Load in one read, Liveness by the store's clock. The rest moves out:
- **Exported checks.** `CheckID(id string) error` (non-empty, at most `MaxIDBytes`, UTF-8, no control character, no `/`, `\`, `..` or leading `.`) and `CheckBatch(after uint64, entries []Entry, h Header) error`. `MaxNameBytes` becomes the FileStore's own; any store may refuse more IDs with `ErrInvalidID`.
- **Two seams.** `Records` (`Create`, `Append`, `Load`, `Log`, `Delete`) and `Liveness` (`Beat`, `Instance`); `Store` becomes `struct{ Records Records; Liveness Liveness }`.
- **Optional listing.** `Lister.List(ctx, ListQuery{Limit int; Cursor string}) (ListPage{Infos []RecordInfo; Next string}, error)`, found by `store.Records.(Lister)`; without it, `Runner.Sessions` is `not_supported`.
- **Columns apart from state.** A write passes `Header{Epoch string; Info RecordInfo; State json.RawMessage; LogFrom uint64}`. `Info` is listing columns only; `State` is Gotato's bytes (Run in flight, busy, notices, last Run end, base counts), which no store reads. `Load` returns `Loaded{Entries, Segments, Seq, Header}` in one read: the facts from the last Checkpoint, plus every Entry from `LogFrom`, which the Writer sets while a Run is in flight.

## Not chosen
- `MaxNameBytes` for every store: each pays for a file name it never writes.
- Adapters copy the checks: copies drift, and the guide's already does.
- `List` required: every adapter builds cursors for a call the runtime never makes ([024](024-gotato-builds-no-infrastructure.md)).
- Filters in `ListQuery`: each database indexes differently, and the service queries its own columns.
- Keep the embedding and ship `Compose`: a wrapper hides what it wraps' optional interfaces.
- A typed state the store round-trips: another shape, with nothing the store reads.
- Run, release and notice Entries as facts: a load would cost as much as chatty Runs ([028](028-every-step-is-written-as-facts-and-a-log.md)).
- `Load` then `Log` bounded at the head: still two reads, blind to a Record recreated between them.

## Consequences
Adapters call Gotato's checks, a SQL store keeps every ID Gotato makes, Records and Liveness can live apart, and a rebuild has no conflict path. It breaks every adapter in 0.3.0; a store without `Lister` cannot list; a page may repeat or skip a Record updated while paging. `Records` gains no methods after 0.3.0. [044](044-an-agent-profile-and-a-task-spec-are-registered-inline-or-from-a-store.md)'s `DefinitionStore` follows the same pattern.

1. Export `CheckID` and `CheckBatch`; move the name limit into `FileStore`; drop the guide's copies.
2. Add `Records`, `Header`, `Lister`, `ListQuery`, `ListPage`; make `Store` a struct; slim `RecordInfo`; reshape `Loaded`.
3. Split `infoLocked` into `Info` and `State`; the Writer sets `LogFrom`; `fromLoaded` drops its `Log` call and `ErrConflict` branch.
4. Replace `factKinds` with one table of each stored kind's `Layer`; `layerOf` fails for a missing kind, and a test ranges over the stored kinds.
5. `Runner.Sessions(ctx, session.ListQuery) (session.ListPage, error)`; delete `ListOption`; `GET /v1/sessions` takes `limit` and `cursor`.
6. `NewMemoryStore` and `NewFileStore` take `WithClock`; `storetest` splits into `RunRecords`, `RunLiveness`, `RunLister`, checks a Load racing Appends, and takes an optional `Advance(time.Duration)` for its sleeps.
7. Update the `Store` doc, [specs/record.md](../../specs/record.md) and [wiki/write-a-store.md](../../wiki/write-a-store.md).
