package text

import (
	"testing"
)

func TestSpillRef(t *testing.T) {
	if got := SpillRef("c1"); got != "result:c1" {
		t.Fatalf("SpillRef = %q", got)
	}
	for ref, want := range map[string]string{"result:c1": "c1", "  result:c2 ": "c2"} {
		if call, ok := CallOfRef(ref); !ok || call != want {
			t.Errorf("CallOfRef(%q) = %q, %v", ref, call, ok)
		}
	}
	for _, ref := range []string{"", "result:", "c1"} {
		if _, ok := CallOfRef(ref); ok {
			t.Errorf("CallOfRef(%q) ok", ref)
		}
	}
}
