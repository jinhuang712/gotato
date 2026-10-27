package testkit

import (
	"io"

	"github.com/jinhuang712/gotato/gotato"
)

var (
	// ErrScriptExhausted is returned by ReplayModel when the recording runs out.
	// It wraps io.ErrUnexpectedEOF, so errors.Is matches either sentinel.
	ErrScriptExhausted = func() *gotato.RuntimeError {
		e := gotato.ErrorOf(gotato.CodeModelFailure, "testkit: script exhausted")
		e.Cause = io.ErrUnexpectedEOF
		return e
	}()
)
