package text

import (
	"reflect"
	"testing"
	"time"
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

// A limit smaller than the first rune must still make progress.
func TestSplitLinesWithALimitBelowTheFirstRune(t *testing.T) {
	done := make(chan []string, 1)
	go func() {
		done <- append(append(SplitLines("日本", 2), SplitLines("a\n日b", 0)...), append(SplitLines("日b", 1), SplitLines("日b\n", -3)...)...)
	}()
	select {
	case got := <-done:
		if want := []string{"日", "本", "a", "日", "b", "日", "b", "日", "b"}; !reflect.DeepEqual(got, want) {
			t.Errorf("SplitLines = %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SplitLines does not end when the limit is below one rune")
	}
}
