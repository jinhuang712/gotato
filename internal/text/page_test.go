package text

import (
	"reflect"
	"testing"
)

func TestSplitLinesAndPaginate(t *testing.T) {
	if SplitLines("", 10) != nil {
		t.Error("empty text must give no lines")
	}
	got := SplitLines("ab\nécdé\n", 3)
	if want := []string{"ab", "éc", "dé"}; !reflect.DeepEqual(got, want) {
		t.Errorf("SplitLines = %q, want %q", got, want)
	}
	pages := Paginate([]string{"aaaa", "bbbb", "cc"}, 10)
	if want := [][]string{{"aaaa", "bbbb"}, {"cc"}}; !reflect.DeepEqual(pages, want) {
		t.Errorf("Paginate = %q, want %q", pages, want)
	}
	if got := Paginate(nil, 10); len(got) != 1 {
		t.Errorf("Paginate(nil) = %q, want one empty page", got)
	}
}
