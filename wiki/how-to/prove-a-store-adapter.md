# Prove a store adapter

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Prove a store adapter

By the end of this page your adapter passes the conformance suite: `storetest.RunRecords`, `RunLiveness` and, if you list, `RunLister`. You need [Write a store adapter](write-a-store-adapter.md).

## Run the suite

There is one suite for each seam, and each runs every check as a subtest, each on a new backing store. You give it a function that creates one empty backing store and returns how to open handles on it, and how to clean it up. `RunRecords` and `RunLister` take handles of `store.Records`, and `RunLiveness` takes handles of `store.Liveness`; a Store whose two seams live in two databases has two backings. The clock that times a Liveness row is the store's, so give `RunLiveness` a way to move it, `storetest.WithAdvance`, and no check sleeps.

```go
package mystore_test

import (
	"sync"
	"testing"
	"time"

	"example.com/yourapp/mystore"
	"github.com/jinhuang712/gotato/store"
	"github.com/jinhuang712/gotato/store/storetest"
)

func TestRecords(t *testing.T) {
	storetest.RunRecords(t, func() (storetest.Open, func(), error) {
		db := mystore.NewDB(nil)
		return func() store.Records { return db.Store().Records }, nil, nil
	})
}

// An adapter with a Lister runs its checks on the same backing.
func TestLister(t *testing.T) {
	storetest.RunLister(t, func() (storetest.Open, func(), error) {
		db := mystore.NewDB(nil)
		return func() store.Records { return db.Store().Records }, nil, nil
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
		return func() store.Liveness { return db.Store().Liveness }, nil, nil
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
--- PASS: TestRecords/every_field_of_Header,_Info_and_Entry_is_kept
--- PASS: TestLister/List_pages_by_Limit_and_Cursor,_every_Record_once
--- PASS: TestLiveness/an_expired_Liveness_row_is_not_alive,_and_Beat_on_it_is_ErrExpired
--- PASS: TestLiveness/every_field_of_InstanceRow_is_kept
...
```

## Run it without a database

The suite needs no external service and no transaction. `store.NewMemoryStore`, `store.NewFileStore` and the `mystore` above all pass it ([`TestMemoryStoreConformance` and `TestFileStoreConformance`](../../store/file_test.go) run it on the first two), so you can write the adapter's tests against a map or a directory first and point the same function at a real database later. To check a store outside `go test`, for example at startup, call `storetest.CheckRecords`.

What the contract asks of a backing store is one conditional write: a single-document atomic write, plus a unique `(id, seq)` key, is enough. A multi-document transaction is a cost of one design (entries and head in separate documents or rows), not a Gotato rule. If your database cannot take a transaction, choose a layout that does not need one; `Load` is the one read that must not tear, so a layout with the Header apart from the Entries reads both in one snapshot.

## Read a failure

A failure message names the rule that broke. If the adapter above returned `ErrNotFound` where it returns `ErrConflict` (a unique-key violation mapped to the wrong error), the suite says:

```text
--- FAIL: TestRecords/a_stale_writer_is_refused_with_ErrConflict_and_writes_nothing
    storetest.go:76: an Append for a position another writer already wrote returned not_found: record: not found, want ErrConflict: a position is written once
--- FAIL: TestRecords/of_writers_racing_for_one_position,_exactly_one_wins
    storetest.go:76: writer 0 that lost the race for position 4 returned not_found: record: not found, want ErrConflict
```

## Know what the suite does not check

- **Your database's failure modes.** A lost connection, a deadlock the database retries, a serialization failure: map the ones that mean "someone else wrote first" to `ErrConflict`, and let the rest be plain errors. The Runner reports them as `store_failure`.
- **Your transaction isolation.** The race check runs writers against your handles at once, but a database at a weak isolation level can pass it on a quiet machine. Rely on the unique key, not on a read before the write.
- **Your scale.** The suite checks behavior, not speed. `Load` reads from the last Checkpoint on, so index `(id, seq)` and make sure your database finds the last Checkpoint without reading the whole log.

## See also

- [Test a store on a real database](test-a-store-on-a-real-database.md)
- [How the store contract works](../../specs/store.md)
- [The suite, as the user sees it](../../FEATURES.md#f84-conformance-suite)
