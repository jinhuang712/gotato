package bash_test

import (
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/adapter/bash"
)

// A line over the limit is cut with a marker that gives the size of the whole
// line, and never without one.
func TestLongLineIsCutWithAMarker(t *testing.T) {
	files := map[string]string{
		"long":  strings.Repeat("x", 1<<20) + "\nnext\n",
		"nonl":  strings.Repeat("y", 100),
		"utf8":  strings.Repeat("é", 10) + "\n",
		"fit":   strings.Repeat("z", 8192) + "\n",
		"json":  `{"items":[` + strings.Repeat(`{"id":1},`, 5000) + `{"id":2}]}` + "\n",
		"short": "a\nb\n",
	}
	for _, tc := range []struct {
		name, cmd string
		opts      []bash.Option
		want      string
	}{
		{"default limit", "cat /long", nil, strings.Repeat("x", bash.DefaultMaxLine) + "[line cut: 1.0 MiB]\nnext\n"},
		{"a line at the limit is kept", "cat /fit", nil, files["fit"]},
		{"one byte over", "cat /fit /short | head -n 1", []bash.Option{bash.WithMaxLine(8191)}, strings.Repeat("z", 8191) + "[line cut: 8.0 KiB]\n"},
		{"no newline at the end", "cat /nonl", []bash.Option{bash.WithMaxLine(10)}, "yyyyyyyyyy[line cut: 100 B]"},
		{"a character is not split", "cat /utf8", []bash.Option{bash.WithMaxLine(5)}, "éé[line cut: 20 B]\n"},
		{"short lines are untouched", "cat /short", []bash.Option{bash.WithMaxLine(1)}, "a\nb\n"},
		{"the cut can be turned off", "cat /long | wc -c", []bash.Option{bash.WithMaxLine(-1)}, "1048582\n"},
		{"stderr joined to stdout is cut too", "cat /nonl 2>&1 | cat", []bash.Option{bash.WithMaxLine(3)}, "yyy[line cut: 100 B]"},
		{"a stage of a pipeline sees the whole line", "cat /long | wc -c", nil, "1048582\n"},
		{"grep reads the whole line, the output is cut", "grep -c xxxx /long; grep -o 'x\\{5\\}' /long | head -n 1", nil, "1\nxxxxx\n"},
		{"cut of the first field of a long line", "cut -c1-3 /long | head -n 1", nil, "xxx\n"},
		{"jq output is cut like any other", "jq -c . /json", []bash.Option{bash.WithMaxLine(40)}, `{"items":[{"id":1},{"id":1},{"id":1},{"i[line cut: 44.0 KiB]` + "\n"},
		{"jq reads the whole line, the output is short", "jq '.items|length' /json", nil, "5001\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := run(t, tc.cmd, files, tc.opts...)
			if got.Stdout != tc.want || got.ExitCode != 0 {
				t.Fatalf("%+v\nwant %q", trimResult(got), tc.want)
			}
		})
	}
}

func trimResult(r bash.Result) bash.Result {
	if len(r.Stdout) > 200 {
		r.Stdout = r.Stdout[:100] + "..." + r.Stdout[len(r.Stdout)-60:]
	}
	return r
}

// The engine gives the layer above what it needs to spill: the whole output up
// to the bound, and a flag that says the bound was passed.
func TestOutputBoundGivesTheLayerAboveWhatItNeeds(t *testing.T) {
	files := map[string]string{"a": strings.Repeat("line of text\n", 1000)}
	got := run(t, "cat /a", files, bash.WithMaxOutput(100))
	if !got.Truncated || len(got.Stdout) != 100 || got.Stdout != strings.Repeat("line of text\n", 1000)[:100] {
		t.Fatalf("truncated %v, %d bytes", got.Truncated, len(got.Stdout))
	}
	whole := run(t, "cat /a", files, bash.WithMaxOutput(1<<20))
	if whole.Truncated || len(whole.Stdout) != 13000 {
		t.Fatalf("truncated %v, %d bytes", whole.Truncated, len(whole.Stdout))
	}
}
