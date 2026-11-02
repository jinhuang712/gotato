package session

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
)

// ErrNotFound is one of the runtime's errors: it carries the code not_found
// and says the request did not run, and errors.Is keeps matching it.
func TestErrNotFoundIsTheNotFoundCode(t *testing.T) {
	_, err := NewMemoryStore().Load(context.Background(), "missing")
	if !errors.Is(err, record.ErrNotFound) {
		t.Fatalf("Load of a missing Record = %v, want ErrNotFound", err)
	}
	if !gotato.IsCode(err, gotato.CodeNotFound) || !gotato.NotRun(err) || gotato.Retryable(err) {
		t.Fatalf("Load of a missing Record = %v, want not_found, not run, not retryable", err)
	}
	wrapped := fmt.Errorf("opening: %w", record.ErrNotFound)
	if !errors.Is(wrapped, record.ErrNotFound) || !gotato.IsCode(wrapped, gotato.CodeNotFound) {
		t.Fatal("a wrapped ErrNotFound stops matching")
	}
	if errors.Is(ErrConflict, record.ErrNotFound) {
		t.Fatal("ErrConflict matches ErrNotFound")
	}
}
