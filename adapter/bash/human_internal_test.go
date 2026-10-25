package bash

import "testing"

// humanSize rounds up, as ls -h does, with one decimal below 10.
func TestHumanSize(t *testing.T) {
	for n, want := range map[uint64]string{
		0:       "0",
		1:       "1",
		1023:    "1023",
		1024:    "1.0K",
		1025:    "1.1K",
		1536:    "1.5K",
		10239:   "10K",
		10240:   "10K",
		10241:   "11K",
		1048575: "1.0M",
		1048576: "1.0M",
		1048577: "1.1M",
	} {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
}
