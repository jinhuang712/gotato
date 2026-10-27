package service_test

import (
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/modelctx"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// TestEverySentinelCarriesACode holds the naming rule: a sentinel error is
// Err…, and it carries the Code… that classifies it.
func TestEverySentinelCarriesACode(t *testing.T) {
	cases := []struct {
		err  error
		code gotato.ErrorCode
	}{
		{session.ErrNotFound, gotato.CodeNotFound},
		{session.ErrExists, gotato.CodeInvalidState},
		{session.ErrConflict, gotato.CodeConflict},
		{session.ErrExpired, gotato.CodeInvalidState},
		{session.ErrOldLayout, gotato.CodeNotSupported},
		{service.ErrNotPersisted, gotato.CodeStoreFailure},
		{service.ErrBusy, gotato.CodeBusy},
		{modelctx.ErrBadRecap, gotato.CodeModelProtocolFailure},
		{modelctx.ErrNoRecapContext, gotato.CodeInvalidArgument},
		{testkit.ErrScriptExhausted, gotato.CodeModelFailure},
		{gotato.ErrEventsLost, gotato.CodeCannotResume},
	}
	for _, c := range cases {
		if !gotato.IsCode(c.err, c.code) {
			t.Errorf("%v does not carry %q", c.err, c.code)
		}
	}
}

// TestCodeStringsMatchTheirNames keeps a code's name and its wire string in step.
func TestCodeStringsMatchTheirNames(t *testing.T) {
	if gotato.CodeInternalInvariant != "internal_invariant" || gotato.CodeToolArgumentFailure != "tool_argument_failure" || gotato.CodeCanceled != "canceled" {
		t.Fatalf("code strings = %q %q %q", gotato.CodeInternalInvariant, gotato.CodeToolArgumentFailure, gotato.CodeCanceled)
	}
}
