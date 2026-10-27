package httpapi_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
)

// A new ErrorCode must be given a status on purpose: this fails until the
// table maps it.
func TestEveryErrorCodeHasAnExplicitStatus(t *testing.T) {
	for _, code := range gotato.ErrorCodes() {
		if _, ok := httpapi.StatusByCode[code]; !ok {
			t.Errorf("error code %q has no HTTP status", code)
		}
	}
	for code := range httpapi.StatusByCode {
		var listed bool
		for _, known := range gotato.ErrorCodes() {
			listed = listed || known == code
		}
		if !listed {
			t.Errorf("the status table names %q, which gotato.ErrorCodes does not list", code)
		}
	}
}

func TestStatusForTranslatesTheCode(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{service.ErrDraining, http.StatusServiceUnavailable},
		{service.ErrOverloaded, http.StatusServiceUnavailable},
		{service.ErrBusy, http.StatusConflict},
		{service.ErrSessionExists, http.StatusConflict},
		{session.ErrNotFound, http.StatusNotFound},
		{fmt.Errorf("opening: %w", session.ErrNotFound), http.StatusNotFound},
		{gotato.ErrorOf(gotato.CodeStoreFailure, "x"), http.StatusServiceUnavailable},
		{gotato.ErrorOf(gotato.CodeInvalidState, "x"), http.StatusConflict},
	} {
		if got := httpapi.StatusFor(tc.err); got != tc.want {
			t.Errorf("StatusFor(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}
