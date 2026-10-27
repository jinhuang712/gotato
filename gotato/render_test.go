package gotato

import "testing"

func TestRenderBlocks(t *testing.T) {
	got := RenderBlocks([]Block{
		{Tag: "resource", Attrs: map[string]string{"path": "a.md", "lines": "1-2"}, Text: "x"},
		{Text: "bare"},
	})
	want := "<resource lines=\"1-2\" path=\"a.md\">x</resource>\nbare"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
