package httpapi

import "time"

// StatusByCode shows the tests the table that maps each ErrorCode to a status.
var StatusByCode = statusOfCode

// WithKeepAlive sets how long an idle Session stream waits before it sends a
// comment.
func WithKeepAlive(d time.Duration) Option {
	return func(h *Handler) {
		if d > 0 {
			h.keepAlive = d
		}
	}
}
