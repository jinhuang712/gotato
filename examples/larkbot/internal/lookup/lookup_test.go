package lookup

import "testing"

func TestExpected(t *testing.T) {
	want := "order-88501 is on line 42010 of the export, and its total is 80.37."
	if got := Expected(); got != want {
		t.Fatalf("Expected() = %q, want %q", got, want)
	}
}
