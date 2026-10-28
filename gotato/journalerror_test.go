package gotato

import (
	"errors"
	"strings"
	"testing"
)

// A Journal's own error text stays on the Cause: Message reaches clients.
func TestJournalErrorKeepsForeignTextOffMessage(t *testing.T) {
	failure := journalError(errors.New("dsn=postgres://user:hunter2@db"))
	if failure.Code != CodeStoreFailure || strings.Contains(failure.Message, "hunter2") {
		t.Fatalf("journalError = %+v", failure)
	}
	if failure.Cause == nil || !strings.Contains(failure.Cause.Error(), "hunter2") {
		t.Fatalf("Cause = %v, want the original error", failure.Cause)
	}
}
