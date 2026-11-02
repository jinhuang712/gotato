# Write a Store

By the end of this page you have a `session.Store` of your own that passes the conformance suite, `storetest.Run`. You need the [Embed Gotato in a service](embed-in-a-service.md) page, and a database in mind. The adapter below keeps its rows in a map shaped like two SQL tables, so each line says which statement it stands for; the adapter's snippets were compiled and the suite was run against it; the CI and MongoDB sections further down are illustrations that were not compiled or run here.

**What is here**

| Group | Item | In one line |
|---|---|---|
| **The contract** | [What a Store keeps](#what-a-store-keeps) | Records as append-only logs of Entries, and one Liveness row per Instance. |
| | [The six rules](#the-six-rules) | The rules an adapter is most likely to miss. |
| | [What a Store does not do](#what-a-store-does-not-do) | It reads an Entry's Seq, Kind and Layer, and nothing else. |
| **The adapter** | [Tables and handles](#tables-and-handles) | Two tables, one shared database, many handles. |
| | [Create and Append](#create-and-append) | A conditional write on `(id, seq)`. |
| | [Load and Log](#load-and-log) | Facts from the last Checkpoint on, or every Entry. |
| | [List and Delete](#list-and-delete) | |
| | [Liveness](#liveness) | `Beat` and `Instance`, timed by the Store's clock. |
| | [Checks every call makes](#checks-every-call-makes) | IDs, batches, and a canceled context. |
| **The suite** | [Run storetest](#run-storetest) | One test function. |
| | [No database at hand](#no-database-at-hand) | `storetest.Run` needs no service and no transaction. |
| | [Run it in CI](#run-it-in-ci) | A real backing store as a service container. |
| | [MongoDB](#mongodb) | Unique index, `E11000`, the epoch, and a single-node replica set. |
| | [When a check fails](#when-a-check-fails) | A failure names the rule. |
| | [Things the suite does not know](#things-the-suite-does-not-know) | |

## The contract

### What a Store keeps

A Store keeps two things, and nothing else.

- **Records.** A Record is a Session's append-only log of Entries (see [GLOSSARY.md](../GLOSSARY.md#core)). An Entry is one line: a `Seq` (its position, from 1, with no gap), a `Kind` (the Event kind), a `Layer` (`fact` or `log`), the time, the Instance that wrote it, and its `Data` as JSON. Next to the log the Store keeps a `RecordInfo`, the listing view of the Record (ID, counts, times, metadata), so it can list Sessions without reading their logs.
- **Liveness rows.** One row per running Instance, renewed on a heartbeat and timed by the Store's clock. When a row expires, every Claim of that Instance ends at once. This is how another Instance knows it may take a Session over.

The interface has eight methods for Records (`Create`, `Append`, `Load`, `Log`, `List`, `Delete`) and Liveness (`Beat`, `Instance`). Every write is one conditional write: a batch of Entries goes in all or nothing, and only to positions that are still free. An adapter maps that to an insert on a unique `(session, seq)` key.

### The six rules

These are the rules an adapter is most likely to miss. The suite has a check for each, and its message names the rule.

1. **Load returns facts only, Log returns everything.** `Load` returns the Entries of Layer `fact` from the last Checkpoint on (`Entry.IsCheckpoint`: `session_created` or `compaction_ended`), the `segment_closed` Entries from before it, the head position, and the `RecordInfo`. `Log` returns every Entry, facts and log, in order.
2. **A write continues the log.** The Entries have the Seqs `after+1`, `after+2` and so on, and a Record starts with a Checkpoint at Seq 1. A batch that does not is refused with `invalid_argument`. An `Append` whose `after` is not the Record's last position is refused with `session.ErrConflict`. Of writers racing for one position, exactly one wins.
3. **The Record epoch is required.** `RecordInfo.RecordEpoch` must be set on `Create` and `Append`; a write without it is `invalid_argument`. `Append` checks it before the position. An epoch that is not the stored Record's is `ErrNotFound`, not `ErrConflict`: the Record was deleted and created again, and the writer belongs to the old one.
4. **Delete of an unknown Record is `ErrNotFound`,** and so is every later `Append`, `Load` and `Log` of it.
5. **`Beat` keeps an expired row.** The next `Beat` on it returns `session.ErrExpired`, and the row never comes back. Only a `ttl` of 0 removes a row, and a `Beat` with a `ttl` of 0 on a missing row is not an error.
6. **`Instance` of an unknown ID is not an error.** It is a state that is not alive.

### What a Store does not do

- **It reads an Entry's `Seq`, `Kind` and `Layer`, and `IsCheckpoint` to find where a load starts.** The rest of an Entry, and its `Data`, are kept as they are.
- **It keeps every field.** `RecordInfo`, `Entry` and `InstanceRow` gain fields over time. An adapter that maps them to columns must round-trip every field, also those it was written before; the suite fills each field by reflection and fails an adapter that drops one. Storing the whole value as JSON next to the columns you index keeps a later field for free.
- **`Store` gains no methods after 0.2.0.** A later capability is an optional interface that a caller finds by type assertion, as `Journal.Durable` is, so an adapter written today keeps compiling.
- **What `Load`, `Log` and `List` return is the caller's to read and the store's to keep.** A caller must not modify it, and an adapter with a cache may return it without a copy. `MemoryStore` returns copies.

## The adapter

The pieces below are one file, `mystore.go`. It stands for a database with two tables: `sessions` (one row per Record: the ID and its `RecordInfo` as JSON) and `entries` (one row per Entry: `(id, seq)` unique, the Entry as JSON). A real adapter replaces the map and the mutex with a connection pool and transactions; the comments name the statement each step becomes.

### Tables and handles

Every handle must see what the others wrote, as two pods sharing one database do. So the state lives in a `db` that handles share, and a `Store` is one handle.

```go
// Package mystore is a Store over a map of JSON rows, shaped like two SQL tables.
package mystore

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// db stands for the database. Every handle shares one, as two pods share a server.
type db struct {
	mu        sync.Mutex
	records   map[string]*record // table sessions: id, info (JSON)
	instances map[string]instance
}

type record struct {
	info    []byte   // the RecordInfo as JSON
	entries [][]byte // table entries: (id, seq) unique, the Entry as JSON; seq = index+1
}

type instance struct {
	row     session.InstanceRow
	expires time.Time
}

// Store is one handle on a db.
type Store struct{ db *db }

var _ session.Store = (*Store)(nil)

// NewBacking returns a function that opens handles on one new db.
func NewBacking() func() *Store {
	shared := &db{records: map[string]*record{}, instances: map[string]instance{}}
	return func() *Store { return &Store{db: shared} }
}
```

### Create and Append

`Create` inserts a Record whose first Entry is a Checkpoint at Seq 1. `Append` adds Entries after `after`. In SQL, `Append` is one transaction: check the epoch, then insert the rows. The unique `(id, seq)` key is what makes a stale writer lose, so map its violation to `ErrConflict`.

```go
func (s *Store) Create(ctx context.Context, id string, entries []session.Entry, info session.RecordInfo) error {
	if err := precheck(ctx, id); err != nil {
		return err
	}
	if err := session.CheckBatch(0, entries, info); err != nil {
		return err
	}
	rows, infoJSON, err := encode(entries, info)
	if err != nil {
		return err
	}
	s.db.mu.Lock()
	defer s.db.mu.Unlock()
	if _, ok := s.db.records[id]; ok { // INSERT hit the unique key on id
		return session.ErrExists
	}
	s.db.records[id] = &record{info: infoJSON, entries: rows}
	return nil
}

func (s *Store) Append(ctx context.Context, id string, after uint64, entries []session.Entry, info session.RecordInfo) error {
	if err := precheck(ctx, id); err != nil {
		return err
	}
	if err := session.CheckBatch(after, entries, info); err != nil {
		return err
	}
	rows, infoJSON, err := encode(entries, info)
	if err != nil {
		return err
	}
	s.db.mu.Lock() // one transaction
	defer s.db.mu.Unlock()
	rec, ok := s.db.records[id]
	if !ok {
		return session.ErrNotFound
	}
	var stored session.RecordInfo
	if err := json.Unmarshal(rec.info, &stored); err != nil {
		return err
	}
	if stored.RecordEpoch != info.RecordEpoch { // the epoch is checked before the position
		return session.ErrNotFound
	}
	if uint64(len(rec.entries)) != after { // the unique (id, seq) key would refuse the insert
		return session.ErrConflict
	}
	rec.entries = append(rec.entries, rows...)
	rec.info = infoJSON
	return nil
}
```

- **Order of checks in `Append`.** The epoch is compared before the position, so a writer of a deleted Record gets `ErrNotFound` even when its position would also be wrong.
- **`Create` of a taken ID is `ErrExists`.** An error other than `ErrExists` leaves the outcome unknown (a write may have been made but not confirmed), and the caller tolerates `ErrExists` on a retry.
- **A write is all or nothing.** In SQL, the batch and the `RecordInfo` update go in one transaction.

### Load and Log

`Load` finds the last Checkpoint, returns the facts from there, and returns the `segment_closed` Entries before it apart. `Log` is `WHERE seq > after ORDER BY seq LIMIT limit`.

```go
func (s *Store) Load(ctx context.Context, id string) (session.Loaded, error) {
	if err := precheck(ctx, id); err != nil {
		return session.Loaded{}, err
	}
	s.db.mu.Lock()
	defer s.db.mu.Unlock()
	rec, ok := s.db.records[id]
	if !ok {
		return session.Loaded{}, session.ErrNotFound
	}
	all, err := decode(rec.entries)
	if err != nil {
		return session.Loaded{}, err
	}
	start := 0 // the last Checkpoint
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].IsCheckpoint() {
			start = i
			break
		}
	}
	var out session.Loaded
	for _, e := range all[:start] {
		if e.Kind == gotato.EventKindSegmentClosed {
			out.Segments = append(out.Segments, e)
		}
	}
	for _, e := range all[start:] {
		if e.Layer == session.LayerFact {
			out.Facts = append(out.Facts, e)
		}
	}
	out.Head = uint64(len(all))
	if err := json.Unmarshal(rec.info, &out.Info); err != nil {
		return session.Loaded{}, err
	}
	return out, nil
}

func (s *Store) Log(ctx context.Context, id string, after uint64, limit int) ([]session.Entry, error) {
	if err := precheck(ctx, id); err != nil {
		return nil, err
	}
	s.db.mu.Lock()
	defer s.db.mu.Unlock()
	rec, ok := s.db.records[id]
	if !ok {
		return nil, session.ErrNotFound
	}
	if after >= uint64(len(rec.entries)) {
		return nil, nil
	}
	rows := rec.entries[after:] // WHERE seq > after ORDER BY seq LIMIT limit
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return decode(rows)
}
```

- **A `limit` of 0 or less means every Entry.** A caller that needs one Entry or one page passes a limit so that the Record is not read to its end.
- **A large Entry stays whole** in `Log`, and stays out of `Load` when it is a log Entry. Nothing in the adapter truncates `Data`.

### List and Delete

`List` returns every Record's `RecordInfo`, the most recently updated first (ties by ID). `Delete` removes the Record and its Entries, and an unknown ID is `ErrNotFound`.

```go
func (s *Store) List(ctx context.Context) ([]session.RecordInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.db.mu.Lock()
	defer s.db.mu.Unlock()
	out := make([]session.RecordInfo, 0, len(s.db.records))
	for _, rec := range s.db.records {
		var info session.RecordInfo
		if err := json.Unmarshal(rec.info, &info); err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { // ORDER BY updated_at DESC, id
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	if err := precheck(ctx, id); err != nil {
		return err
	}
	s.db.mu.Lock()
	defer s.db.mu.Unlock()
	if _, ok := s.db.records[id]; !ok {
		return session.ErrNotFound
	}
	delete(s.db.records, id)
	return nil
}
```

### Liveness

`Beat` renews an Instance's row until `ttl` from now. Time it by the Store's clock: in SQL, the database's `NOW()`, not the clock of the pod that calls, so a pod with a skewed clock cannot keep a dead row alive. The comment marks it, and the suite relies on it.

```go
func (s *Store) Beat(ctx context.Context, row session.InstanceRow, ttl time.Duration) error {
	if err := precheck(ctx, row.ID); err != nil {
		return err
	}
	s.db.mu.Lock()
	defer s.db.mu.Unlock()
	if ttl <= 0 { // an Instance ends its row; a missing row is not an error
		delete(s.db.instances, row.ID)
		return nil
	}
	now := time.Now() // with SQL, the database's NOW(), not this process's clock
	if cur, ok := s.db.instances[row.ID]; ok && !now.Before(cur.expires) {
		return session.ErrExpired // the expired row stays
	}
	s.db.instances[row.ID] = instance{row: row, expires: now.Add(ttl)}
	return nil
}

func (s *Store) Instance(ctx context.Context, id string) (session.InstanceState, error) {
	if err := ctx.Err(); err != nil {
		return session.InstanceState{}, err
	}
	s.db.mu.Lock()
	defer s.db.mu.Unlock()
	cur, ok := s.db.instances[id]
	if !ok {
		return session.InstanceState{ID: id}, nil // unknown is not an error
	}
	return session.InstanceState{ID: id, Alive: time.Now().Before(cur.expires), Draining: cur.row.Draining, Address: cur.row.Address}, nil
}
```

### Checks every call makes

Every method that takes an ID checks it and the context first. The rules themselves are Gotato's: the adapter calls them.

```go
func precheck(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return session.CheckID(id)
}

func encode(entries []session.Entry, info session.RecordInfo) (rows [][]byte, infoJSON []byte, err error) {
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			return nil, nil, err
		}
		rows = append(rows, b)
	}
	infoJSON, err = json.Marshal(info)
	return rows, infoJSON, err
}

func decode(rows [][]byte) ([]session.Entry, error) {
	out := make([]session.Entry, len(rows))
	for i, b := range rows {
		if err := json.Unmarshal(b, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}
```

`session.CheckBatch` is rules 2 and 3, and `session.CheckID` is the ID rule below; an adapter calls them and does not copy them.

- **IDs.** Every Store refuses the same IDs, with `session.ErrInvalidID` (code `invalid_argument`): an empty one, one over `session.MaxIDBytes` (100), one that is not valid UTF-8 or has a control character, one with a path separator or `..`, and one that starts with a dot. `session.CheckID` is that rule, so call it and do not copy it. A Store may refuse more IDs, with `ErrInvalidID` too: the `FileStore` refuses one whose file name would be over 200 bytes, a limit that belongs to its files, and an adapter with no files keeps those IDs. The suite checks `CheckID`'s rule.
- **IDs differ by their characters alone.** `Case` and `case` are two Records whatever the collation of your database; so are the two spellings of an accented letter. Use a binary collation on the ID column.
- **Every call honors `ctx`.** A canceled context stops the call and writes nothing.

## The suite

### Run storetest

`storetest.Run` runs every check as a subtest, each on a new backing store. You give it a function that creates one empty backing store and returns how to open handles on it, and how to clean it up.

```go
package mystore_test

import (
	"testing"

	"example.com/yourapp/mystore"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/session/storetest"
)

func TestStore(t *testing.T) {
	storetest.Run(t, func() (storetest.Open, func(), error) {
		open := mystore.NewBacking()
		return func() session.Store { return open() }, nil, nil
	})
}
```

- **`Open` returns a new handle on the same backing store each time it is called.** Two handles are two Instances sharing one database; the suite uses them to prove that one sees what the other wrote and that racing writers lose.
- **For a real database,** create a schema (or a database) in the first function and drop it in the cleanup function. The suite makes one backing store per check, 30 in one run.
- **`storetest.Check(ctx, backing)`** does the same without `testing` and returns the problems, for a tool that checks a store at startup.

`go test -race -v` lists what the suite checks:

```text
--- PASS: TestStore/Create_then_Load_returns_the_facts_and_Log_returns_every_Entry
--- PASS: TestStore/Create_of_a_taken_ID_is_ErrExists
--- PASS: TestStore/Append_adds_Entries_after_the_head,_seen_by_Log_and_Load
--- PASS: TestStore/a_stale_writer_is_refused_with_ErrConflict_and_writes_nothing
--- PASS: TestStore/of_writers_racing_for_one_position,_exactly_one_wins
--- PASS: TestStore/Beat_creates_a_Liveness_row_that_Instance_returns,_and_ttl_0_ends_it
--- PASS: TestStore/an_expired_Liveness_row_is_not_alive,_and_Beat_on_it_is_ErrExpired
--- PASS: TestStore/every_field_of_RecordInfo,_Entry_and_InstanceRow_is_kept
...
```

### No database at hand

The suite needs no external service and no transaction. `session.NewMemoryStore`, `session.NewFileStore` and the `mystore` above all pass it ([`TestMemoryStoreConformance` and `TestFileStoreConformance`](../session/file_test.go) run it on the first two), so you can write the adapter's tests against a map or a directory first and point the same function at a real database later. To check a store outside `go test`, for example at startup, call `storetest.Check`.

What the contract asks of a backing store is one conditional write: a single-document atomic write, plus a unique `(id, seq)` key, is enough. A multi-document transaction is a cost of one design (entries and head in separate documents or rows), not a Gotato rule. If your database cannot take a transaction, choose a layout that does not need one.

### Run it in CI

A mock passes for the wrong reasons, so run the suite against the database you ship on. This GitHub Actions job starts one as a service container and passes its address in; the test reads it and skips when it is not set:

```yaml
jobs:
  store:
    runs-on: ubuntu-latest
    services:
      db:
        image: postgres:17
        env:
          POSTGRES_PASSWORD: test
        ports: ["5432:5432"]
        options: >-
          --health-cmd "pg_isready -U postgres"
          --health-interval 5s --health-retries 10
    steps:
      - uses: actions/checkout@v6.1.0
      - uses: actions/setup-go@v6.5.0
        with:
          go-version-file: go.mod
      - run: go test -race -count=1 ./mystore/...
        env:
          STORE_TEST_DSN: postgres://postgres:test@localhost:5432/postgres?sslmode=disable
```

```go
func TestStore(t *testing.T) {
	dsn := os.Getenv("STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("STORE_TEST_DSN is not set")
	}
	storetest.Run(t, func() (storetest.Open, func(), error) {
		db, drop, err := newSchema(dsn) // your helper: a new schema or database per call
		if err != nil {
			return nil, nil, err
		}
		return func() session.Store { return mystore.New(db) }, drop, nil
	})
}
```

Swap the image for yours. The function runs once per check, so make creating and dropping a schema cheap. For a database that needs a replica set, see the next section.

### MongoDB

Mongo adapters meet the same rules as any other, in Mongo's terms.

- **Unique index on `(id, seq)`** in the entries collection. A write of a position that is taken fails with a duplicate-key error, code 11000 (`E11000`). Map that, and only that, to `session.ErrConflict` (in the Go driver, `mongo.IsDuplicateKeyError`). On `Create`, a duplicate `_id` in the head document is `session.ErrExists`.
- **The epoch goes in the head document's filter.** Update the head with a filter on the ID and `RecordInfo.RecordEpoch`, and on the head position when you use it as the guard. When it matches nothing, read the head document: a missing one, or one with another epoch, is `session.ErrNotFound`; any other is `session.ErrConflict`. The epoch is judged before the position (rule 3); the suite has a check for each order (`Append by a writer of another epoch is ErrNotFound before its position is checked`).
- **All or nothing needs a transaction, or a head-document CAS.** An `InsertMany` of several entries is not atomic by itself. A multi-document transaction (entries and head in one) needs a replica set, which a standalone `mongod` is not. Without a transaction, make the head document the guard: move its head position by compare-and-set first, then write the entries, and design what a reader sees if the process dies in between. The suite does not kill a process between two writes, so it cannot tell you that part is right.

**A single-node replica set without Docker.** *Reported from real use, not run in this repository.* A Mongo Store passed all 30 `storetest.Run` subtests, also with `-race -count=5`, on a single-node replica set started in user space with MongoDB 8.0: no Docker and no package manager.

1. Download the official MongoDB 8.0 Community Server tarball for your platform from mongodb.com and unpack it in a directory you own.
2. Start `mongod` from it with `--replSet <name>`, a `--dbpath` you own and a `--port` that is free (add `--bind_ip 127.0.0.1`).
3. Connect once with `mongosh` and run `rs.initiate()` (the `replSetInitiate` command). It is needed once; the set elects itself primary in a few seconds.
4. Point the test at the connection string of that node. Check the exact flags and shell against the MongoDB documentation for your version.

Not covered by that run: a set of several nodes, a transaction retried after a failover, and several pods writing one Session. Test those against your own deployment before you rely on them.

### When a check fails

A failure message names the rule that broke. If the adapter above returned `ErrNotFound` where it returns `ErrConflict` (a unique-key violation mapped to the wrong error), the suite says:

```text
--- FAIL: TestStore/a_stale_writer_is_refused_with_ErrConflict_and_writes_nothing
    storetest.go:76: an Append for a position another writer already wrote returned not_found: session: not found, want ErrConflict: a position is written once
--- FAIL: TestStore/of_writers_racing_for_one_position,_exactly_one_wins
    storetest.go:76: writer 0 that lost the race for position 4 returned not_found: session: not found, want ErrConflict
```

### Things the suite does not know

- **Your database's failure modes.** A lost connection, a deadlock the database retries, a serialization failure: map the ones that mean "someone else wrote first" to `ErrConflict`, and let the rest be plain errors. The Runner reports them as `store_failure`.
- **Your transaction isolation.** The race check runs writers against your handles at once, but a database at a weak isolation level can pass it on a quiet machine. Rely on the unique key, not on a read before the write.
- **Your scale.** The suite checks behavior, not speed. `Load` reads from the last Checkpoint on, so index `(id, seq)` and make sure your database finds the last Checkpoint without reading the whole log.

## Next

- Run it in a service: [Embed Gotato in a service](embed-in-a-service.md).
- The reference stores are `session.NewMemoryStore` ([memory.go](../session/memory.go)) and `session.NewFileStore` ([file.go](../session/file.go)); read them next to yours.
- Test a Tool or a Model the same way: [Write a Tool, a ToolSet and a Model](write-a-tool-and-model.md).
