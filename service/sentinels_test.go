package service_test

import (
	"errors"
	"fmt"
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

// TestSentinelsThatShareACodeAreToldApart: errors.Is matches a sentinel by
// identity, and gotato.IsCode matches by code.
func TestSentinelsThatShareACodeAreToldApart(t *testing.T) {
	if errors.Is(session.ErrExpired, session.ErrExists) || errors.Is(session.ErrExists, session.ErrExpired) {
		t.Error("session.ErrExpired and session.ErrExists match each other")
	}
	if !errors.Is(fmt.Errorf("x: %w", session.ErrExpired), session.ErrExpired) {
		t.Error("a wrapped session.ErrExpired does not match itself")
	}
	if errors.Is(service.ErrNoRun, service.ErrSessionExists) || errors.Is(service.ErrSessionExists, service.ErrNoRun) {
		t.Error("service.ErrNoRun and service.ErrSessionExists match each other")
	}
	if !gotato.IsCode(session.ErrExpired, gotato.CodeInvalidState) {
		t.Error("session.ErrExpired does not carry invalid_state")
	}
	if gotato.Retryable(service.ErrNotPersisted) {
		t.Error("service.ErrNotPersisted is retryable; it was a plain error before it carried a code")
	}
}

// TestCodeStringsMatchTheirNames keeps a code's name and its wire string in step.
func TestCodeStringsMatchTheirNames(t *testing.T) {
	if gotato.CodeInternalInvariant != "internal_invariant" || gotato.CodeToolArgumentFailure != "tool_argument_failure" || gotato.CodeCanceled != "canceled" {
		t.Fatalf("code strings = %q %q %q", gotato.CodeInternalInvariant, gotato.CodeToolArgumentFailure, gotato.CodeCanceled)
	}
}
