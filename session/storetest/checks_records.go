package storetest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

func checkList(ctx context.Context, open Open) error {
	store := open()
	if err := store.Create(ctx, "older", []session.Entry{checkpoint(1, "a")}, summary("older", 1, "2026-10-17T00:00:01.000Z")); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	if err := store.Create(ctx, "newer", []session.Entry{checkpoint(1, "a")}, summary("newer", 1, "2026-10-17T00:00:02.000Z")); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	if err := store.Append(ctx, "older", 1, []session.Entry{fact(2, "a")}, summary("older", 2, "2026-10-17T00:00:03.000Z")); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	list, err := open().List(ctx)
	if err != nil {
		return fmt.Errorf("list: %w", err)
	}
	if len(list) != 2 || list[0].ID != "older" || list[0].Seq != 2 || list[1].ID != "newer" {
		return failf("List returned %+v, want the last RecordInfo of each Record (older at Seq 2, newer), the most recently updated first (UpdatedAt)", list)
	}
	return nil
}

func checkDelete(ctx context.Context, open Open) error {
	store := open()
	if _, err := create(ctx, store, "r1"); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	if err := open().Delete(ctx, "r1"); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	if _, err := store.Load(ctx, "r1"); !errors.Is(err, session.ErrNotFound) {
		return failf("Load after Delete returned %v, want ErrNotFound", err)
	}
	if err := store.Append(ctx, "r1", 3, []session.Entry{fact(4, "a")}, summary("r1", 4, "")); !errors.Is(err, session.ErrNotFound) {
		return failf("Append after Delete returned %v, want ErrNotFound", err)
	}
	if err := store.Delete(ctx, "r1"); !errors.Is(err, session.ErrNotFound) {
		return fmt.Errorf("a second Delete returned %v, want ErrNotFound: Delete of a missing Record is an error, not a no-op", err)
	}
	list, err := store.List(ctx)
	if err != nil || len(list) != 0 {
		return failf("List after Delete returned %+v, %v, want no Record: Delete must remove the whole Record", list, err)
	}
	return nil
}

// checkInvalidID holds a store to one rule for an ID it cannot keep: an empty
// one, one with a path separator, a "..", a leading ".", a control character,
// invalid UTF-8, more than 100 bytes, or a file name (session.MaxNameBytes)
// longer than 200 bytes once it is encoded: 33 CJK characters are 99 bytes
// and 297 bytes of name. Every call that takes it fails with
// ErrInvalidID (invalid_argument), never with ErrNotFound, so a Session fails
// the same way on every store.
func checkInvalidID(ctx context.Context, open Open) error {
	store := open()
	for _, id := range []string{"", "a/b", `a\b`, "..", "a..b", ".hidden", "a\x00b", "a\nb", "a\xffb", strings.Repeat("x", 101), strings.Repeat("会", 33), strings.Repeat("A", 101)} {
		_, loadErr := store.Load(ctx, id)
		_, logErr := store.Log(ctx, id, 0, 0)
		calls := []error{
			store.Create(ctx, id, []session.Entry{checkpoint(1, "a")}, summary(id, 1, "")),
			store.Append(ctx, id, 1, []session.Entry{fact(2, "a")}, summary(id, 2, "")),
			loadErr,
			logErr,
			store.Delete(ctx, id),
			store.Beat(ctx, session.InstanceRow{ID: id}, time.Hour),
		}
		for i, err := range calls {
			if !errors.Is(err, session.ErrInvalidID) || !gotato.IsCode(err, gotato.CodeInvalidArgument) {
				return fmt.Errorf("call %d with the ID %q = %v, want ErrInvalidID (invalid_argument)", i, id, err)
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
	ids := []string{"Case", "case", "!case", "café", "café"}
	for _, id := range ids {
		if _, err := store.Load(ctx, id); !errors.Is(err, session.ErrNotFound) {
			return fmt.Errorf("load of %q before it exists = %v, want ErrNotFound", id, err)
		}
		if err := store.Create(ctx, id, []session.Entry{checkpoint(1, id)}, summary(id, 1, "")); err != nil {
			return fmt.Errorf("create %q: %w", id, err)
		}
	}
	for _, id := range ids {
		log, err := open().Log(ctx, id, 0, 0)
		if err != nil {
			return fmt.Errorf("log of %q: %w", id, err)
		}
		if err := sameEntries(fmt.Sprintf("Log of %q", id), log, []session.Entry{checkpoint(1, id)}, ruleLog); err != nil {
			return err
		}
	}
	if err := store.Delete(ctx, "case"); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	list, err := store.List(ctx)
	if err != nil || len(list) != len(ids)-1 {
		return fmt.Errorf("list after deleting one of %d = %d Records, %v", len(ids), len(list), err)
	}
	listed := map[string]bool{}
	for _, info := range list {
		listed[info.ID] = true
	}
	if listed["case"] || !listed["Case"] {
		return fmt.Errorf("list after deleting %q = %v", "case", listed)
	}
	if _, err := store.Load(ctx, "Case"); err != nil {
		return fmt.Errorf("load of %q after deleting %q: %w", "Case", "case", err)
	}
	return nil
}

func checkUnknown(ctx context.Context, open Open) error {
	store := open()
	if _, err := store.Load(ctx, "missing"); !errors.Is(err, session.ErrNotFound) {
		return failf("Load of an unknown ID returned %v, want ErrNotFound", err)
	}
	if _, err := store.Log(ctx, "missing", 0, 0); !errors.Is(err, session.ErrNotFound) {
		return failf("Log of an unknown ID returned %v, want ErrNotFound", err)
	}
	if err := store.Append(ctx, "missing", 1, []session.Entry{fact(2, "a")}, summary("missing", 2, "")); !errors.Is(err, session.ErrNotFound) {
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
	if err := store.Append(canceled, "r1", 3, []session.Entry{fact(4, "a")}, summary("r1", 4, "")); err == nil {
		return failf("Append with a canceled context succeeded, want an error: every call must honor ctx")
	}
	if _, err := store.Load(canceled, "r1"); err == nil {
		return failf("Load with a canceled context succeeded, want an error: every call must honor ctx")
	}
	loaded, err := store.Load(ctx, "r1")
	if err != nil || loaded.Head != 3 {
		return fmt.Errorf("after an Append with a canceled context the head is %d (%v), want 3: a canceled call must write nothing", loaded.Head, err)
	}
	return nil
}
