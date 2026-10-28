package storetest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

// checkEmptyEpoch holds a store to the first rule of its epoch: Create and
// Append refuse a RecordInfo that names none, with invalid_argument, and write
// nothing. With none, a writer of a deleted Record could not be told from the
// writer of the Record created again.
func checkEmptyEpoch(ctx context.Context, open Open) error {
	store := open()
	none := summary("r1", 1, "")
	none.RecordEpoch = ""
	if err := store.Create(ctx, "r1", []session.Entry{checkpoint(1, "a")}, none); !gotato.IsCode(err, gotato.CodeInvalidArgument) {
		return failf("Create with an empty RecordEpoch returned %v, want invalid_argument: RecordInfo.RecordEpoch is required", err)
	}
	if _, err := store.Load(ctx, "r1"); !errors.Is(err, session.ErrNotFound) {
		return failf("Load after a refused Create returned %v, want ErrNotFound: %s", err, ruleAtomic)
	}
	if err := store.Create(ctx, "r1", []session.Entry{checkpoint(1, "a")}, summary("r1", 1, "")); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	none = summary("r1", 2, "")
	none.RecordEpoch = ""
	if err := store.Append(ctx, "r1", 1, []session.Entry{fact(2, "a")}, none); !gotato.IsCode(err, gotato.CodeInvalidArgument) {
		return failf("Append with an empty RecordEpoch returned %v, want invalid_argument: RecordInfo.RecordEpoch is required", err)
	}
	log, err := store.Log(ctx, "r1", 0, 0)
	if err != nil || len(log) != 1 {
		return failf("Log after a refused Append returned %v Entries (%v), want 1: %s", len(log), err, ruleAtomic)
	}
	return nil
}

// checkFieldsKept round-trips every field of RecordInfo, Entry and InstanceRow
// through the store, found by reflection: a field added to one of them later
// is filled by this check without a change here, so an adapter that maps the
// fields it knew to columns, and drops a new one, fails.
func checkFieldsKept(ctx context.Context, open Open) error {
	store := open()
	data := []byte(`{"filled":true}`)
	entries := []session.Entry{filledEntry(1, gotato.EventKindSessionCreated, data), filledEntry(2, gotato.EventKindMessageCommitted, data)}
	for i := range entries {
		entry := &entries[i]
		seq, kind := entry.Seq, entry.Kind
		if err := fillAll(reflect.ValueOf(entry).Elem(), "Entry", map[string]bool{"Seq": true, "Kind": true, "Layer": true, "Data": true}); err != nil {
			return err
		}
		entry.Seq, entry.Kind, entry.Layer, entry.Data = seq, kind, session.LayerFact, data
	}
	info := session.RecordInfo{}
	if err := fillAll(reflect.ValueOf(&info).Elem(), "RecordInfo", nil); err != nil {
		return err
	}
	info.ID = "shape"
	if err := store.Create(ctx, "shape", entries[:1], info); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	next := session.RecordInfo{}
	if err := fillAll(reflect.ValueOf(&next).Elem(), "RecordInfo", nil); err != nil {
		return err
	}
	next.ID, next.RecordEpoch = "shape", info.RecordEpoch
	// A second value, so that Append is told from Create.
	next.Messages, next.Runs = info.Messages+1, info.Runs+1
	if err := store.Append(ctx, "shape", 1, entries[1:], next); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	reader := open()
	loaded, err := reader.Load(ctx, "shape")
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if diff := differ(reflect.ValueOf(loaded.Info), reflect.ValueOf(next), "Load.Info"); diff != "" {
		return failf("%s: a store must keep every field of the RecordInfo of the last write", diff)
	}
	list, err := reader.List(ctx)
	if err != nil || len(list) != 1 {
		return failf("List returned %d Records (%v), want 1", len(list), err)
	}
	if diff := differ(reflect.ValueOf(list[0]), reflect.ValueOf(next), "List"); diff != "" {
		return failf("%s: a store must keep every field of the RecordInfo of the last write", diff)
	}
	log, err := reader.Log(ctx, "shape", 0, 0)
	if err != nil {
		return fmt.Errorf("log: %w", err)
	}
	if len(log) != len(entries) {
		return failf("Log returned %d Entries, want %d", len(log), len(entries))
	}
	for i := range entries {
		if diff := differ(reflect.ValueOf(log[i]), reflect.ValueOf(entries[i]), fmt.Sprintf("Log[%d]", i)); diff != "" {
			return failf("%s: a store must keep every field of an Entry", diff)
		}
	}
	if len(loaded.Facts) != len(entries) {
		return failf("Load returned the facts %v, want %v", seqs(loaded.Facts), seqs(entries))
	}
	for i := range entries {
		if diff := differ(reflect.ValueOf(loaded.Facts[i]), reflect.ValueOf(entries[i]), fmt.Sprintf("Load.Facts[%d]", i)); diff != "" {
			return failf("%s: a store must keep every field of an Entry", diff)
		}
	}
	row := session.InstanceRow{}
	if err := fillAll(reflect.ValueOf(&row).Elem(), "InstanceRow", nil); err != nil {
		return err
	}
	if err := store.Beat(ctx, row, time.Hour); err != nil {
		return fmt.Errorf("beat: %w", err)
	}
	state, err := open().Instance(ctx, row.ID)
	if err != nil || !state.Alive {
		return failf("Instance after Beat returned %+v, %v, want alive", state, err)
	}
	got, want := reflect.ValueOf(state), reflect.ValueOf(row)
	for i := range want.NumField() {
		name := want.Type().Field(i).Name
		field := got.FieldByName(name)
		if !field.IsValid() {
			return failf("InstanceState has no field %s, but InstanceRow has: add it, so that Instance returns what Beat stored", name)
		}
		if diff := differ(field, want.Field(i), "Instance."+name); diff != "" {
			return failf("%s: Beat must store every field of the InstanceRow", diff)
		}
	}
	return nil
}

// filledEntry is an Entry at seq of kind, a fact.
func filledEntry(seq uint64, kind gotato.EventKind, data []byte) session.Entry {
	return session.Entry{Seq: seq, Kind: kind, Layer: session.LayerFact, Data: data}
}

// fillAll sets every exported field of v, except the ones in keep, to a
// distinctive value that is not the field's zero value. A field of a type it
// does not know is an error, so a new kind of field is never skipped quietly.
func fillAll(v reflect.Value, path string, keep map[string]bool) error {
	counter := 0
	return fill(v, path, keep, &counter)
}

var timeType = reflect.TypeFor[time.Time]()

func fill(v reflect.Value, path string, keep map[string]bool, counter *int) error {
	*counter++
	n := *counter
	switch v.Kind() {
	case reflect.String:
		v.SetString(fmt.Sprintf("%s-%d", path, n))
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(n) + 100)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(n) + 100)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(float64(n) + 0.5)
	case reflect.Struct:
		if v.Type() == timeType {
			v.Set(reflect.ValueOf(time.Date(2026, 10, 17, 1, 2, n%60, 123000000, time.UTC)))
			return nil
		}
		for i := range v.NumField() {
			field := v.Type().Field(i)
			if !field.IsExported() || keep[field.Name] {
				continue
			}
			if err := fill(v.Field(i), path+"."+field.Name, nil, counter); err != nil {
				return err
			}
		}
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String || v.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("storetest: the field %s is a %s, which the field check cannot fill: teach it, so that the new field is checked", path, v.Type())
		}
		v.Set(reflect.MakeMap(v.Type()))
		v.SetMapIndex(reflect.ValueOf(fmt.Sprintf("key-%d", n)).Convert(v.Type().Key()), reflect.ValueOf(fmt.Sprintf("value-%d", n)).Convert(v.Type().Elem()))
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		return fill(v.Index(0), path+"[0]", nil, counter)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		return fill(v.Elem(), path, nil, counter)
	default:
		return fmt.Errorf("storetest: the field %s is a %s, which the field check cannot fill: teach it, so that the new field is checked", path, v.Type())
	}
	return nil
}

// differ names the first field in which got differs from want, or returns "".
// Times are equal when they are the same instant.
func differ(got, want reflect.Value, path string) string {
	switch want.Kind() {
	case reflect.Struct:
		if want.Type() == timeType {
			if !got.Interface().(time.Time).Equal(want.Interface().(time.Time)) {
				return fmt.Sprintf("%s = %v, want %v", path, got.Interface(), want.Interface())
			}
			return ""
		}
		for i := range want.NumField() {
			if !want.Type().Field(i).IsExported() {
				continue
			}
			if diff := differ(got.Field(i), want.Field(i), path+"."+want.Type().Field(i).Name); diff != "" {
				return diff
			}
		}
	case reflect.Slice:
		if got.Len() != want.Len() {
			return fmt.Sprintf("%s has %d items, want %d", path, got.Len(), want.Len())
		}
		for i := range want.Len() {
			if diff := differ(got.Index(i), want.Index(i), fmt.Sprintf("%s[%d]", path, i)); diff != "" {
				return diff
			}
		}
	case reflect.Map:
		if got.Len() != want.Len() {
			return fmt.Sprintf("%s has %d items, want %d", path, got.Len(), want.Len())
		}
		for _, key := range want.MapKeys() {
			if !got.MapIndex(key).IsValid() || !reflect.DeepEqual(got.MapIndex(key).Interface(), want.MapIndex(key).Interface()) {
				return fmt.Sprintf("%s lost the key %v", path, key)
			}
		}
	case reflect.Pointer:
		if got.IsNil() != want.IsNil() {
			return fmt.Sprintf("%s = %v, want %v", path, got.Interface(), want.Interface())
		}
		if !want.IsNil() {
			return differ(got.Elem(), want.Elem(), path)
		}
	default:
		if !reflect.DeepEqual(got.Interface(), want.Interface()) {
			return fmt.Sprintf("%s = %v, want %v", path, got.Interface(), want.Interface())
		}
	}
	return ""
}
