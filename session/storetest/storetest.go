// Package storetest checks that a session.Store keeps the contract Gotato
// relies on: a Record is an append-only log, every write is all or nothing,
// a position is written once, a stale writer is refused, a load starts at the
// last Checkpoint (a session_created or compaction_ended Entry, or the
// "checkpoint" of the earlier format) and hands back the segment_closed
// Entries before it, Liveness rows expire by the store's clock, and a row its
// Instance ended is removed.
//
// An adapter runs it from its own tests:
//
//	func TestMyStore(t *testing.T) {
//		storetest.Run(t, func() (storetest.Open, func(), error) {
//			db := newTestDatabase(t)
//			return func() session.Store { return mystore.New(db) }, db.Close, nil
//		})
//	}
//
// It is a package of its own so that importing testkit does not link the
// testing package into a program.
package storetest

import (
	"context"
	"fmt"
	"testing"

	"github.com/jinhuang712/gotato/session"
)

// Open returns a new handle on one backing store. Every handle must see what
// the others wrote, as two Instances sharing one database do.
type Open func() session.Store

// Backing creates one new, empty backing store, returns how to open handles
// on it, and how to clean it up.
type Backing func() (Open, func(), error)

type check struct {
	name string
	run  func(ctx context.Context, open Open) error
}

var checks = []check{
	{"Create then Load returns the facts and Log returns every Entry", checkCreateLoad},
	{"Create of a taken ID is ErrExists", checkCreateTwice},
	{"RecordInfo keeps pending notices and the last run end through Create, Append, Load and List", checkInfoNotices},
	{"Append adds Entries after the head, seen by Log and Load", checkAppend},
	{"Load starts at the last Checkpoint", checkLastCheckpoint},
	{"Load returns the segment_closed Entries from before the last Checkpoint apart", checkSegments},
	{"a large log Entry is kept whole, read by Log, and left out of Load", checkSpilledEntry},
	{"a stale writer is refused with ErrConflict and writes nothing", checkStaleWriter},
	{"Append must continue the head, or it is refused", checkContinuesHead},
	{"of writers racing for one position, exactly one wins", checkRace},
	{"Log returns at most limit Entries, and every Entry when limit is 0 or less", checkLogLimit},
	{"List returns the last RecordInfo of each Record, newest update first", checkList},
	{"RecordInfo.RecordEpoch is kept by Create and Append and returned by Load and List", checkRecordEpoch},
	{"Delete removes a Record, and Delete of a missing one is ErrNotFound", checkDelete},
	{"Append by a writer of a deleted and re-created Record is ErrNotFound", checkDeletedWriter},
	{"Load, Log and Append of an unknown Record are ErrNotFound", checkUnknown},
	{"a canceled context stops Append and Load and writes nothing", checkCanceled},
	{"Beat creates a Liveness row that Instance returns, and ttl 0 ends it", checkLiveness},
	{"an expired Liveness row is not alive, and Beat on it is ErrExpired", checkExpiry},
	{"a row ended with ttl 0 is removed, for every handle", checkEndedRemoved},
	{"an ID the Store cannot keep is refused with ErrInvalidID by every call that takes an ID", checkInvalidID},
	{"IDs that differ only in case or Unicode form are different Records", checkIDCase},
}

// Run runs every check as a subtest, each on a new backing store.
func Run(t *testing.T, backing Backing) {
	t.Helper()
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if err := runCheck(context.Background(), backing, c); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Check runs every check, each on a new backing store, and returns the
// problems it found, named by check.
func Check(ctx context.Context, backing Backing) []error {
	var problems []error
	for _, c := range checks {
		if err := runCheck(ctx, backing, c); err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", c.name, err))
		}
	}
	return problems
}

func runCheck(ctx context.Context, backing Backing, c check) (err error) {
	open, cleanup, err := backing()
	if err != nil {
		return fmt.Errorf("backing: %w", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic: %v", recovered)
		}
	}()
	return c.run(ctx, open)
}
