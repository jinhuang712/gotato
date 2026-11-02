package storetest

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"github.com/jinhuang712/gotato/session"
)

const (
	ruleListOrder = "List must return the last RecordInfo of each Record, the most recently updated first (UpdatedAt), ties by ID"
	ruleListPages = "List must return each Record once across the pages of a walk: a page holds at most Limit Infos, Next is the Cursor of the page after it, and the last page names none"
)

func checkList(ctx context.Context, open Open) error {
	store, lister, err := listerOf(open)
	if err != nil {
		return err
	}
	for _, c := range []struct{ id, updated string }{
		{"older", "2026-10-17T00:00:01.000Z"},
		{"newer", "2026-10-17T00:00:02.000Z"},
		{"tie-b", "2026-10-17T00:00:01.500Z"},
		{"tie-a", "2026-10-17T00:00:01.500Z"},
	} {
		if err := store.Create(ctx, c.id, []session.Entry{checkpoint(1, "a")}, header(c.id, 1, c.updated)); err != nil {
			return fmt.Errorf("create: %w", err)
		}
	}
	if err := store.Append(ctx, "older", 1, []session.Entry{fact(2, "a")}, header("older", 2, "2026-10-17T00:00:03.000Z")); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	list, err := listAll(ctx, lister, 0)
	if err != nil {
		return err
	}
	if got, want := idsOf(list), []string{"older", "newer", "tie-a", "tie-b"}; !slices.Equal(got, want) {
		return failf("List returned %v, want %v: %s", got, want, ruleListOrder)
	}
	if list[0].Messages != 2 || !list[0].UpdatedAt.Equal(infoTime("2026-10-17T00:00:03.000Z")) {
		return failf("List returned %+v for the Record appended to, want the Info of its last write (Messages 2): %s", list[0], ruleListOrder)
	}
	first, err := lister.List(ctx, session.ListQuery{})
	if err != nil || len(first.Infos) == 0 || first.Infos[0].ID != "older" {
		return failf("List with no Limit returned %+v, %v, want a first page that starts with the Record updated last", first, err)
	}
	return nil
}

func checkListPaging(ctx context.Context, open Open) error {
	store, lister, err := listerOf(open)
	if err != nil {
		return err
	}
	var want []string
	for i := range 7 {
		id := fmt.Sprintf("r%d", i)
		// Two Records share an update time, so the cursor must break ties.
		second := 9 - i
		if i == 3 {
			second = 9 - 2
		}
		updated := fmt.Sprintf("2026-10-17T00:00:%02d.000Z", second)
		if err := store.Create(ctx, id, []session.Entry{checkpoint(1, "a")}, header(id, 1, updated)); err != nil {
			return fmt.Errorf("create: %w", err)
		}
	}
	all, err := listAll(ctx, lister, 0)
	if err != nil {
		return err
	}
	want = idsOf(all)
	if len(want) != 7 {
		return failf("List returned %d Records (%v), want 7: %s", len(want), want, ruleListPages)
	}
	for _, limit := range []int{1, 2, 3, 6, 7, 100} {
		got, err := listAll(ctx, lister, limit)
		if err != nil {
			return err
		}
		if !slices.Equal(idsOf(got), want) {
			return failf("List walked with Limit %d returned %v, want %v: %s", limit, idsOf(got), want, ruleListPages)
		}
	}
	// A Record updated while a caller pages may repeat or be skipped, but a
	// walk still ends, and the Records it did not touch are each seen once.
	page, err := lister.List(ctx, session.ListQuery{Limit: 2})
	if err != nil {
		return fmt.Errorf("list: %w", err)
	}
	if page.Next == "" {
		return failf("List with Limit 2 of 7 Records returned Next %q, want a Cursor: %s", page.Next, ruleListPages)
	}
	return nil
}

func checkListBadCursor(ctx context.Context, open Open) error {
	store, lister, err := listerOf(open)
	if err != nil {
		return err
	}
	if _, err := create(ctx, store, "r1"); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	if page, err := lister.List(ctx, session.ListQuery{Cursor: "%%% not a cursor %%%"}); err == nil {
		return failf("List with a Cursor the store did not make returned %+v and no error, want an error (invalid_argument is best)", page)
	}
	return nil
}

// checkListInfoKept round-trips every field of RecordInfo, found by
// reflection, through Create and Append to the listing.
func checkListInfoKept(ctx context.Context, open Open) error {
	store, lister, err := listerOf(open)
	if err != nil {
		return err
	}
	filled := func() (session.Header, error) {
		h := session.Header{Epoch: "epoch-shape"}
		if err := fillAll(reflect.ValueOf(&h.Info).Elem(), "RecordInfo", nil); err != nil {
			return h, err
		}
		h.Info.ID = "shape"
		return h, nil
	}
	first, err := filled()
	if err != nil {
		return err
	}
	if err := store.Create(ctx, "shape", []session.Entry{checkpoint(1, "a")}, first); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	next, err := filled()
	if err != nil {
		return err
	}
	// A second value, so that Append is told from Create.
	next.Info.Messages, next.Info.Runs = first.Info.Messages+1, first.Info.Runs+1
	if err := store.Append(ctx, "shape", 1, []session.Entry{fact(2, "a")}, next); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	list, err := listAll(ctx, lister, 0)
	if err != nil {
		return err
	}
	if len(list) != 1 {
		return failf("List returned %d Records (%v), want 1", len(list), idsOf(list))
	}
	if diff := differ(reflect.ValueOf(list[0]), reflect.ValueOf(next.Info), "List"); diff != "" {
		return failf("%s: List must return every field of the RecordInfo of the last write", diff)
	}
	return nil
}

// checkListAfterDelete checks that Delete takes a Record out of the listing,
// and that IDs that differ only in case or Unicode form are listed apart.
func checkListAfterDelete(ctx context.Context, open Open) error {
	store, lister, err := listerOf(open)
	if err != nil {
		return err
	}
	ids := []string{"Case", "case", "!case", "caf\u00e9", "cafe\u0301"}
	for _, id := range ids {
		if err := store.Create(ctx, id, []session.Entry{checkpoint(1, id)}, header(id, 1, "")); err != nil {
			return fmt.Errorf("create %q: %w", id, err)
		}
	}
	if err := store.Delete(ctx, "case"); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	list, err := listAll(ctx, lister, 0)
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
	return nil
}
