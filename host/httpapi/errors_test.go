package httpapi_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/host/httpapi"
)

// A new ErrorCode must be given a status on purpose: this fails until the
// table maps it.
func TestEveryErrorCodeHasAnExplicitStatus(t *testing.T) {
	all := append(gotato.ErrorCodes(), host.ErrorCodes()...)
	for _, code := range all {
		if _, ok := httpapi.StatusByCode[code]; !ok {
			t.Errorf("error code %q has no HTTP status", code)
		}
	}
	for code := range httpapi.StatusByCode {
		var listed bool
		for _, known := range all {
			listed = listed || known == code
		}
		if !listed {
			t.Errorf("the status table names %q, which neither gotato.ErrorCodes nor host.ErrorCodes lists", code)
		}
	}
}

func TestStatusForTranslatesTheCode(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{host.ErrDraining, http.StatusServiceUnavailable},
		{host.ErrOverloaded, http.StatusServiceUnavailable},
		{host.ErrBusy, http.StatusConflict},
		{host.ErrSessionExists, http.StatusConflict},
		{record.ErrNotFound, http.StatusNotFound},
		{fmt.Errorf("opening: %w", record.ErrNotFound), http.StatusNotFound},
		{gotato.ErrorOf(gotato.CodeStoreFailure, "x"), http.StatusServiceUnavailable},
		{gotato.ErrorOf(gotato.CodeInvalidState, "x"), http.StatusConflict},
	} {
		if got := httpapi.StatusFor(tc.err); got != tc.want {
			t.Errorf("StatusFor(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}
