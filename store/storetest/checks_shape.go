package storetest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/record"
	"github.com/jinhuang712/gotato/store"
)

// checkEmptyEpoch holds a store to the first rule of its epoch: Create and
// Append refuse a Header that names none, with invalid_argument, and write
// nothing. With none, a writer of a deleted Record could not be told from the
// writer of the Record created again.
func checkEmptyEpoch(ctx context.Context, open Open) error {
	store := open()
	none := header("r1", 1, "")
	none.Epoch = ""
	if err := store.Create(ctx, "r1", []record.Entry{checkpoint(1, "a")}, none); !gotato.IsCode(err, gotato.CodeInvalidArgument) {
		return failf("Create with an empty Epoch returned %v, want invalid_argument: Header.Epoch is required", err)
	}
	if _, err := store.Load(ctx, "r1"); !errors.Is(err, record.ErrNotFound) {
		return failf("Load after a refused Create returned %v, want ErrNotFound: %s", err, ruleAtomic)
	}
	if err := store.Create(ctx, "r1", []record.Entry{checkpoint(1, "a")}, header("r1", 1, "")); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	none = header("r1", 2, "")
	none.Epoch = ""
	if err := store.Append(ctx, "r1", 1, []record.Entry{fact(2, "a")}, none); !gotato.IsCode(err, gotato.CodeInvalidArgument) {
		return failf("Append with an empty Epoch returned %v, want invalid_argument: Header.Epoch is required", err)
	}
	log, err := store.Log(ctx, "r1", 0, 0)
	if err != nil || len(log) != 1 {
		return failf("Log after a refused Append returned %v Entries (%v), want 1: %s", len(log), err, ruleAtomic)
	}
	return nil
}

// checkFieldsKept round-trips every field of Header (Info included) and
// Entry through the store, found by reflection, after Create and after
// Append: a field added to one of them later is filled by this check without a
// change here, so an adapter that maps the fields it knew to columns, and
// drops a new one, fails.
func checkFieldsKept(ctx context.Context, open Open) error {
	store := open()
	entries, err := filledEntries()
	if err != nil {
		return err
	}
	next, err := writeShape(ctx, store, open, entries)
	if err != nil {
		return err
	}
	return checkShapeRead(ctx, open(), entries, next)
}

// filledEntries returns the two Entries of the Record "shape", every field
// filled but Seq, Kind, Layer and Data.
func filledEntries() ([]record.Entry, error) {
	data := []byte(`{"filled":true}`)
	entries := []record.Entry{filledEntry(1, gotato.EventKindSessionCreated, data), filledEntry(2, gotato.EventKindMessageCommitted, data)}
	for i := range entries {
		entry := &entries[i]
		seq, kind := entry.Seq, entry.Kind
		if err := fillAll(reflect.ValueOf(entry).Elem(), "Entry", map[string]bool{"Seq": true, "Kind": true, "Layer": true, "Data": true}); err != nil {
			return nil, err
		}
		entry.Seq, entry.Kind, entry.Layer, entry.Data = seq, kind, record.LayerFact, data
	}
	return entries, nil
}

// filledHeader is a Header of the Record "shape" with every field filled.
func filledHeader() (record.Header, error) {
	h := record.Header{}
	if err := fillAll(reflect.ValueOf(&h).Elem(), "Header", nil); err != nil {
		return h, err
	}
	h.Info.ID = "shape"
	return h, nil
}

// writeShape creates the Record "shape" with the first Entry and a filled
// Header, checks the Header on a new handle, appends the rest with a second
// Header, and returns that second one.
func writeShape(ctx context.Context, store store.Records, open Open, entries []record.Entry) (record.Header, error) {
	first, err := filledHeader()
	if err != nil {
		return record.Header{}, err
	}
	if err := store.Create(ctx, "shape", entries[:1], first); err != nil {
		return record.Header{}, fmt.Errorf("create: %w", err)
	}
	created, err := open().Load(ctx, "shape")
	if err != nil {
		return record.Header{}, fmt.Errorf("load: %w", err)
	}
	if diff := differ(reflect.ValueOf(created.Header), reflect.ValueOf(first), "Load.Header after Create"); diff != "" {
		return record.Header{}, failf("%s: a store must keep every field of the Header of the last write", diff)
	}
	next, err := filledHeader()
	if err != nil {
		return record.Header{}, err
	}
	next.Epoch = first.Epoch
	// A second value, so that Append is told from Create.
	next.Info.Messages, next.Info.Runs = first.Info.Messages+1, first.Info.Runs+1
	if err := store.Append(ctx, "shape", 1, entries[1:], next); err != nil {
		return record.Header{}, fmt.Errorf("append: %w", err)
	}
	return next, nil
}

// checkShapeRead reads the Record "shape" back through Load and Log and checks
// that every field of next and of entries was kept.
func checkShapeRead(ctx context.Context, reader store.Records, entries []record.Entry, next record.Header) error {
	loaded, err := reader.Load(ctx, "shape")
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	if diff := differ(reflect.ValueOf(loaded.Header), reflect.ValueOf(next), "Load.Header"); diff != "" {
		return failf("%s: a store must keep every field of the Header of the last write", diff)
	}
	log, err := reader.Log(ctx, "shape", 0, 0)
	if err != nil {
		return fmt.Errorf("log: %w", err)
	}
	if len(log) != len(entries) {
		return failf("Log returned %d Entries, want %d", len(log), len(entries))
	}
	if diff := differEntries(log, entries, "Log"); diff != "" {
		return failf("%s: a store must keep every field of an Entry", diff)
	}
	if len(loaded.Entries) != len(entries) {
		return failf("Load returned the Entries %v, want %v", seqs(loaded.Entries), seqs(entries))
	}
	if diff := differEntries(loaded.Entries, entries, "Load.Entries"); diff != "" {
		return failf("%s: a store must keep every field of an Entry", diff)
	}
	return nil
}

// differEntries is differ for each Entry of want, named path[i].
func differEntries(got, want []record.Entry, path string) string {
	for i := range want {
		if diff := differ(reflect.ValueOf(got[i]), reflect.ValueOf(want[i]), fmt.Sprintf("%s[%d]", path, i)); diff != "" {
			return diff
		}
	}
	return ""
}

// filledEntry is an Entry at seq of kind, a fact.
func filledEntry(seq uint64, kind gotato.EventKind, data []byte) record.Entry {
	return record.Entry{Seq: seq, Kind: kind, Layer: record.LayerFact, Data: data}
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
		return differStruct(got, want, path)
	case reflect.Slice:
		return differSlice(got, want, path)
	case reflect.Map:
		return differMap(got, want, path)
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

// differStruct is differ for a struct: a time by its instant, any other
// struct by its exported fields in order.
func differStruct(got, want reflect.Value, path string) string {
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
	return ""
}

// differSlice is differ for a slice: its length, then each item in order.
func differSlice(got, want reflect.Value, path string) string {
	if got.Len() != want.Len() {
		return fmt.Sprintf("%s has %d items, want %d", path, got.Len(), want.Len())
	}
	for i := range want.Len() {
		if diff := differ(got.Index(i), want.Index(i), fmt.Sprintf("%s[%d]", path, i)); diff != "" {
			return diff
		}
	}
	return ""
}

// differMap is differ for a map: its length, then each key of want.
func differMap(got, want reflect.Value, path string) string {
	if got.Len() != want.Len() {
		return fmt.Sprintf("%s has %d items, want %d", path, got.Len(), want.Len())
	}
	for _, key := range want.MapKeys() {
		if !got.MapIndex(key).IsValid() || !reflect.DeepEqual(got.MapIndex(key).Interface(), want.MapIndex(key).Interface()) {
			return fmt.Sprintf("%s lost the key %v", path, key)
		}
	}
	return ""
}
