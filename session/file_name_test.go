package session

import "testing"

// A file name decodes to the ID it was made from, and an ID of lowercase ASCII
// is its own name.
func TestNameOfRoundTrips(t *testing.T) {
	for _, id := range []string{"abc", "Abc", "!a", "a%41", "café", "café", "oc_X-1.2", "a b"} {
		name := nameOf(id)
		if got, ok := idOf(name); !ok || got != id {
			t.Errorf("idOf(nameOf(%q)) = %q, %v (name %q)", id, got, ok, name)
		}
	}
	if got := nameOf("abc_1-2.3"); got != "abc_1-2.3" {
		t.Errorf("nameOf of a lowercase ID = %q, want the ID", got)
	}
	for _, name := range []string{"A", "!", "!A", "%", "%4", "%zz", "%2f", "a b"} {
		if id, ok := idOf(name); ok {
			t.Errorf("idOf(%q) = %q, want it refused", name, id)
		}
	}
}
