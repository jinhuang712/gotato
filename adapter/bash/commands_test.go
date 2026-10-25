package bash_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/adapter/bash"
)

// What a command does not support is an error that says so and says what is
// supported, with a status other than 0, and never a different answer.
func TestUnsupportedIsAClearError(t *testing.T) {
	for cmd, want := range map[string]string{
		"tail -f /results/a":               "following a file (-f) is not supported",
		"tail -F /results/a":               "following a file (-F) is not supported",
		"tail --follow /results/a":         "following a file (--follow) is not supported",
		"tail -n 1K /results/a":            "suffixes",
		"tail -s 1 /results/a":             "option '-s' is not supported",
		"sed 2p /results/a":                "without -n",
		"sed -n '/a/p' /results/a":         "regular expression addresses are not supported",
		"sed -n 's/a/b/p' /results/a":      "the command 's' is not supported",
		"sed -n '2d' /results/a":           "the command 'd' is not supported",
		"sed -n '2!p' /results/a":          "! is not supported",
		"sed -n '1~2p' /results/a":         "step address",
		"sed -n '2,+1p' /results/a":        "addr,+N is not supported",
		"sed -n '2p x' /results/a":         "extra characters",
		"sed -n -i 2p /results/a":          "option '-i' is not supported",
		"wc -L /results/a":                 "option '-L' is not supported. Supported options: -c/--bytes, -m/--chars, -l/--lines, -w/--words, --help",
		"wc --files0-from=/results/a":      "is not supported",
		"sort -c /results/a":               "option '-c' is not supported",
		"sort -h /results/a":               "option '-h' is not supported",
		"sort -V /results/a":               "option '-V' is not supported",
		"sort -o x /results/a":             "option '-o' is not supported",
		"sort -k1,2x /results/a":           "the key option 'x' is not supported",
		"sort -k0 /results/a":              "field number is zero",
		"sort -t ab /results/a":            "multi-character tab",
		"uniq /results/a /results/b":       "writing to an output file",
		"uniq -f 1 /results/a":             "option '-f' is not supported",
		"uniq -D /results/a":               "option '-D' is not supported",
		"cut -f1 --output-delimiter=x":     "is not supported",
		"cut -z -f1 /results/a":            "option '-z' is not supported",
		"tr -t a b":                        "option '-t' is not supported",
		"echo a | tr 'a' '[x*3]'":          "repeats are not supported",
		"echo a | tr '[:foo:]' x":          "invalid character class 'foo'",
		"base64 -x /results/a":             "option '-x' is not supported",
		"ls -R /results":                   "option '-R' is not supported",
		"ls -t /results":                   "option '-t' is not supported",
		"grep -w '^a' /results/a":          "-w is not supported with ^ or $",
		"grep -w 'a$' /results/a":          "-w is not supported with ^ or $",
		"grep -m 1 a /results/a":           "option '-m' is not supported",
		"grep -R a /results":               "option '-R' is not supported",
		"grep --include=a -r a /results":   "is not supported",
		"grep --color=always a /results/a": "is not supported",
		"grep -b a /results/a":             "option '-b' is not supported",
	} {
		got := run(t, cmd, safetyFiles)
		if got.ExitCode == 0 || got.Stdout != "" || !strings.Contains(got.Stderr, want) {
			t.Errorf("%s: %+v (want an error with %q)", cmd, got, want)
		}
	}
}

// Every command answers --help with the options it supports (echo takes none).
func TestNewCommandsSupportedOptionsAreListed(t *testing.T) {
	for _, name := range []string{"tail", "sed", "wc", "sort", "uniq", "cut", "tr", "base64", "ls", "grep"} {
		got := run(t, name+" --help", safetyFiles)
		if got.ExitCode != 0 || !strings.HasPrefix(got.Stdout, "Usage: "+name) || !strings.Contains(got.Stdout, "Supported options") && name != "sed" {
			t.Errorf("%s --help: %+v", name, got)
		}
		if !strings.Contains(got.Stdout, "--help") {
			t.Errorf("%s --help does not list --help", name)
		}
	}
}

// grep -r with no file searches the root, and shows names without a prefix
// of "./", as GNU grep does.
func TestGrepRecursiveWithoutAFile(t *testing.T) {
	files := map[string]string{"results/a": "alpha\n", "results/sub/deep": "beta deep\n", "other": "deep\n"}
	got := run(t, "grep -r deep", files)
	if got.Stdout != "other:deep\nresults/sub/deep:beta deep\n" || got.ExitCode != 0 {
		t.Errorf("%+v", got)
	}
	got = run(t, "grep -r deep .", files)
	if got.Stdout != "./other:deep\n./results/sub/deep:beta deep\n" || got.ExitCode != 0 {
		t.Errorf("%+v", got)
	}
	got = run(t, "grep -r deep /", files)
	if got.Stdout != "/other:deep\n/results/sub/deep:beta deep\n" || got.ExitCode != 0 {
		t.Errorf("%+v", got)
	}
}

// wc takes standard input for a pipe, also after a redirect: GNU wc pads a
// redirect from a file to the digits of its size, and this engine cannot see
// the file behind a redirect, so the padding of a pipe is used.
func TestWcStandardInputIsAPipe(t *testing.T) {
	got := run(t, "wc < /results/a", safetyFiles)
	if got.Stdout != "      2       2      11\n" {
		t.Errorf("%+v", got)
	}
}

// The commands that read to the end stop early where GNU's do, and end when
// ctx ends where they cannot.
func TestNewCommandsEndEarlyAndOnCancel(t *testing.T) {
	for _, cmd := range []string{
		"sed -n 2,3p /inf",
		"sed -n 1p /inf",
		"tail -n +3 /inf | head -n 2",
		"tail -c +5 /inf | head -n 2",
		"cut -c1-4 /inf | head -n 3",
		"cut -d' ' -f2 /inf | head -n 3",
		"tr a b < /inf | head -n 3",
		"base64 /inf | head -n 2",
		"uniq /inf | head -n 1",
		"uniq -c /inf | head -n 1",
		"grep -o line /inf | head -n 3",
		"grep -A1 line /inf | head -n 3",
		"grep -w line /inf | head -n 3",
		"grep -r line /inf | head -n 3",
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		start := time.Now()
		got, err := bash.Run(ctx, cmd, &endless{})
		cancel()
		if err != nil || time.Since(start) > 5*time.Second {
			t.Errorf("%s: %v after %v", cmd, err, time.Since(start))
		}
		if strings.Count(got.Stdout, "\n") < 1 {
			t.Errorf("%s: stdout %q", cmd, got.Stdout)
		}
	}
	for _, cmd := range []string{
		"tail -n 3 /inf", "tail -c 3 /inf", "tail -n +3 /inf | grep -c zzz", "wc /inf", "wc -l /inf", "sed -n '$p' /inf",
		"uniq -d /inf", "cut -c1 /inf | grep -c zzz", "tr -d x < /inf | grep -c zzz", "base64 /inf | grep -c zzz",
		"grep -c -A1 zzz /inf", "grep -wc zzz /inf", "grep -o zzz /inf",
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		start := time.Now()
		_, err := bash.Run(ctx, cmd, &endless{})
		cancel()
		if err == nil {
			t.Errorf("%s: returned without an error", cmd)
		}
		if time.Since(start) > 5*time.Second {
			t.Errorf("%s: took %v to stop", cmd, time.Since(start))
		}
	}
}

// sort holds its input in memory, so it stops with an error at a bound; the
// same goes for tail -c with a count past it.
func TestSortAndTailAreBounded(t *testing.T) {
	for _, cmd := range []string{"sort /inf", "tail -c 999999999 /inf", "tail -n 999999999 /inf"} {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		start := time.Now()
		got, err := bash.Run(ctx, cmd, &endless{})
		cancel()
		if err != nil || got.ExitCode == 0 || !strings.Contains(got.Stderr, "too large to hold in memory") {
			t.Errorf("%s: %+v err %v after %v", cmd, got, err, time.Since(start))
		}
	}
}
