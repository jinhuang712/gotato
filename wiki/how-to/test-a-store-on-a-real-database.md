# Test a store on a real database

> [Wiki](../README.md) › [How-to](../README.md#how-to) › Test a store on a real database

By the end of this page the suite runs against the database you ship on, in CI. A mock passes for the wrong reasons. These sections are illustrations that were not compiled or run here, except where they say otherwise. You need [Prove a store adapter](prove-a-store-adapter.md).

## Run the suite in CI

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
		return func() store.Records { return mystore.NewRecords(db) }, drop, nil
	})
}
```

Swap the image for yours, and add the same for `RunLiveness` and `RunLister`. The function runs once per check, so make creating and dropping a schema cheap. For a database that needs a replica set, see the next section.

## Map the rules onto MongoDB

Mongo adapters meet the same rules as any other, in Mongo's terms.

- **Unique index on `(id, seq)`** in the entries collection. A write of a position that is taken fails with a duplicate-key error, code 11000 (`E11000`). Map that, and only that, to `store.ErrConflict` (in the Go driver, `mongo.IsDuplicateKeyError`). On `Create`, a duplicate `_id` in the head document is `store.ErrExists`.
- **The epoch goes in the head document's filter.** Update the head with a filter on the ID and `Header.Epoch`, and on the head position when you use it as the guard. When it matches nothing, read the head document: a missing one, or one with another epoch, is `record.ErrNotFound`; any other is `store.ErrConflict`. The epoch is judged before the position (rule 3); the suite has a check for each order (`Append by a writer of another epoch is ErrNotFound before its position is checked`).
- **All or nothing needs a transaction, or a head-document CAS.** An `InsertMany` of several entries is not atomic by itself. A multi-document transaction (entries and head in one) needs a replica set, which a standalone `mongod` is not. Without a transaction, make the head document the guard: move its head position by compare-and-set first, then write the entries, and design what a reader sees if the process dies in between. The suite does not kill a process between two writes, so it cannot tell you that part is right.

**A single-node replica set without Docker.** *Reported from real use, not run in this repository.* A Mongo Store passed all 30 subtests of the suite as it was then (one `storetest.Run`, before it was split into `RunRecords`, `RunLiveness` and `RunLister`), also with `-race -count=5`, on a single-node replica set started in user space with MongoDB 8.0: no Docker and no package manager.

1. Download the official MongoDB 8.0 Community Server tarball for your platform from mongodb.com and unpack it in a directory you own.
2. Start `mongod` from it with `--replSet <name>`, a `--dbpath` you own and a `--port` that is free (add `--bind_ip 127.0.0.1`).
3. Connect once with `mongosh` and run `rs.initiate()` (the `replSetInitiate` command). It is needed once; the set elects itself primary in a few seconds.
4. Point the test at the connection string of that node. Check the exact flags and shell against the MongoDB documentation for your version.

Not covered by that run: a set of several nodes, a transaction retried after a failover, and several pods writing one Session. Test those against your own deployment before you rely on them.

## See also

- [Write a store adapter](write-a-store-adapter.md)
- [How the store contract works](../../specs/store.md)
