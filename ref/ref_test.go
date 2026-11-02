package ref_test

import (
	"testing"

	"github.com/jinhuang712/gotato/ref"
)

func TestRefForm(t *testing.T) {
	if got := ref.Result("c1").String(); got != "result:c1" {
		t.Errorf("Result = %q", got)
	}
	if got := ref.Message("m1").String(); got != "msg:m1" {
		t.Errorf("Message = %q", got)
	}
	if got := (ref.Ref{}).String(); got != "" {
		t.Errorf("zero Ref = %q", got)
	}
	if ref.ReadResult != "read_result" || ref.ReadSegment != "read_segment" {
		t.Errorf("tool IDs changed: %q, %q: Segments already written name them", ref.ReadResult, ref.ReadSegment)
	}
}

func TestParse(t *testing.T) {
	for in, want := range map[string]ref.Ref{
		"result:c1":    ref.Result("c1"),
		"  result:c2 ": ref.Result("c2"),
		"msg:m1":       ref.Message("m1"),
		"\tmsg:m-2\n":  ref.Message("m-2"),
	} {
		got, ok := ref.Parse(in)
		if !ok || got != want {
			t.Errorf("Parse(%q) = %v, %v; want %v", in, got, ok, want)
		}
		if again, ok := ref.Parse(got.String()); !ok || again != got {
			t.Errorf("Parse(String(%v)) = %v, %v", got, again, ok)
		}
	}
	for _, in := range []string{"", "result:", "msg:", "c1", "Result:c1", "ref msg:m1"} {
		if got, ok := ref.Parse(in); ok {
			t.Errorf("Parse(%q) = %v, want no ref", in, got)
		}
	}
}

func TestSafe(t *testing.T) {
	for _, id := range []string{"c1", "call_9a2", "toolu_01-x.y"} {
		if !ref.Safe(id) {
			t.Errorf("Safe(%q) = false", id)
		}
	}
	for _, id := range []string{"", "a b", "a\nb", "a]b", "a[b", `a"b`, "a<b", "a>b", `a\b`, "a\x7fb"} {
		if ref.Safe(id) {
			t.Errorf("Safe(%q) = true", id)
		}
	}
}
