# The Store

A Session has to outlive its Instance: the process can stop between two Steps, and any other Instance must be able to pick the Session up. The Store is where it lives. Gotato asks very little of it, so that a plain SQL or document database, or a directory of files, can keep the contract. This spec is the `store` layer of the [map](ARCHITECTURE.md). It shows what the Store promises, how a Writer binds a Record to it, what keeps two writers or two lives of one ID apart, and how an adapter proves its work. The Record itself, the log that is saved, is in `record.md`, and the package `record` does not know how it is saved: `store` imports it, never the reverse.

## TL;DR
- A Store is two seams. `Records` makes one promise: a batch of Entries lands whole, at the position after the head the writer read, or it is refused. `Liveness` keeps one row per Instance, timed by the store's clock.
- Listing Records is an optional third seam, `Lister`. Stored definitions are a separate contract, `Definitions`, that is not part of the Store.
- A Writer binds one Record to a `Records` as the Instance that holds its Claim. It writes every Step as one batch, and stops at its first failed write.
- A memory store and a file store ship. `storetest` is the suite that an adapter of your own runs to prove the contract.

## Two seams

A `store.Store` is a struct of two interfaces, so a service may keep them in two databases. Gotato calls what each declares and finds an optional capability by type assertion on the field.

```text
 store.Store{ Records, Liveness }

 Records     Create(id, entries, header)            a new Record; its first Entry is a Checkpoint at Seq 1
             Append(id, after, entries, header)     a batch at after+1 onward, or refused
             Load(id)                               the Record in one read
             Log(id, after, limit)                  Entries in order, a page at a time
             Delete(id)
   optional  Lister.List(query)                     the Records, most recently updated first, paged
 Liveness    Beat(row, ttl)                         renew this Instance's row, by the store's clock
             Instance(id)                           alive or not, draining, its address

 Definitions SaveDefinition(kind, name, bytes), LoadDefinition(kind, name)     a contract of its own
```

`Records` gains no methods after 0.3.0: a later capability is an optional interface, as `Lister` is, so an adapter written today keeps working. A store calls `CheckID` on every ID and `CheckBatch` on every write, so all stores refuse the same IDs and batches the same way. An ID is refused when it is empty or longer than 100 bytes, is not valid UTF-8 or holds a control character, holds a path separator or `..`, or starts with a dot. It may refuse more IDs (the file store has a rule about file names), and answers `ErrInvalidID`. What `Load`, `Log` and `List` return is the caller's to read and the store's to keep, so a caller must not change it.

## One write

Every write is one conditional write. A `Create` or an `Append` writes its batch all or nothing, and only to positions that are still free, so an adapter maps it to an insert on a unique (session, seq) key.

```text
 Writer A (holds the Claim)               Store               Writer B (stale)
   │ Append(after=41, Entries 42-44) ───▶  │
   │                                       │ last position is 41: write all three
   │ ◀─────────────────── ok ───────────── │
   │                                       │ ◀── Append(after=41, Entry 42) ──────│
   │                                       │ last position is 44, not 41
   │                                       │ ──────── ErrConflict ───────────────▶│
```

A Step is one batch, so a crash loses at most the Step in flight. A taken ID is `ErrExists` on `Create`. A conflict means another writer took the Record. Any other failure of a write leaves the outcome unknown, so the Writer stops and the Record is opened again from the Store to see whether the write landed.

## Two fences on every write

An `Append` is checked in this order, and the epoch comes before the position.

```text
 Append(id, after, Entries, Header)
   0  Header names a Record epoch?         no ─▶ invalid_argument
   1  the Record exists?                   no ─▶ ErrNotFound
   2  the Record epoch is the stored one?  no ─▶ ErrNotFound  (deleted and created again)
   3  `after` is the last position?        no ─▶ ErrConflict  (another writer wrote first)
   4  write the batch, replace the Header
```

Fence 3 is the position. A Claim is an Entry written at the position after the head its Instance read, so a writer that lost its Claim always writes to a taken position. Fence 2 is the Record epoch, an identity chosen when the Record is created. Without it, a writer of a deleted Record could land at a position that is free again in the new one. The Epoch inside a Claim is another number, and `host.md` covers it. The Record is also the trace of a Session, and this is a promise about Gotato's code and not about the Store: only the Session's Writer appends, and a Tool or the model has no way to write to the Record, but whoever operates the Store can still change or delete a Record, and Entries are not signed.

## Delete and create again

A number means the same Event only within one Record epoch, so an ID that is reused starts a new epoch.

```text
 Record "s1"   epoch E1: 1 2 3 ... 40 41 │ deleted │ epoch E2: 1 2 3 ...
 Writer W1 (E1)                    Append(E1) ────────────▶ ErrNotFound
 Client C (saw E1, up to 40)                resume(E1, 40) ─▶ cannot_resume, with E2 and the head
```

The old Writer is refused even when the new log is as long as its own, because the epoch is compared first. A client that sends the epoch it saw is refused instead of being handed another conversation's 41st Event. A delete claims the Session first, so it never removes a Record from under a Run on another Instance.

## A load in one read

`Load` returns the Record in one read, so its Entries and its Header always belong together and a rebuild never has to settle a disagreement between two reads.

```text
 Record   1 ... [seg 3][seg 4][CP 5: lists 3, 4 + tail] 6 ... 71 (head)
                   │      │            └── Facts from here on ──┘
                   └──────┴── segment_closed Entries before the Checkpoint

 Records.Load  ─▶  Entries   the Facts from the last Checkpoint on, and every Entry from Header.LogFrom
                   Segments  the segment_closed Entries before it
                   Seq       the position of the last Entry
                   Header    the Header of the last write
```

A Checkpoint holds the state of the Facts at one point, so a load starts at the last one and skips everything before it, except the Segments that Checkpoint lists by number. This is why a Session that has lived for weeks opens as fast as a new one. `Log` is the other read: every Entry, facts and log, after a position.

## The Header

A write passes a `Header` with its Entries, and the Store keeps the last one with the Record and returns it from `Load`.

```text
 Header   Epoch    the Record epoch: the fence, read by every store
          Info     listing columns: ID, parent, times, Messages, Runs, Usage, metadata   (read by a Lister)
          State    Gotato's bytes: the Run in flight, busy, notices, the last Run end, base counts   (read by no store)
          LogFrom  from where Load also returns the log Entries; 0 for none
```

`Info` holds what a service lists and a database may index, and nothing a rebuild needs. `State` is Gotato's own JSON, kept as an Entry's `Data` is. So a store keeps the Header as it is, and a later field of `Info` or `State` needs no change to an adapter that stores the whole value. While a Run is in flight the Record sets `LogFrom` to the last Checkpoint's position, so a load also returns the log Entries since then, which rebuild a reply that was streaming as a Cut-off reply if the Run never ended.

## Listing

Listing is optional because the runtime never lists. `List(ctx, ListQuery{Limit, Cursor})` returns the Records, most recently updated first, 100 a page by default, with an opaque cursor. A caller finds it as `records.(store.Lister)`; without it `Runner.Sessions` is `not_supported`. A page may repeat or skip a Record that is updated while a caller pages. There are no filters: a service queries its own columns.

## Liveness rows

An Instance proves it is alive with one row, however many Sessions it holds. `Beat` renews the row to now plus a TTL by the store's clock and creates it the first time. An expired row stays, so the next `Beat` fails with `ErrExpired` and the row never comes back. A TTL of 0 removes the row, which is how an Instance leaves. An unknown Instance is not an error, it is simply not alive, so every Claim that names it is dead. The row also carries a draining mark and an optional address, which Gotato only carries: it is what a `not_writer` answer names. The memory and file stores time rows by the clock of the machine, so a file store shared by several machines needs synced clocks. A database adapter uses its own clock.

## The Writer

A Writer writes one Record's Entries to a Store as the Instance that holds its Claim. Every change to the Record queues an Entry, and `Flush` writes what is queued in one write. It is also a `gotato.Transcript` and a `gotato.Journal`, so a live Session that is given it writes the Events of each Step as they happen, and gets them back numbered.

```text
 CreateWriter   a new Record: write session_created with the first Claim            Create
 OpenWriter     Load; ask the host's holder check; claim with the next Epoch;      Load, then Append
                end a Run that never ended as `interrupted`
 Resume         claim again on a warm copy, with the same Epoch                     Append
 Stale          does the Store hold Entries after my head?                          Log(after=head, limit=1)
 Flush, Release write what is queued; Release adds session_released                 Append
```

Four rules make it safe.
- **It stops at its first failed write.** A conflict says another writer took the Record, and any other failure leaves the outcome unknown. Every later `Flush` returns the same error.
- **A guard runs before every write.** The host gives it a check of its own Liveness row, so an Instance whose row may have expired does not write.
- **A write is not canceled with its caller.** It is bounded by `WriteTimeout` (10 seconds) and a canceled Run still writes its last Steps, so a slow Store cannot hold a Run for good.
- **Publishing follows storing.** An Event is handed to the publisher only after the write that holds it succeeded.

`Open` loads a Record from its facts for reading (everything a Run needs), and `View` loads the whole log, with Run records, usage and Events. `StoredEvents` reads Events by number for a subscriber that fell behind, with a spilled body left out.

## The two stores that ship

The memory store keeps Records and rows in memory and returns copies. It is for library use and tests. The file store keeps each Record under a directory of its own epoch, one file per write.

```text
 <dir>/<id>/<epoch>/00000000000000000001.json     {"entries": [...], "header": {...}}
 <dir>/<id>/<epoch>/00000000000000000003.json     named by the write's first position
 <dir>/.instances/<instance>.json                 one Liveness row per Instance
```

A write goes to a temporary file, is synced, and is then linked to its name. Linking fails when the name exists, so two writers can never both write a position, even from two processes on one directory, and a crash never leaves a torn write. The epoch is in the path, so a writer of a deleted and created Record finds no directory to write into. `Create` builds the directory under a temporary name and renames it, which fails when the ID is taken, and a delete renames before it removes. An ID of lowercase ASCII is its own file name, and any other character is spelled out, so two IDs never share a directory on a file system that ignores case; a name over 200 bytes is refused. Opening a store sweeps leftovers of a crash: a `.creating-*` or `.deleted-*` directory and a `.tmp-*` file, once a day old, since several processes may share the directory and a live write finishes in seconds. A Record is never named like a leftover, because an ID cannot start with a dot. A cold load reads the `segment_closed` Entries of every earlier file, since there is no index of Segments, so it is the store to try things with and not the one for a busy service.

## Definitions

`store.Definitions` is a store of documents by kind and name, for the profiles a service keeps in its own database. It keeps the bytes as written and copies them in and out. `LoadDefinition` answers `record.ErrNotFound` when there is none, and every implementation refuses a kind or name that `CheckDefinitionKey` refuses (`ErrInvalidID`). It is not a field of `Store`: a service passes it to a registration (`profile.md`). A memory store and a file store (`<dir>/<kind>/<name>.json`, replaced by one rename) ship with it.

## Proving an adapter

`store/storetest` has one suite for each seam: `RunRecords`, `RunLiveness`, `RunLister` and `RunDefinitions`. An adapter runs the ones it implements from its own tests. The suites check the rules above by their failure messages: an append is all or nothing, a position is written once, the epoch is checked before the position, a load racing appends is one read, the Header comes back as it was written, an expired row never returns, and concurrent saves of one key leave one whole document. A liveness test moves the store's clock with `WithAdvance` and never sleeps. The suite is a package of its own so that importing `testkit` does not link the `testing` package into a program.

## What this buys you
- A SQL or document database keeps the contract with one conditional insert for Records and one renewable row for each Instance.
- A stale Writer, or the Writer of a deleted Record, cannot corrupt a Record: it is refused before it writes.
- An adapter that passes the suites keeps what the multi-instance design relies on, with no test of its own to write.

## See also
[ARCHITECTURE.md](ARCHITECTURE.md), the guidelines [the store is the only truth](../DESIGNS.md#1-the-store-is-the-only-truth) and [no instance is special](../DESIGNS.md#8-no-instance-is-special), [FEATURES.md](../FEATURES.md) (F8: Store), decisions [002](../plan/decisions/002-the-store-is-the-truth-and-memory-is-a-cache.md), [013](../plan/decisions/013-a-conformance-suite-ships-with-the-store-contract.md), [017](../plan/decisions/017-the-record-is-an-append-only-log.md), [019](../plan/decisions/019-one-writer-per-session-claimed-in-the-store.md), [027](../plan/decisions/027-instances-coordinate-through-the-stores-conditional-writes.md), [049](../plan/decisions/049-the-store-contract-holds-only-what-every-store-must-do.md) and [050](../plan/decisions/050-each-package-is-one-layer-named-for-its-job.md).
