package storetest

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

func checkDelete(ctx context.Context, open Open) error {
	store := open()
	if _, err := create(ctx, store, "r1"); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	if err := open().Delete(ctx, "r1"); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	if _, err := store.Load(ctx, "r1"); !errors.Is(err, record.ErrNotFound) {
		return failf("Load after Delete returned %v, want ErrNotFound", err)
	}
	if _, err := store.Log(ctx, "r1", 0, 0); !errors.Is(err, record.ErrNotFound) {
		return failf("Log after Delete returned %v, want ErrNotFound: Delete must remove the whole Record", err)
	}
	if err := store.Append(ctx, "r1", 3, []record.Entry{fact(4, "a")}, header("r1", 4, "")); !errors.Is(err, record.ErrNotFound) {
		return failf("Append after Delete returned %v, want ErrNotFound", err)
	}
	if err := store.Delete(ctx, "r1"); !errors.Is(err, record.ErrNotFound) {
		return fmt.Errorf("a second Delete returned %v, want ErrNotFound: Delete of a missing Record is an error, not a no-op", err)
	}
	return nil
}

// checkInvalidID holds a store to one rule for an ID it cannot keep: an empty
// one, one with a path separator, a "..", a leading ".", a control character,
// invalid UTF-8, or more than 100 bytes (session.CheckID). A store may refuse
// more IDs, but not fewer. Every call that takes it fails with
// ErrInvalidID (invalid_argument), never with ErrNotFound, so a Session fails
// the same way on every store.
func checkInvalidID(ctx context.Context, open Open) error {
	store := open()
	for _, id := range []string{"", "a/b", `a\b`, "..", "a..b", ".hidden", "a\x00b", "a\nb", "a\xffb", strings.Repeat("x", 101), strings.Repeat("A", 101)} {
		_, loadErr := store.Load(ctx, id)
		_, logErr := store.Log(ctx, id, 0, 0)
		calls := []error{
			store.Create(ctx, id, []record.Entry{checkpoint(1, "a")}, header(id, 1, "")),
			store.Append(ctx, id, 1, []record.Entry{fact(2, "a")}, header(id, 2, "")),
			loadErr,
			logErr,
			store.Delete(ctx, id),
		}
		for i, err := range calls {
			if !errors.Is(err, session.ErrInvalidID) || !gotato.IsCode(err, gotato.CodeInvalidArgument) {
				return fmt.Errorf("call %d with the ID %q = %v, want ErrInvalidID (invalid_argument): call session.CheckID first", i, id, err)
			}
		}
	}
	return nil
}

// checkIDCase checks that IDs differ by their characters alone: "Case" and
// "case" are two Records whatever the case rules of a file system, and so are
// the two spellings of one accented letter.
func checkIDCase(ctx context.Context, open Open) error {
	store := open()
	ids := []string{"Case", "case", "!case", "caf\u00e9", "cafe\u0301"}
	for _, id := range ids {
		if _, err := store.Load(ctx, id); !errors.Is(err, record.ErrNotFound) {
			return fmt.Errorf("load of %q before it exists = %v, want ErrNotFound", id, err)
		}
		if err := store.Create(ctx, id, []record.Entry{checkpoint(1, id)}, header(id, 1, "")); err != nil {
			return fmt.Errorf("create %q: %w", id, err)
		}
	}
	for _, id := range ids {
		log, err := open().Log(ctx, id, 0, 0)
		if err != nil {
			return fmt.Errorf("log of %q: %w", id, err)
		}
		if err := sameEntries(fmt.Sprintf("Log of %q", id), log, []record.Entry{checkpoint(1, id)}, ruleLog); err != nil {
			return err
		}
	}
	if err := store.Delete(ctx, "case"); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	if _, err := store.Load(ctx, "case"); !errors.Is(err, record.ErrNotFound) {
		return fmt.Errorf("load of %q after deleting it = %v, want ErrNotFound", "case", err)
	}
	if _, err := store.Load(ctx, "Case"); err != nil {
		return fmt.Errorf("load of %q after deleting %q: %w", "Case", "case", err)
	}
	return nil
}

func checkUnknown(ctx context.Context, open Open) error {
	store := open()
	if _, err := store.Load(ctx, "missing"); !errors.Is(err, record.ErrNotFound) {
		return failf("Load of an unknown ID returned %v, want ErrNotFound", err)
	}
	if _, err := store.Log(ctx, "missing", 0, 0); !errors.Is(err, record.ErrNotFound) {
		return failf("Log of an unknown ID returned %v, want ErrNotFound", err)
	}
	if err := store.Append(ctx, "missing", 1, []record.Entry{fact(2, "a")}, header("missing", 2, "")); !errors.Is(err, record.ErrNotFound) {
		return failf("Append to an unknown ID returned %v, want ErrNotFound", err)
	}
	return nil
}

func checkCanceled(ctx context.Context, open Open) error {
	store := open()
	if _, err := create(ctx, store, "r1"); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.Append(canceled, "r1", 3, []record.Entry{fact(4, "a")}, header("r1", 4, "")); err == nil {
		return failf("Append with a canceled context succeeded, want an error: every call must honor ctx")
	}
	if _, err := store.Load(canceled, "r1"); err == nil {
		return failf("Load with a canceled context succeeded, want an error: every call must honor ctx")
	}
	loaded, err := store.Load(ctx, "r1")
	if err != nil || loaded.Seq != 3 {
		return fmt.Errorf("after an Append with a canceled context the position is %d (%v), want 3: a canceled call must write nothing", loaded.Seq, err)
	}
	return nil
}

// checkDeleteIsolated checks that Delete of one Record leaves the others, also
// the ones whose ID starts with its ID, and that a Record created again under
// the ID starts empty.
func checkDeleteIsolated(ctx context.Context, open Open) error {
	for _, id := range []string{"a", "ab", "a-b"} {
		if _, err := create(ctx, open(), id); err != nil {
			return fmt.Errorf("create %q: %w", id, err)
		}
	}
	if err := open().Delete(ctx, "a"); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	for _, id := range []string{"ab", "a-b"} {
		loaded, err := open().Load(ctx, id)
		if err != nil || loaded.Seq != 3 {
			return failf("Load of %q after Delete of \"a\" returned Seq %d, %v, want the Record whole: Delete removes one Record, not the IDs that start with its ID", id, loaded.Seq, err)
		}
	}
	fresh := header("a", 1, "2026-10-17T00:00:09.000Z")
	fresh.Epoch = "epoch-again"
	if err := open().Create(ctx, "a", []record.Entry{checkpoint(1, "again")}, fresh); err != nil {
		return failf("Create of \"a\" after its Delete returned %v, want nil", err)
	}
	if got, err := open().Log(ctx, "a", 0, 0); err != nil || len(got) != 1 {
		return failf("Log of a Record created again returned %d Entries, %v, want 1: nothing of the deleted Record is left", len(got), err)
	}
	return nil
}
