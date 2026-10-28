package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
)

const foreignSecret = "dsn=postgres://user:hunter2@db"

type publicText struct{}

func (publicText) Error() string  { return "private " + foreignSecret }
func (publicText) Public() string { return "the store is unavailable" }

// A failure the service did not write keeps its text on the Cause; Message is
// fixed, or the text of a PublicError.
func TestForeignErrorKeepsPrivateTextOffMessage(t *testing.T) {
	raw := errors.New(foreignSecret)
	e := foreignError(gotato.CodeStoreFailure, "fixed", raw)
	if e.Message != "fixed" || e.Cause != raw || !e.Retryable {
		t.Fatalf("foreignError = %+v", e)
	}
	if got := foreignError(gotato.CodeStoreFailure, "fixed", fmt.Errorf("wrap: %w", publicText{})).Message; got != "the store is unavailable" {
		t.Fatalf("PublicError text = %q", got)
	}
	out := canceledRunResult("s", "a", fmt.Errorf("%s: %w", foreignSecret, context.Canceled))
	if out.Result.Error.Code != gotato.CodeCanceled || strings.Contains(out.Result.Error.Message, "hunter2") {
		t.Fatalf("canceledRunResult = %+v", out.Result.Error)
	}
	if got := toolSetsTimeout(raw).Error(); strings.Contains(got, "hunter2") {
		t.Fatalf("toolSetsTimeout = %q", got)
	}
}
