// Package storetest checks that a store keeps the contract Gotato relies on.
// There is one suite for each seam of a store.Store:
//
//   - RunRecords checks store.Records: a Record is an append-only log, every
//     write is all or nothing, a position is written once, a stale writer is
//     refused, the epoch is checked before the position, a Load reads the
//     Record in one read (the facts from the last Checkpoint, the
//     segment_closed Entries before it, the position of the last Entry and
//     the Header of the last write), and a store keeps the Header and every
//     field of an Entry as it was written.
//   - RunLiveness checks store.Liveness: rows expire by the store's clock,
//     an expired row never comes back, and a row its Instance ended is removed.
//   - RunDefinitions checks store.Definitions: a document is kept byte for
//     byte under its (kind, name), kind and name are independent parts of the
//     key, a save replaces, handles share one backing, no bytes are aliased, an
//     invalid kind or name is ErrInvalidID, and concurrent saves of one key
//     leave one whole document.
//   - RunLister checks store.Lister, the optional listing of Records: the
//     most recently updated first, paged by Limit and Cursor.
//
// An adapter runs the suites it implements from its own tests:
//
//	func TestMyRecords(t *testing.T) {
//		storetest.RunRecords(t, func() (storetest.Open, func(), error) {
//			db := newTestDatabase(t)
//			return func() store.Records { return mystore.NewRecords(db) }, db.Close, nil
//		})
//	}
//
// RunLister takes the same Backing as RunRecords, and every handle it opens
// must be a store.Lister too. RunLiveness takes a LivenessBacking, and the
// option WithAdvance, which moves the store's clock instead of sleeping.
//
// It is a package of its own so that importing testkit does not link the
// testing package into a program.
package storetest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/store"
)

// Open returns a new handle on one backing store of Records. Every handle must
// see what the others wrote, as two Instances sharing one database do.
type Open func() store.Records

// Backing creates one new, empty backing store of Records, returns how to open
// handles on it, and how to clean it up. RunRecords and RunLister take it.
type Backing func() (Open, func(), error)

// OpenLiveness returns a new handle on one backing store of Liveness rows.
type OpenLiveness func() store.Liveness

// LivenessBacking creates one new, empty backing store of Liveness rows,
// returns how to open handles on it, and how to clean it up.
type LivenessBacking func() (OpenLiveness, func(), error)

// OpenDefinitions returns a new handle on one backing store of definitions.
// Every handle must see what the others wrote.
type OpenDefinitions func() store.Definitions

// DefinitionsBacking creates one new, empty backing store of definitions,
// returns how to open handles on it, and how to clean it up.
type DefinitionsBacking func() (OpenDefinitions, func(), error)

// LivenessOption configures RunLiveness and CheckLiveness.
type LivenessOption func(*livenessConfig)

type livenessConfig struct {
	advance func(time.Duration)
}

// WithAdvance gives the suite a way to move the store's clock forward by d. A
// check that needs a row to expire, or a renewed row to outlive its first ttl,
// calls it instead of sleeping, so an adapter that times rows by a clock it
// controls (store.WithClock on the reference stores) runs the suite at once.
// Without it the suite sleeps.
func WithAdvance(advance func(d time.Duration)) LivenessOption {
	return func(c *livenessConfig) { c.advance = advance }
}

// wait moves the store's clock forward by d, or sleeps for d.
func (c livenessConfig) wait(d time.Duration) {
	if c.advance != nil {
		c.advance(d)
		return
	}
	time.Sleep(d)
}

func newLivenessConfig(options []LivenessOption) livenessConfig {
	var config livenessConfig
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	return config
}

type check struct {
	name string
	run  func(ctx context.Context, open Open) error
}

type livenessCheck struct {
	name string
	run  func(ctx context.Context, open OpenLiveness, config livenessConfig) error
}

var recordsChecks = []check{
	{"Create then Load returns the facts and Log returns every Entry", checkCreateLoad},
	{"Create of a taken ID is ErrExists", checkCreateTwice},
	{"Append adds Entries after the head, seen by Log and Load", checkAppend},
	{"Load starts at the last Checkpoint", checkLastCheckpoint},
	{"Load returns the segment_closed Entries from before the last Checkpoint apart", checkSegments},
	{"a large log Entry is kept whole, read by Log, and left out of Load", checkSpilledEntry},
	{"a Load racing Appends reads the Record in one read", checkLoadRacingAppends},
	{"a stale writer is refused with ErrConflict and writes nothing", checkStaleWriter},
	{"Append must continue the head, or it is refused", checkContinuesHead},
	{"of writers racing for one position, exactly one wins", checkRace},
	{"Log returns at most limit Entries, and every Entry when limit is 0 or less", checkLogLimit},
	{"Header.Epoch is kept by Create and Append and returned by Load", checkEpochKept},
	{"Delete removes a Record, and Delete of a missing one is ErrNotFound", checkDelete},
	{"Append by a writer of a deleted and re-created Record is ErrNotFound", checkDeletedWriter},
	{"Append by a writer of another epoch is ErrNotFound before its position is checked", checkEpochBeforePosition},
	{"of Instances creating one ID at once, exactly one wins with the others ErrExists", checkConcurrentCreate},
	{"Log after the head is an empty answer", checkLogPastHead},
	{"Delete of a Record leaves the others, and a Record created again starts empty", checkDeleteIsolated},
	{"Load, Log and Append of an unknown Record are ErrNotFound", checkUnknown},
	{"a canceled context stops Append and Load and writes nothing", checkCanceled},
	{"an ID the store cannot keep is refused with ErrInvalidID by every call that takes an ID", checkInvalidID},
	{"Create and Append refuse a Header with no Epoch with invalid_argument", checkEmptyEpoch},
	{"every field of Header, Info and Entry is kept", checkFieldsKept},
	{"IDs that differ only in case or Unicode form are different Records", checkIDCase},
}

type definitionsCheck struct {
	name string
	run  func(ctx context.Context, open OpenDefinitions) error
}

var definitionsChecks = []definitionsCheck{
	{"a document is kept byte for byte, JSON or not", checkDefinitionRoundTrip},
	{"LoadDefinition of an unknown key is ErrNotFound", checkDefinitionNotFound},
	{"kind and name are independent parts of the key", checkDefinitionKeysIndependent},
	{"SaveDefinition replaces what was there", checkDefinitionReplaces},
	{"every handle sees what the others saved", checkDefinitionHandlesShare},
	{"a store never shares bytes with its caller", checkDefinitionNotAliased},
	{"a kind or name the store cannot keep is ErrInvalidID", checkDefinitionInvalidKey},
	{"concurrent saves of one key leave one whole document", checkDefinitionConcurrentSaves},
}

var livenessChecks = []livenessCheck{
	{"Beat creates a Liveness row that Instance returns, and ttl 0 ends it", checkLiveness},
	{"Beat on a live row renews it and replaces the whole row", checkBeatRenews},
	{"Beat with ttl 0 on a row that is not there is not an error", checkBeatEndsMissingRow},
	{"an expired Liveness row is not alive, and Beat on it is ErrExpired", checkExpiry},
	{"a row ended with ttl 0 is removed, for every handle", checkEndedRemoved},
	{"an Instance ID the store cannot keep is refused by Beat with ErrInvalidID", checkBeatInvalidID},
	{"every field of InstanceRow is kept", checkRowFieldsKept},
}

var listerChecks = []check{
	{"List returns the last Info of each Record, newest update first", checkList},
	{"List pages by Limit and Cursor, every Record once", checkListPaging},
	{"List with a Cursor the store did not make is an error", checkListBadCursor},
	{"every field of Info is kept and listed", checkListInfoKept},
	{"List leaves out a deleted Record and keeps IDs that differ by case or Unicode form apart", checkListAfterDelete},
}

// RunRecords runs every check of store.Records as a subtest, each on a new
// backing store.
func RunRecords(t *testing.T, backing Backing) {
	t.Helper()
	runChecks(t, backing, recordsChecks)
}

// RunLister runs every check of store.Lister as a subtest, each on a new
// backing store. Every handle backing opens must also be a store.Lister.
func RunLister(t *testing.T, backing Backing) {
	t.Helper()
	runChecks(t, backing, listerChecks)
}

func runChecks(t *testing.T, backing Backing, checks []check) {
	t.Helper()
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if err := runCheck(context.Background(), backing, c); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// RunLiveness runs every check of store.Liveness as a subtest, each on a new
// backing store.
func RunLiveness(t *testing.T, backing LivenessBacking, options ...LivenessOption) {
	t.Helper()
	config := newLivenessConfig(options)
	for _, c := range livenessChecks {
		t.Run(c.name, func(t *testing.T) {
			if err := runLivenessCheck(context.Background(), backing, config, c); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// RunDefinitions runs every check of store.Definitions as a subtest, each on
// a new backing store.
func RunDefinitions(t *testing.T, backing DefinitionsBacking) {
	t.Helper()
	for _, c := range definitionsChecks {
		t.Run(c.name, func(t *testing.T) {
			open, cleanup, err := backing()
			if err != nil {
				t.Fatalf("backing: %v", err)
			}
			if cleanup != nil {
				defer cleanup()
			}
			if err := c.run(context.Background(), open); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// CheckRecords runs every check of store.Records, each on a new backing
// store, and returns the problems it found, named by check. It needs no
// testing package: a tool can check a store at startup.
func CheckRecords(ctx context.Context, backing Backing) []error {
	return problemsOf(ctx, backing, recordsChecks)
}

// CheckLister is CheckRecords for the checks of store.Lister.
func CheckLister(ctx context.Context, backing Backing) []error {
	return problemsOf(ctx, backing, listerChecks)
}

func problemsOf(ctx context.Context, backing Backing, checks []check) []error {
	var problems []error
	for _, c := range checks {
		if err := runCheck(ctx, backing, c); err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", c.name, err))
		}
	}
	return problems
}

// CheckLiveness is CheckRecords for the checks of store.Liveness.
func CheckLiveness(ctx context.Context, backing LivenessBacking, options ...LivenessOption) []error {
	config := newLivenessConfig(options)
	var problems []error
	for _, c := range livenessChecks {
		if err := runLivenessCheck(ctx, backing, config, c); err != nil {
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

func runLivenessCheck(ctx context.Context, backing LivenessBacking, config livenessConfig, c livenessCheck) (err error) {
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
	return c.run(ctx, open, config)
}
