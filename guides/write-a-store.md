# Write a Store

By the end of this page you have a `session.Store` of your own that passes the conformance suite, `storetest.RunRecords` and `storetest.RunLiveness`. You need the [Embed Gotato in a service](embed-in-a-service.md) page, and a database in mind. The adapter below keeps its rows in a map shaped like two SQL tables, so each line says which statement it stands for; the adapter's snippets were compiled and the suite was run against it; the CI and MongoDB sections further down are illustrations that were not compiled or run here.

**What is here**

| Group | Item | In one line |
|---|---|---|
| **The contract** | [What a Store is](#what-a-store-is) | Two seams, `Records` and `Liveness`, and an optional `Lister`. |
| | [The rules](#the-rules) | The rules an adapter is most likely to miss. |
| | [What a Store does not do](#what-a-store-does-not-do) | It reads an Entry's Seq, Kind and Layer, and the Header's Epoch, and nothing else. |
| **The adapter** | [Tables and handles](#tables-and-handles) | Two tables, one shared database, many handles. |
| | [Create and Append](#create-and-append) | A conditional write on `(id, seq)`. |
| | [Load and Log](#load-and-log) | Facts from the last Checkpoint, and the log from `LogFrom`, in one read. |
| | [List and Delete](#list-and-delete) | An optional `Lister`, and `Delete`. |
| | [Liveness](#liveness) | `Beat` and `Instance`, timed by the Store's clock. |
| | [Checks every call makes](#checks-every-call-makes) | `CheckID`, `CheckBatch`, and a canceled context. |
| **The suite** | [Run storetest](#run-storetest) | One test function for each seam. |
| | [No database at hand](#no-database-at-hand) | The suite needs no service and no transaction. |
| | [Run it in CI](#run-it-in-ci) | A real backing store as a service container. |
| | [MongoDB](#mongodb) | Unique index, `E11000`, the epoch, and a single-node replica set. |
| | [When a check fails](#when-a-check-fails) | A failure names the rule. |
| | [Things the suite does not know](#things-the-suite-does-not-know) | |

## The contract

### What a Store is

A `session.Store` is a struct of two seams, `Store{Records: …, Liveness: …}`, so the two may live in different databases. Each is an interface, and a caller finds anything optional by type assertion on the field.

- **`Records`** keeps Records: a Record is a Session's append-only log of Entries (see [GLOSSARY.md](../GLOSSARY.md#core)). An Entry is one line: a `Seq` (its position, from 1, with no gap), a `Kind` (the Event kind), a `Layer` (`fact` or `log`), the time, the Instance that wrote it, and its `Data` as JSON. Next to the log the store keeps the `Header` of the last write: the Record's `Epoch`, an `Info` of listing columns (ID, counts, times, metadata), a `State` that is Gotato's own bytes, and `LogFrom`. It has five methods: `Create`, `Append`, `Load`, `Log` and `Delete`. Every write is one conditional write: a batch of Entries goes in all or nothing, and only to positions that are still free. An adapter maps that to an insert on a unique `(session, seq)` key.
- **`Liveness`** keeps one row per running Instance, renewed on a heartbeat and timed by the store's clock. When a row expires, every Claim of that Instance ends at once. This is how another Instance knows it may take a Session over. It has two methods: `Beat` and `Instance`.
- **`Lister`** is optional, on `Records`: `List(ctx, ListQuery{Limit, Cursor}) (ListPage{Infos, Next}, error)`. The runtime never lists, so an adapter that does not implement it still runs every Session; only `Runner.Sessions` (and `GET /v1/sessions`) then answers `not_supported`. Write it when your service lists Sessions.

### The rules

These are the rules an adapter is most likely to miss. The suite has a check for each, and its message names the rule.

For `Records`:

1. **Load reads the Record once.** It returns the Entries of Layer `fact` from the last Checkpoint on (`Entry.IsCheckpoint`: `session_created` or `compaction_ended`), plus every Entry, log Entries too, from `Header.LogFrom` when that is not 0; the `segment_closed` Entries from before the Checkpoint, apart; the position of the last Entry (`Loaded.Seq`); and the Header of the last write. All of it comes from one read, so a `Load` that races an `Append` returns the Entries and the Header of the same write. `Log` returns every Entry, facts and log, in order.
2. **A write continues the log.** The Entries have the Seqs `after+1`, `after+2` and so on, and a Record starts with a Checkpoint at Seq 1. A batch that does not is refused with `invalid_argument`. An `Append` whose `after` is not the Record's last position is refused with `session.ErrConflict`. Of writers racing for one position, exactly one wins.
3. **The Record epoch is required.** `Header.Epoch` must be set on `Create` and `Append`; a write without it is `invalid_argument`. `Append` checks it before the position. An epoch that is not the stored Record's is `ErrNotFound`, not `ErrConflict`: the Record was deleted and created again, and the writer belongs to the old one.
4. **Delete of an unknown Record is `ErrNotFound`,** and so is every later `Append`, `Load` and `Log` of it.

For `Liveness`:

5. **`Beat` keeps an expired row.** The next `Beat` on it returns `session.ErrExpired`, and the row never comes back. Only a `ttl` of 0 removes a row, and a `Beat` with a `ttl` of 0 on a missing row is not an error.
6. **`Instance` of an unknown ID is not an error.** It is a state that is not alive.

For `Lister`:

7. **A listing is newest first, and a page ends where the next begins.** The most recently updated Record first (ties by ID); `Limit` bounds a page, and `Next` is the cursor of the page after it, empty after the last. Following `Next` reaches every Record that was not updated meanwhile exactly once; one that was may repeat or be skipped.

### What a Store does not do

- **It reads an Entry's `Seq`, `Kind` and `Layer`, and `IsCheckpoint` to find where a load starts, and the Header's `Epoch` (and `LogFrom`, in `Load`).** The rest of an Entry, its `Data`, the Header's `State` and its `Info` are kept as they are. The store never reads what Gotato's `State` holds.
- **It keeps every field.** `Header`, `RecordInfo`, `Entry` and `InstanceRow` gain fields over time. An adapter that maps them to columns must round-trip every field, also those it was written before; the suite fills each field by reflection and fails an adapter that drops one. Storing the whole value as JSON next to the columns you index keeps a later field for free.
- **`Records` gains no methods after 0.3.0.** A later capability is an optional interface that a caller finds by type assertion, as `Lister` is, so an adapter written today keeps compiling. Wrap neither seam in a type that hides what it wraps: a wrapper around a `Records` must pass on `List` if the caller is to find it.
- **What `Load`, `Log` and `List` return is the caller's to read and the store's to keep.** A caller must not modify it, and an adapter with a cache may return it without a copy. `MemoryStore` returns copies.

## The adapter

The pieces below are one file, `mystore.go`. It stands for a database with two tables: `sessions` (one row per Record: the ID and its `Header` as JSON) and `entries` (one row per Entry: `(id, seq)` unique, the Entry as JSON). A real adapter replaces the map and the mutex with a connection pool and transactions; the comments name the statement each step becomes.

### Tables and handles

Every handle must see what the others wrote, as two pods sharing one database do. So the state lives in a `DB` that handles share, and a `Records` or a `Liveness` is one handle on it.

```go
// Package mystore is a Store over a map of JSON rows, shaped like two SQL tables.
package mystore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// DB stands for the database. Every handle shares one, as two pods share a server.
type DB struct {
	mu        sync.Mutex
	records   map[string]*record // table sessions: id, header (JSON)
	instances map[string]instance
	now       func() time.Time // the database's clock: NOW()
}

type record struct {
	header  []byte   // the Header as JSON
	entries [][]byte // table entries: (id, seq) unique, the Entry as JSON; seq = index+1
}

type instance struct {
	row     session.InstanceRow
	expires time.Time
}

// NewDB returns an empty database. now is its clock; nil is the machine's.
func NewDB(now func() time.Time) *DB {
	if now == nil {
		now = time.Now
	}
	return &DB{records: map[string]*record{}, instances: map[string]instance{}, now: now}
}

// Records and Liveness are two handles on one DB. They could as well be on two.
type (
	Records  struct{ db *DB }
	Liveness struct{ db *DB }
)

var (
	_ session.Records  = (*Records)(nil)
	_ session.Lister   = (*Records)(nil)
	_ session.Liveness = (*Liveness)(nil)
)

// Store opens a handle of each kind, as session.Store takes them.
func (d *DB) Store() session.Store {
	return session.Store{Records: &Records{db: d}, Liveness: &Liveness{db: d}}
}
```

### Create and Append

`Create` inserts a Record whose first Entry is a Checkpoint at Seq 1. `Append` adds Entries after `after`. In SQL, `Append` is one transaction: check the epoch, then insert the rows. The unique `(id, seq)` key is what makes a stale writer lose, so map its violation to `ErrConflict`.

```go
func (r *Records) Create(ctx context.Context, id string, entries []session.Entry, h session.Header) error {
	if err := precheck(ctx, id); err != nil {
		return err
	}
	if err := session.CheckBatch(0, entries, h); err != nil {
		return err
	}
	rows, header, err := encode(entries, h)
	if err != nil {
		return err
	}
	r.db.mu.Lock()
	defer r.db.mu.Unlock()
	if _, ok := r.db.records[id]; ok { // INSERT hit the unique key on id
		return session.ErrExists
	}
	r.db.records[id] = &record{header: header, entries: rows}
	return nil
}

func (r *Records) Append(ctx context.Context, id string, after uint64, entries []session.Entry, h session.Header) error {
	if err := precheck(ctx, id); err != nil {
		return err
	}
	if err := session.CheckBatch(after, entries, h); err != nil {
		return err
	}
	rows, header, err := encode(entries, h)
	if err != nil {
		return err
	}
	r.db.mu.Lock() // one transaction
	defer r.db.mu.Unlock()
	rec, ok := r.db.records[id]
	if !ok {
		return session.ErrNotFound
	}
	var stored session.Header
	if err := json.Unmarshal(rec.header, &stored); err != nil {
		return err
	}
	if stored.Epoch != h.Epoch { // the epoch is checked before the position
		return session.ErrNotFound
	}
	if uint64(len(rec.entries)) != after { // the unique (id, seq) key would refuse the insert
		return session.ErrConflict
	}
	rec.entries = append(rec.entries, rows...)
	rec.header = header
	return nil
}
```

- **Order of checks in `Append`.** The epoch is compared before the position, so a writer of a deleted Record gets `ErrNotFound` even when its position would also be wrong.
- **`Create` of a taken ID is `ErrExists`.** An error other than `ErrExists` leaves the outcome unknown (a write may have been made but not confirmed), and the caller tolerates `ErrExists` on a retry.
- **A write is all or nothing.** In SQL, the batch and the Header update go in one transaction.

### Load and Log

`Load` reads the Record once: it finds the last Checkpoint, returns the facts from there and every Entry from `LogFrom`, returns the `segment_closed` Entries before it apart, and the Header of the last write. `Log` is `WHERE seq > after ORDER BY seq LIMIT limit`.

```go
func (r *Records) Load(ctx context.Context, id string) (session.Loaded, error) {
	if err := precheck(ctx, id); err != nil {
		return session.Loaded{}, err
	}
	r.db.mu.Lock() // one read: one transaction, or one snapshot
	defer r.db.mu.Unlock()
	rec, ok := r.db.records[id]
	if !ok {
		return session.Loaded{}, session.ErrNotFound
	}
	all, err := decode(rec.entries)
	if err != nil {
		return session.Loaded{}, err
	}
	var out session.Loaded
	if err := json.Unmarshal(rec.header, &out.Header); err != nil {
		return session.Loaded{}, err
	}
	start := 0 // the last Checkpoint
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].IsCheckpoint() {
			start = i
			break
		}
	}
	for _, e := range all[:start] {
		if e.Kind == gotato.EventKindSegmentClosed {
			out.Segments = append(out.Segments, e)
		}
	}
	for _, e := range all[start:] {
		if e.Layer == session.LayerFact || (out.Header.LogFrom != 0 && e.Seq >= out.Header.LogFrom) {
			out.Entries = append(out.Entries, e)
		}
	}
	out.Seq = uint64(len(all))
	return out, nil
}

func (r *Records) Log(ctx context.Context, id string, after uint64, limit int) ([]session.Entry, error) {
	if err := precheck(ctx, id); err != nil {
		return nil, err
	}
	r.db.mu.Lock()
	defer r.db.mu.Unlock()
	rec, ok := r.db.records[id]
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
- **A large Entry stays whole** in `Log`, and stays out of `Load` when it is a log Entry before `LogFrom`. Nothing in the adapter truncates `Data`.
- **`Load` is one read.** In SQL, take the Header and the Entries in one transaction (or one statement that joins them), so that a write landing between two selects cannot give a Header newer than the Entries. The suite writes while it loads to catch this.

### List and Delete

`List` is the optional `Lister`: a page of Records, the most recently updated first (ties by ID), and a cursor for the page after it. `Delete` removes the Record and its Entries, and an unknown ID is `ErrNotFound`.

```go
// List is a keyset page: ORDER BY updated_at DESC, id, after the cursor, LIMIT n+1.
func (r *Records) List(ctx context.Context, q session.ListQuery) (session.ListPage, error) {
	if err := ctx.Err(); err != nil {
		return session.ListPage{}, err
	}
	r.db.mu.Lock()
	infos := make([]session.RecordInfo, 0, len(r.db.records))
	for _, rec := range r.db.records {
		var h session.Header
		if err := json.Unmarshal(rec.header, &h); err != nil {
			r.db.mu.Unlock()
			return session.ListPage{}, err
		}
		infos = append(infos, h.Info)
	}
	r.db.mu.Unlock()
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].UpdatedAt.Equal(infos[j].UpdatedAt) {
			return infos[i].ID < infos[j].ID
		}
		return infos[i].UpdatedAt.After(infos[j].UpdatedAt)
	})
	if q.Cursor != "" { // the cursor is the last row of the page before: WHERE (updated_at, id) is after it
		raw, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		var last session.RecordInfo
		if err == nil {
			err = json.Unmarshal(raw, &last)
		}
		if err != nil {
			return session.ListPage{}, gotato.ErrorOf(gotato.CodeInvalidArgument, "mystore: bad cursor")
		}
		i := sort.Search(len(infos), func(i int) bool {
			if infos[i].UpdatedAt.Equal(last.UpdatedAt) {
				return infos[i].ID > last.ID
			}
			return infos[i].UpdatedAt.Before(last.UpdatedAt)
		})
		infos = infos[i:]
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 100 // the store's own page size
	}
	page := session.ListPage{Infos: infos}
	if len(infos) > limit {
		page.Infos = infos[:limit]
		raw, err := json.Marshal(page.Infos[limit-1])
		if err != nil {
			return session.ListPage{}, err
		}
		page.Next = base64.RawURLEncoding.EncodeToString(raw)
	}
	return page, nil
}

func (r *Records) Delete(ctx context.Context, id string) error {
	if err := precheck(ctx, id); err != nil {
		return err
	}
	r.db.mu.Lock()
	defer r.db.mu.Unlock()
	if _, ok := r.db.records[id]; !ok {
		return session.ErrNotFound
	}
	delete(r.db.records, id)
	return nil
}
```

- **The cursor is yours.** Gotato passes `Next` back as `Cursor` and reads neither; a database makes it from the last row's sort key. A cursor the adapter cannot read is `invalid_argument`.
- **No filters.** A `ListQuery` has no filter on purpose: each database indexes differently, and a service that needs to find Sessions by owner or state queries its own columns.

### Liveness

`Beat` renews an Instance's row until `ttl` from now. Time it by the Store's clock: in SQL, the database's `NOW()`, not the clock of the pod that calls, so a pod with a skewed clock cannot keep a dead row alive. The comment marks it, and the suite relies on it.

```go
func (l *Liveness) Beat(ctx context.Context, row session.InstanceRow, ttl time.Duration) error {
	if err := precheck(ctx, row.ID); err != nil {
		return err
	}
	l.db.mu.Lock()
	defer l.db.mu.Unlock()
	if ttl <= 0 { // an Instance ends its row; a missing row is not an error
		delete(l.db.instances, row.ID)
		return nil
	}
	now := l.db.now() // with SQL, the database's NOW(), not this process's clock
	if cur, ok := l.db.instances[row.ID]; ok && !now.Before(cur.expires) {
		return session.ErrExpired // the expired row stays
	}
	l.db.instances[row.ID] = instance{row: row, expires: now.Add(ttl)}
	return nil
}

func (l *Liveness) Instance(ctx context.Context, id string) (session.InstanceState, error) {
	if err := ctx.Err(); err != nil {
		return session.InstanceState{}, err
	}
	l.db.mu.Lock()
	defer l.db.mu.Unlock()
	cur, ok := l.db.instances[id]
	if !ok {
		return session.InstanceState{ID: id}, nil // unknown is not an error
	}
	return session.InstanceState{ID: id, Alive: l.db.now().Before(cur.expires), Draining: cur.row.Draining, Address: cur.row.Address}, nil
}
```

### Checks every call makes

Every method that takes an ID checks it and the context first. The rules themselves are Gotato's, so the adapter calls them: `session.CheckID` is the ID rule, and `session.CheckBatch` is rules 2 and 3.

```go
func precheck(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return session.CheckID(id)
}

func encode(entries []session.Entry, h session.Header) (rows [][]byte, header []byte, err error) {
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			return nil, nil, err
		}
		rows = append(rows, b)
	}
	header, err = json.Marshal(h) // the whole Header, so a later field is kept
	return rows, header, err
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

- **IDs.** Every Store refuses the same IDs, with `session.ErrInvalidID` (code `invalid_argument`): an empty one, one over `session.MaxIDBytes` (100), one that is not valid UTF-8 or has a control character, one with a path separator or `..`, and one that starts with a dot. `session.CheckID` is that rule, so call it and do not copy it. A Store may refuse more IDs, with `ErrInvalidID` too: the `FileStore` refuses one whose file name would be over 200 bytes, a limit that belongs to its files, and an adapter with no files keeps those IDs (23 CJK characters, say). The suite checks `CheckID`'s rule.
- **IDs differ by their characters alone.** `Case` and `case` are two Records whatever the collation of your database; so are the two spellings of an accented letter. Use a binary collation on the ID column.
- **Every call honors `ctx`.** A canceled context stops the call and writes nothing.

## The suite

### Run storetest

There is one suite for each seam, and each runs every check as a subtest, each on a new backing store. You give it a function that creates one empty backing store and returns how to open handles on it, and how to clean it up. `RunRecords` and `RunLister` take handles of `session.Records`, and `RunLiveness` takes handles of `session.Liveness`; a Store whose two seams live in two databases has two backings. The clock that times a Liveness row is the store's, so give `RunLiveness` a way to move it, `storetest.WithAdvance`, and no check sleeps.

```go
package mystore_test

import (
	"sync"
	"testing"
	"time"

	"example.com/yourapp/mystore"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/session/storetest"
)

func TestRecords(t *testing.T) {
	storetest.RunRecords(t, func() (storetest.Open, func(), error) {
		db := mystore.NewDB(nil)
		return func() session.Records { return db.Store().Records }, nil, nil
	})
}

// An adapter with a Lister runs its checks on the same backing.
func TestLister(t *testing.T) {
	storetest.RunLister(t, func() (storetest.Open, func(), error) {
		db := mystore.NewDB(nil)
		return func() session.Records { return db.Store().Records }, nil, nil
	})
}

// fakeClock is moved by the suite, so no check sleeps.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestLiveness(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)}
	storetest.RunLiveness(t, func() (storetest.OpenLiveness, func(), error) {
		db := mystore.NewDB(clock.Now)
		return func() session.Liveness { return db.Store().Liveness }, nil, nil
	}, storetest.WithAdvance(clock.Advance))
}
```

- **`Open` returns a new handle on the same backing store each time it is called.** Two handles are two Instances sharing one database; the suite uses them to prove that one sees what the other wrote and that racing writers lose.
- **For a real database,** create a schema (or a database) in the first function and drop it in the cleanup function. The suite makes one backing store per check: 26 for `RunRecords`, 5 for `RunLister` and 7 for `RunLiveness` today.
- **`storetest.CheckRecords`, `CheckLister` and `CheckLiveness(ctx, backing)`** do the same without `testing` and return the problems, for a tool that checks a store at startup.
- **Without `WithAdvance`,** `RunLiveness` sleeps past a short ttl, which works when the store's clock is the machine's and fails when it is a fake one you did not give it.

`go test -race -v` lists what the suite checks:

```text
--- PASS: TestRecords/Create_then_Load_returns_the_facts_and_Log_returns_every_Entry
--- PASS: TestRecords/Create_of_a_taken_ID_is_ErrExists
--- PASS: TestRecords/Load_returns_every_Entry_from_the_Header's_LogFrom,_each_once,_and_LogFrom_is_the_last_write's
--- PASS: TestRecords/a_Load_racing_Appends_reads_the_Record_in_one_read
--- PASS: TestRecords/a_stale_writer_is_refused_with_ErrConflict_and_writes_nothing
--- PASS: TestRecords/of_writers_racing_for_one_position,_exactly_one_wins
--- PASS: TestRecords/every_field_of_Header,_RecordInfo_and_Entry_is_kept
--- PASS: TestLister/List_pages_by_Limit_and_Cursor,_every_Record_once
--- PASS: TestLiveness/an_expired_Liveness_row_is_not_alive,_and_Beat_on_it_is_ErrExpired
--- PASS: TestLiveness/every_field_of_InstanceRow_is_kept
...
```

### No database at hand

The suite needs no external service and no transaction. `session.NewMemoryStore`, `session.NewFileStore` and the `mystore` above all pass it ([`TestMemoryStoreConformance` and `TestFileStoreConformance`](../session/file_test.go) run it on the first two), so you can write the adapter's tests against a map or a directory first and point the same function at a real database later. To check a store outside `go test`, for example at startup, call `storetest.CheckRecords`.

What the contract asks of a backing store is one conditional write: a single-document atomic write, plus a unique `(id, seq)` key, is enough. A multi-document transaction is a cost of one design (entries and head in separate documents or rows), not a Gotato rule. If your database cannot take a transaction, choose a layout that does not need one; `Load` is the one read that must not tear, so a layout with the Header apart from the Entries reads both in one snapshot.

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
func TestRecords(t *testing.T) {
	dsn := os.Getenv("STORE_TEST_DSN")
	if dsn == "" {
		t.Skip("STORE_TEST_DSN is not set")
	}
	storetest.RunRecords(t, func() (storetest.Open, func(), error) {
		db, drop, err := newSchema(dsn) // your helper: a new schema or database per call
		if err != nil {
			return nil, nil, err
		}
		return func() session.Records { return mystore.NewRecords(db) }, drop, nil
	})
}
```

Swap the image for yours, and add the same for `RunLiveness` and `RunLister`. The function runs once per check, so make creating and dropping a schema cheap. For a database that needs a replica set, see the next section.

### MongoDB

Mongo adapters meet the same rules as any other, in Mongo's terms.

- **Unique index on `(id, seq)`** in the entries collection. A write of a position that is taken fails with a duplicate-key error, code 11000 (`E11000`). Map that, and only that, to `session.ErrConflict` (in the Go driver, `mongo.IsDuplicateKeyError`). On `Create`, a duplicate `_id` in the head document is `session.ErrExists`.
- **The epoch goes in the head document's filter.** Update the head with a filter on the ID and `Header.Epoch`, and on the head position when you use it as the guard. When it matches nothing, read the head document: a missing one, or one with another epoch, is `session.ErrNotFound`; any other is `session.ErrConflict`. The epoch is judged before the position (rule 3); the suite has a check for each order (`Append by a writer of another epoch is ErrNotFound before its position is checked`).
- **All or nothing needs a transaction, or a head-document CAS.** An `InsertMany` of several entries is not atomic by itself. A multi-document transaction (entries and head in one) needs a replica set, which a standalone `mongod` is not. Without a transaction, make the head document the guard: move its head position by compare-and-set first, then write the entries, and design what a reader sees if the process dies in between. The suite does not kill a process between two writes, so it cannot tell you that part is right.

**A single-node replica set without Docker.** *Reported from real use, not run in this repository.* A Mongo Store passed all 30 subtests of the suite as it was then (one `storetest.Run`, before it was split into `RunRecords`, `RunLiveness` and `RunLister`), also with `-race -count=5`, on a single-node replica set started in user space with MongoDB 8.0: no Docker and no package manager.

1. Download the official MongoDB 8.0 Community Server tarball for your platform from mongodb.com and unpack it in a directory you own.
2. Start `mongod` from it with `--replSet <name>`, a `--dbpath` you own and a `--port` that is free (add `--bind_ip 127.0.0.1`).
3. Connect once with `mongosh` and run `rs.initiate()` (the `replSetInitiate` command). It is needed once; the set elects itself primary in a few seconds.
4. Point the test at the connection string of that node. Check the exact flags and shell against the MongoDB documentation for your version.

Not covered by that run: a set of several nodes, a transaction retried after a failover, and several pods writing one Session. Test those against your own deployment before you rely on them.

### When a check fails

A failure message names the rule that broke. If the adapter above returned `ErrNotFound` where it returns `ErrConflict` (a unique-key violation mapped to the wrong error), the suite says:

```text
--- FAIL: TestRecords/a_stale_writer_is_refused_with_ErrConflict_and_writes_nothing
    storetest.go:76: an Append for a position another writer already wrote returned not_found: session: not found, want ErrConflict: a position is written once
--- FAIL: TestRecords/of_writers_racing_for_one_position,_exactly_one_wins
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
