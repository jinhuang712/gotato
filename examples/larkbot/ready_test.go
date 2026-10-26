package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadyzFollowsDraining(t *testing.T) {
	draining := false
	h := readyHandler(func() bool { return draining })
	for _, c := range []struct {
		draining bool
		want     int
	}{{false, http.StatusOK}, {true, http.StatusServiceUnavailable}} {
		draining = c.draining
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))
		if rec.Code != c.want {
			t.Fatalf("draining=%v: status %d, want %d", c.draining, rec.Code, c.want)
		}
	}
}
