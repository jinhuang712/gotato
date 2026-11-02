package storetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/session"
)

func checkDefinitionRoundTrip(ctx context.Context, open OpenDefinitions) error {
	store := open()
	for _, doc := range [][]byte{[]byte(`{"a":1}`), {0x00, 0xff, 0xfe, '\n', ' '}, []byte("not json"), {}} {
		if err := store.SaveDefinition(ctx, "kind", "name", doc); err != nil {
			return fmt.Errorf("save: %w", err)
		}
		got, err := store.LoadDefinition(ctx, "kind", "name")
		if err != nil || !bytes.Equal(got, doc) {
			return failf("LoadDefinition after SaveDefinition of %q returned %q, %v: a store keeps the bytes exactly as written", doc, got, err)
		}
	}
	return nil
}

func checkDefinitionNotFound(ctx context.Context, open OpenDefinitions) error {
	store := open()
	if _, err := store.LoadDefinition(ctx, "kind", "unknown"); !errors.Is(err, record.ErrNotFound) {
		return failf("LoadDefinition of an unknown key returned %v, want ErrNotFound", err)
	}
	if err := store.SaveDefinition(ctx, "kind", "known", []byte("x")); err != nil {
		return fmt.Errorf("save: %w", err)
	}
	if _, err := store.LoadDefinition(ctx, "other", "known"); !errors.Is(err, record.ErrNotFound) {
		return failf("LoadDefinition of a known name under another kind returned %v, want ErrNotFound", err)
	}
	return nil
}

func checkDefinitionKeysIndependent(ctx context.Context, open OpenDefinitions) error {
	store := open()
	keys := [][2]string{{"a", "b"}, {"b", "a"}, {"a", "a"}, {"c", "b"}, {"A", "b"}}
	for _, k := range keys {
		if err := store.SaveDefinition(ctx, k[0], k[1], []byte(k[0]+"/"+k[1])); err != nil {
			return fmt.Errorf("save: %w", err)
		}
	}
	for _, k := range keys {
		got, err := store.LoadDefinition(ctx, k[0], k[1])
		if err != nil || string(got) != k[0]+"/"+k[1] {
			return failf("LoadDefinition(%q, %q) returned %q, %v: kind and name are independent parts of the key", k[0], k[1], got, err)
		}
	}
	return nil
}

func checkDefinitionReplaces(ctx context.Context, open OpenDefinitions) error {
	store := open()
	for _, doc := range []string{"a longer first document", "second"} {
		if err := store.SaveDefinition(ctx, "kind", "name", []byte(doc)); err != nil {
			return fmt.Errorf("save: %w", err)
		}
	}
	if got, err := store.LoadDefinition(ctx, "kind", "name"); err != nil || string(got) != "second" {
		return failf("LoadDefinition after two saves returned %q, %v, want the second document only", got, err)
	}
	return nil
}

func checkDefinitionHandlesShare(ctx context.Context, open OpenDefinitions) error {
	if err := open().SaveDefinition(ctx, "kind", "name", []byte("shared")); err != nil {
		return fmt.Errorf("save: %w", err)
	}
	if got, err := open().LoadDefinition(ctx, "kind", "name"); err != nil || string(got) != "shared" {
		return failf("a second handle loaded %q, %v, want what the first saved", got, err)
	}
	return nil
}

func checkDefinitionNotAliased(ctx context.Context, open OpenDefinitions) error {
	store := open()
	doc := []byte("original")
	if err := store.SaveDefinition(ctx, "kind", "name", doc); err != nil {
		return fmt.Errorf("save: %w", err)
	}
	doc[0] = 'X'
	got, err := store.LoadDefinition(ctx, "kind", "name")
	if err != nil || string(got) != "original" {
		return failf("LoadDefinition after the caller changed the saved bytes returned %q, %v: copy the document in", got, err)
	}
	got[0] = 'Y'
	if again, err := store.LoadDefinition(ctx, "kind", "name"); err != nil || string(again) != "original" {
		return failf("LoadDefinition after the caller changed a loaded document returned %q, %v: copy the document out", again, err)
	}
	return nil
}

func checkDefinitionInvalidKey(ctx context.Context, open OpenDefinitions) error {
	store := open()
	bad := []string{"", "a/b", `a\b`, "a\x00b", "a\nb", "..", "a..b", ".hidden", strings.Repeat("x", session.MaxDefinitionKeyBytes+1), "\xff"}
	for _, key := range bad {
		for _, k := range [][2]string{{key, "ok"}, {"ok", key}} {
			err := store.SaveDefinition(ctx, k[0], k[1], []byte("x"))
			if !errors.Is(err, session.ErrInvalidID) || !gotato.IsCode(err, gotato.CodeInvalidArgument) {
				return failf("SaveDefinition(%q, %q) = %v, want ErrInvalidID (invalid_argument): call session.CheckDefinitionKey first", k[0], k[1], err)
			}
			if _, err := store.LoadDefinition(ctx, k[0], k[1]); !errors.Is(err, session.ErrInvalidID) {
				return failf("LoadDefinition(%q, %q) returned %v, want ErrInvalidID: call session.CheckDefinitionKey first", k[0], k[1], err)
			}
		}
	}
	return nil
}

func checkDefinitionConcurrentSaves(ctx context.Context, open OpenDefinitions) error {
	docs := make([][]byte, 8)
	for i := range docs {
		docs[i] = bytes.Repeat([]byte{byte('a' + i)}, 64<<10)
	}
	var wg sync.WaitGroup
	errs := make([]error, len(docs))
	for i, doc := range docs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = open().SaveDefinition(ctx, "kind", "name", doc)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return fmt.Errorf("concurrent save: %w", err)
		}
	}
	got, err := open().LoadDefinition(ctx, "kind", "name")
	if err != nil {
		return fmt.Errorf("load: %w", err)
	}
	for _, doc := range docs {
		if bytes.Equal(got, doc) {
			return nil
		}
	}
	return failf("LoadDefinition after concurrent saves returned %d bytes that are none of the saved documents: a save replaces the whole document", len(got))
}
