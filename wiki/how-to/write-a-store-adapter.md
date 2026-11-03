# Write a store adapter

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Write a store adapter

By the end of this page you have a `store.Store` of your own, written against the contract. You need [Run Sessions on many pods](run-sessions-on-many-pods.md) and a database in mind. The adapter below keeps its rows in a map shaped like two SQL tables, so each line says which statement it stands for; its snippets were compiled and the suite was run against it.

## The contract

### What a Store is

A `store.Store` is a struct of two seams, `Store{Records: …, Liveness: …}`, so the two may live in different databases. Each is an interface, and a caller finds anything optional by type assertion on the field.

The contract is in two packages, and the line between them is "does it exist without being saved?". Package `store` holds the contract (`Store`, `Records`, `Liveness`, `Lister`, `Definitions`), the checks every store makes (`CheckID`, `CheckBatch`), the errors `ErrExists`, `ErrConflict`, `ErrExpired` and `ErrInvalidID`, `InstanceRow` and `InstanceState`, and the suite, `store/storetest`. Package `record` holds what an adapter keeps and returns: `Entry`, `Header`, `Info`, `Loaded`, and `ErrNotFound`, which a store returns for a Record that is not there. An adapter imports both; `store` imports `record`, never the reverse.

- **`Records`** keeps Records: a Record is a Session's append-only log of Entries (see [GLOSSARY.md](../../GLOSSARY.md#core)). An Entry is one line: a `Seq` (its position, from 1, with no gap), a `Kind` (the Event kind), a `Layer` (`fact` or `log`), the time, the Instance that wrote it, and its `Data` as JSON. Next to the log the store keeps the `Header` of the last write: the Record's `Epoch`, an `Info` of listing columns (ID, counts, times, metadata), a `State` that is Gotato's own bytes, and `LogFrom`. It has five methods: `Create`, `Append`, `Load`, `Log` and `Delete`. Every write is one conditional write: a batch of Entries goes in all or nothing, and only to positions that are still free. An adapter maps that to an insert on a unique `(session, seq)` key.
- **`Liveness`** keeps one row per running Instance, renewed on a heartbeat and timed by the store's clock. When a row expires, every Claim of that Instance ends at once. This is how another Instance knows it may take a Session over. It has two methods: `Beat` and `Instance`.
- **`Lister`** is optional, on `Records`: `List(ctx, ListQuery{Limit, Cursor}) (ListPage{Infos, Next}, error)`. The runtime never lists, so an adapter that does not implement it still runs every Session; only `Runner.Sessions` (and `GET /v1/sessions`) then answers `not_supported`. Write it when your service lists Sessions.

### The rules

These are the rules an adapter is most likely to miss. The suite has a check for each, and its message names the rule.

For `Records`:

1. **Load reads the Record once.** It returns the Entries of Layer `fact` from the last Checkpoint on (`Entry.IsCheckpoint`: `session_created` or `compaction_ended`), plus every Entry, log Entries too, from `Header.LogFrom` when that is not 0; the `segment_closed` Entries from before the Checkpoint, apart; the position of the last Entry (`Loaded.Seq`); and the Header of the last write. All of it comes from one read, so a `Load` that races an `Append` returns the Entries and the Header of the same write. `Log` returns every Entry, facts and log, in order.
2. **A write continues the log.** The Entries have the Seqs `after+1`, `after+2` and so on, and a Record starts with a Checkpoint at Seq 1. A batch that does not is refused with `invalid_argument`. An `Append` whose `after` is not the Record's last position is refused with `store.ErrConflict`. Of writers racing for one position, exactly one wins.
3. **The Record epoch is required.** `Header.Epoch` must be set on `Create` and `Append`; a write without it is `invalid_argument`. `Append` checks it before the position. An epoch that is not the stored Record's is `ErrNotFound`, not `ErrConflict`: the Record was deleted and created again, and the writer belongs to the old one.
4. **Delete of an unknown Record is `ErrNotFound`,** and so is every later `Append`, `Load` and `Log` of it.

For `Liveness`:

5. **`Beat` keeps an expired row.** The next `Beat` on it returns `store.ErrExpired`, and the row never comes back. Only a `ttl` of 0 removes a row, and a `Beat` with a `ttl` of 0 on a missing row is not an error.
6. **`Instance` of an unknown ID is not an error.** It is a state that is not alive.

For `Lister`:

7. **A listing is newest first, and a page ends where the next begins.** The most recently updated Record first (ties by ID); `Limit` bounds a page, and `Next` is the cursor of the page after it, empty after the last. Following `Next` reaches every Record that was not updated meanwhile exactly once; one that was may repeat or be skipped.

### What a Store does not do

- **It reads an Entry's `Seq`, `Kind` and `Layer`, and `IsCheckpoint` to find where a load starts, and the Header's `Epoch` (and `LogFrom`, in `Load`).** The rest of an Entry, its `Data`, the Header's `State` and its `Info` are kept as they are. The store never reads what Gotato's `State` holds.
- **It keeps every field.** `Header`, `Info`, `Entry` and `InstanceRow` gain fields over time. An adapter that maps them to columns must round-trip every field, also those it was written before; the suite fills each field by reflection and fails an adapter that drops one. Storing the whole value as JSON next to the columns you index keeps a later field for free.
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
	"github.com/jinhuang712/gotato/record"
	"github.com/jinhuang712/gotato/store"
)

// DB stands for the database. Every handle shares one, as two pods share a server.
type DB struct {
	mu        sync.Mutex
	records   map[string]*recRow // table sessions: id, header (JSON)
	instances map[string]instance
	now       func() time.Time // the database's clock: NOW()
}

type recRow struct {
	header  []byte   // the Header as JSON
	entries [][]byte // table entries: (id, seq) unique, the Entry as JSON; seq = index+1
}

type instance struct {
	row     store.InstanceRow
	expires time.Time
}

// NewDB returns an empty database. now is its clock; nil is the machine's.
func NewDB(now func() time.Time) *DB {
	if now == nil {
		now = time.Now
	}
	return &DB{records: map[string]*recRow{}, instances: map[string]instance{}, now: now}
}

// Records and Liveness are two handles on one DB. They could as well be on two.
type (
	Records  struct{ db *DB }
	Liveness struct{ db *DB }
)

var (
	_ store.Records  = (*Records)(nil)
	_ store.Lister   = (*Records)(nil)
	_ store.Liveness = (*Liveness)(nil)
)

// Store opens a handle of each kind, as store.Store takes them.
func (d *DB) Store() store.Store {
	return store.Store{Records: &Records{db: d}, Liveness: &Liveness{db: d}}
}
```

### Create and Append

`Create` inserts a Record whose first Entry is a Checkpoint at Seq 1. `Append` adds Entries after `after`. In SQL, `Append` is one transaction: check the epoch, then insert the rows. The unique `(id, seq)` key is what makes a stale writer lose, so map its violation to `ErrConflict`.

```go
func (r *Records) Create(ctx context.Context, id string, entries []record.Entry, h record.Header) error {
	if err := precheck(ctx, id); err != nil {
		return err
	}
	if err := store.CheckBatch(0, entries, h); err != nil {
		return err
	}
	rows, header, err := encode(entries, h)
	if err != nil {
		return err
	}
	r.db.mu.Lock()
	defer r.db.mu.Unlock()
	if _, ok := r.db.records[id]; ok { // INSERT hit the unique key on id
		return store.ErrExists
	}
	r.db.records[id] = &recRow{header: header, entries: rows}
	return nil
}

func (r *Records) Append(ctx context.Context, id string, after uint64, entries []record.Entry, h record.Header) error {
	if err := precheck(ctx, id); err != nil {
		return err
	}
	if err := store.CheckBatch(after, entries, h); err != nil {
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
		return record.ErrNotFound
	}
	var stored record.Header
	if err := json.Unmarshal(rec.header, &stored); err != nil {
		return err
	}
	if stored.Epoch != h.Epoch { // the epoch is checked before the position
		return record.ErrNotFound
	}
	if uint64(len(rec.entries)) != after { // the unique (id, seq) key would refuse the insert
		return store.ErrConflict
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
func (r *Records) Load(ctx context.Context, id string) (record.Loaded, error) {
	if err := precheck(ctx, id); err != nil {
		return record.Loaded{}, err
	}
	r.db.mu.Lock() // one read: one transaction, or one snapshot
	defer r.db.mu.Unlock()
	rec, ok := r.db.records[id]
	if !ok {
		return record.Loaded{}, record.ErrNotFound
	}
	all, err := decode(rec.entries)
	if err != nil {
		return record.Loaded{}, err
	}
	var out record.Loaded
	if err := json.Unmarshal(rec.header, &out.Header); err != nil {
		return record.Loaded{}, err
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
		if e.Layer == record.LayerFact || (out.Header.LogFrom != 0 && e.Seq >= out.Header.LogFrom) {
			out.Entries = append(out.Entries, e)
		}
	}
	out.Seq = uint64(len(all))
	return out, nil
}

func (r *Records) Log(ctx context.Context, id string, after uint64, limit int) ([]record.Entry, error) {
	if err := precheck(ctx, id); err != nil {
		return nil, err
	}
	r.db.mu.Lock()
	defer r.db.mu.Unlock()
	rec, ok := r.db.records[id]
	if !ok {
		return nil, record.ErrNotFound
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
func (r *Records) List(ctx context.Context, q store.ListQuery) (store.ListPage, error) {
	if err := ctx.Err(); err != nil {
		return store.ListPage{}, err
	}
	r.db.mu.Lock()
	infos := make([]record.Info, 0, len(r.db.records))
	for _, rec := range r.db.records {
		var h record.Header
		if err := json.Unmarshal(rec.header, &h); err != nil {
			r.db.mu.Unlock()
			return store.ListPage{}, err
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
		var last record.Info
		if err == nil {
			err = json.Unmarshal(raw, &last)
		}
		if err != nil {
			return store.ListPage{}, gotato.ErrorOf(gotato.CodeInvalidArgument, "mystore: bad cursor")
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
	page := store.ListPage{Infos: infos}
	if len(infos) > limit {
		page.Infos = infos[:limit]
		raw, err := json.Marshal(page.Infos[limit-1])
		if err != nil {
			return store.ListPage{}, err
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
		return record.ErrNotFound
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
func (l *Liveness) Beat(ctx context.Context, row store.InstanceRow, ttl time.Duration) error {
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
		return store.ErrExpired // the expired row stays
	}
	l.db.instances[row.ID] = instance{row: row, expires: now.Add(ttl)}
	return nil
}

func (l *Liveness) Instance(ctx context.Context, id string) (store.InstanceState, error) {
	if err := ctx.Err(); err != nil {
		return store.InstanceState{}, err
	}
	l.db.mu.Lock()
	defer l.db.mu.Unlock()
	cur, ok := l.db.instances[id]
	if !ok {
		return store.InstanceState{ID: id}, nil // unknown is not an error
	}
	return store.InstanceState{ID: id, Alive: l.db.now().Before(cur.expires), Draining: cur.row.Draining, Address: cur.row.Address}, nil
}
```

### Checks every call makes

Every method that takes an ID checks it and the context first. The rules themselves are Gotato's, so the adapter calls them: `store.CheckID` is the ID rule, and `store.CheckBatch` is rules 2 and 3.

```go
func precheck(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return store.CheckID(id)
}

func encode(entries []record.Entry, h record.Header) (rows [][]byte, header []byte, err error) {
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

func decode(rows [][]byte) ([]record.Entry, error) {
	out := make([]record.Entry, len(rows))
	for i, b := range rows {
		if err := json.Unmarshal(b, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}
```

- **IDs.** Every Store refuses the same IDs, with `store.ErrInvalidID` (code `invalid_argument`): an empty one, one over `store.MaxIDBytes` (100), one that is not valid UTF-8 or has a control character, one with a path separator or `..`, and one that starts with a dot. `store.CheckID` is that rule, so call it and do not copy it. A Store may refuse more IDs, with `ErrInvalidID` too: the `FileStore` refuses one whose file name would be over 200 bytes, a limit that belongs to its files, and an adapter with no files keeps those IDs (23 CJK characters, say). The suite checks `CheckID`'s rule.
- **IDs differ by their characters alone.** `Case` and `case` are two Records whatever the collation of your database; so are the two spellings of an accented letter. Use a binary collation on the ID column.
- **Every call honors `ctx`.** A canceled context stops the call and writes nothing.

## See also

- [Prove a store adapter](prove-a-store-adapter.md)
- [Test a store on a real database](test-a-store-on-a-real-database.md)
- [Write a definitions store](write-a-definitions-store.md)
- [How the store contract works](../../specs/store.md)
- [The reference stores: memory](../../store/memory.go)
- [The reference stores: file](../../store/file.go)
