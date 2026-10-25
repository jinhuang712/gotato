package bash_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// conformanceFiles are the files every conformance case sees.
func conformanceFiles() map[string]string {
	var twelve strings.Builder
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&twelve, "line %d\n", i)
	}
	return map[string]string{
		"results/a":         "alpha\nbeta\ngamma\ndelta\n",
		"results/b":         "Beta two\nzeta\n",
		"results/nonl":      "one\ntwo",
		"results/empty":     "",
		"results/twelve":    twelve.String(),
		"results/star":      "x*a\nxa\n",
		"results/mixed":     "apple pie\nApple tart\nbanana\n\ncherry (red)\na.b\naxb\nfoo|bar\n",
		"results/bin":       "text\x00binary\n",
		"results/crlf":      "a\r\nb\r\n",
		"results/nums":      "10\n9\n2\n-1\n1.5\n01\nabc\n",
		"results/nums2":     "1\n01\n1.0\n2\n",
		"results/csv":       "b,2,x\na,10,y\nc,1,z\na,10,y\n",
		"results/dups":      "a\na\nb\nA\nc\nc\nc\n",
		"results/dups2":     "a\nA\nb\nB\nB\n",
		"results/blank":     "\n\n\nx\n",
		"results/words":     "one two  three\nfour\n\n  five six\n",
		"results/ws":        " b\na\n\tc\n",
		"results/cols":      "x  b 3\ny a 1\nz c 2\n",
		"results/tsv":       "a\tb\tc\n",
		"results/b64":       "aGVsbG8gd29ybGQ=\n",
		"results/long":      strings.Repeat("x", 1<<20) + "\n",
		"results/sub/deep":  "beta deep\n",
		"results/sub/mid":   "12345",
		"results/sub/small": "x",
		"results/sub/.hid":  "h\n",
		"jqdata/one.json":   `{"name":"x","id":3,"tags":["a","b"],"nested":{"z":1,"a":[1,2,{"q":null}]}}` + "\n",
		"jqdata/nd.json":    `{"id":2,"name":"bob","price":10.5,"ok":true}` + "\n" + `{"id":1,"name":"al","price":3,"ok":false}` + "\n" + `{"id":3,"name":"cy","price":7.25,"ok":true}` + "\n",
		"jqdata/arr.json":   `[{"k":"a","v":1},{"k":"b","v":2},{"k":"a","v":3}]`,
		"jqdata/text.txt":   "line one\nline two\nlast",
	}
}

// allCases is every conformance case: the table of cat, head and grep above,
// and the tables of the other commands.
func allCases() []conformanceCase {
	return slices.Concat(conformanceCases, tailSedWcCases, sortUniqCutTrCases, base64EchoLsCases, grepMoreCases, jqCases, jqDifferCases)
}

// conformanceCase is one command with the output GNU coreutils 9 and GNU grep
// 3 give for it. The expectations were written from the GNU documentation and
// the behavior of those tools, not recorded from a run (no GNU tools were
// available where this file was written); TestAgainstGNU runs the same table
// against installed GNU tools to check them.
type conformanceCase struct {
	name   string
	cmd    string
	stdout string
	stderr string
	code   int
}

var conformanceCases = []conformanceCase{
	// cat
	{name: "cat one file", cmd: "cat /results/a", stdout: "alpha\nbeta\ngamma\ndelta\n"},
	{name: "cat relative path", cmd: "cat results/b", stdout: "Beta two\nzeta\n"},
	{name: "cat two files", cmd: "cat /results/b /results/a", stdout: "Beta two\nzeta\nalpha\nbeta\ngamma\ndelta\n"},
	{name: "cat empty file", cmd: "cat /results/empty", stdout: ""},
	{name: "cat no final newline", cmd: "cat /results/nonl", stdout: "one\ntwo"},
	{name: "cat joins a file with no final newline", cmd: "cat /results/nonl /results/b", stdout: "one\ntwoBeta two\nzeta\n"},
	{name: "cat -n", cmd: "cat -n /results/a", stdout: "     1\talpha\n     2\tbeta\n     3\tgamma\n     4\tdelta\n"},
	{name: "cat --number", cmd: "cat --number /results/b", stdout: "     1\tBeta two\n     2\tzeta\n"},
	{name: "cat -n numbers blank lines", cmd: "cat -n /results/mixed", stdout: "     1\tapple pie\n     2\tApple tart\n     3\tbanana\n     4\t\n     5\tcherry (red)\n     6\ta.b\n     7\taxb\n     8\tfoo|bar\n"},
	{name: "cat -n no final newline", cmd: "cat -n /results/nonl", stdout: "     1\tone\n     2\ttwo"},
	{name: "cat -n goes on across files", cmd: "cat -n /results/b /results/b", stdout: "     1\tBeta two\n     2\tzeta\n     3\tBeta two\n     4\tzeta\n"},
	{name: "cat -n joins a line across files", cmd: "cat -n /results/nonl /results/b", stdout: "     1\tone\n     2\ttwoBeta two\n     3\tzeta\n"},
	{name: "cat from stdin", cmd: "cat < /results/b", stdout: "Beta two\nzeta\n"},
	{name: "cat - reads stdin", cmd: "cat - < /results/b", stdout: "Beta two\nzeta\n"},
	{name: "cat of a here-document", cmd: "cat <<EOF\nhello\nEOF", stdout: "hello\n"},
	{name: "cat of a here-string", cmd: "cat <<< hello", stdout: "hello\n"},
	{name: "cat -n of a pipe", cmd: "cat /results/b | cat -n", stdout: "     1\tBeta two\n     2\tzeta\n"},
	{name: "cat missing file", cmd: "cat /results/zzz", stderr: "cat: /results/zzz: No such file or directory\n", code: 1},
	{name: "cat goes on after a missing file", cmd: "cat /results/zzz /results/b", stdout: "Beta two\nzeta\n", stderr: "cat: /results/zzz: No such file or directory\n", code: 1},
	{name: "cat of a directory", cmd: "cat /results", stderr: "cat: /results: Is a directory\n", code: 1},

	// head
	{name: "head default is 10 lines", cmd: "head /results/twelve", stdout: "line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8\nline 9\nline 10\n"},
	{name: "head -n 3", cmd: "head -n 3 /results/twelve", stdout: "line 1\nline 2\nline 3\n"},
	{name: "head -n3", cmd: "head -n3 /results/twelve", stdout: "line 1\nline 2\nline 3\n"},
	{name: "head -3", cmd: "head -3 /results/twelve", stdout: "line 1\nline 2\nline 3\n"},
	{name: "head --lines=2", cmd: "head --lines=2 /results/twelve", stdout: "line 1\nline 2\n"},
	{name: "head --lines 2", cmd: "head --lines 2 /results/twelve", stdout: "line 1\nline 2\n"},
	{name: "head --line=2 (a unique prefix)", cmd: "head --line=2 /results/twelve", stdout: "line 1\nline 2\n"},
	{name: "head -n 0", cmd: "head -n 0 /results/twelve", stdout: ""},
	{name: "head -n more than the file", cmd: "head -n 100 /results/b", stdout: "Beta two\nzeta\n"},
	{name: "head -n -10 (all but the last 10)", cmd: "head -n -10 /results/twelve", stdout: "line 1\nline 2\n"},
	{name: "head -n -20 (all but more than the file)", cmd: "head -n -20 /results/twelve", stdout: ""},
	{name: "head -n -0 (all)", cmd: "head -n -0 /results/b", stdout: "Beta two\nzeta\n"},
	{name: "head -c 7", cmd: "head -c 7 /results/a", stdout: "alpha\nb"},
	{name: "head --bytes=3", cmd: "head --bytes=3 /results/a", stdout: "alp"},
	{name: "head no final newline", cmd: "head -n 5 /results/nonl", stdout: "one\ntwo"},
	{name: "head empty file", cmd: "head /results/empty", stdout: ""},
	{name: "head two files has headers", cmd: "head -n 1 /results/a /results/b", stdout: "==> /results/a <==\nalpha\n\n==> /results/b <==\nBeta two\n"},
	{name: "head -q two files", cmd: "head -q -n 1 /results/a /results/b", stdout: "alpha\nBeta two\n"},
	{name: "head -v one file", cmd: "head -v -n 1 /results/a", stdout: "==> /results/a <==\nalpha\n"},
	{name: "head options after the file", cmd: "head /results/twelve -n 1", stdout: "line 1\n"},
	{name: "head from a pipe", cmd: "cat /results/twelve | head -n 2", stdout: "line 1\nline 2\n"},
	{name: "head - reads stdin", cmd: "head -n 1 - < /results/a", stdout: "alpha\n"},
	{name: "head -v of stdin", cmd: "head -v -n 1 < /results/a", stdout: "==> standard input <==\nalpha\n"},
	{name: "head bad number", cmd: "head -n x /results/a", stderr: "head: invalid number of lines: 'x'\n", code: 1},
	{name: "head missing file", cmd: "head /results/zzz", stderr: "head: cannot open '/results/zzz' for reading: No such file or directory\n", code: 1},
	{name: "head goes on after a missing file", cmd: "head -n 1 /results/zzz /results/a", stdout: "==> /results/a <==\nalpha\n", stderr: "head: cannot open '/results/zzz' for reading: No such file or directory\n", code: 1},

	// grep: matching
	{name: "grep a word", cmd: "grep apple /results/mixed", stdout: "apple pie\n"},
	{name: "grep -i", cmd: "grep -i apple /results/mixed", stdout: "apple pie\nApple tart\n"},
	{name: "grep --ignore-case", cmd: "grep --ignore-case APPLE /results/mixed", stdout: "apple pie\nApple tart\n"},
	{name: "grep -n", cmd: "grep -n an /results/mixed", stdout: "3:banana\n"},
	{name: "grep -in (grouped)", cmd: "grep -in apple /results/mixed", stdout: "1:apple pie\n2:Apple tart\n"},
	{name: "grep -c", cmd: "grep -c a /results/mixed", stdout: "6\n"},
	{name: "grep -c of no match", cmd: "grep -c zzz /results/mixed", stdout: "0\n", code: 1},
	{name: "grep -v", cmd: "grep -v a /results/mixed", stdout: "\ncherry (red)\n"},
	{name: "grep -vc", cmd: "grep -vc a /results/mixed", stdout: "2\n"},
	{name: "grep no match", cmd: "grep nothing /results/a", code: 1},
	{name: "grep last line has no newline", cmd: "grep two /results/nonl", stdout: "two\n"},
	{name: "grep empty file", cmd: "grep x /results/empty", code: 1},
	{name: "grep empty pattern matches every line", cmd: "grep -c '' /results/a", stdout: "4\n"},
	{name: "grep -e twice", cmd: "grep -e banana -e cherry /results/mixed", stdout: "banana\ncherry (red)\n"},
	{name: "grep -e with the pattern joined", cmd: "grep -ebanana /results/mixed", stdout: "banana\n"},
	{name: "grep --regexp=", cmd: "grep --regexp=banana /results/mixed", stdout: "banana\n"},
	{name: "grep a pattern of two lines", cmd: "grep 'banana\ncherry' /results/mixed", stdout: "banana\ncherry (red)\n"},
	{name: "grep option after the file", cmd: "grep apple /results/mixed -i", stdout: "apple pie\nApple tart\n"},
	{name: "grep -- ends the options", cmd: "grep -- -x /results/a", code: 1},
	{name: "grep from a pipe", cmd: "cat /results/mixed | grep -c a", stdout: "6\n"},
	{name: "grep from stdin", cmd: "grep banana < /results/mixed", stdout: "banana\n"},
	{name: "grep - reads stdin", cmd: "grep -H banana - < /results/mixed", stdout: "(standard input):banana\n"},
	{name: "grep chain", cmd: "cat /results/twelve | grep -n 1 | head -n 2", stdout: "1:line 1\n10:line 10\n"},

	// grep: basic and extended regular expressions
	{name: "grep BRE dot", cmd: "grep a.b /results/mixed", stdout: "a.b\naxb\n"},
	{name: "grep BRE escaped dot", cmd: `grep 'a\.b' /results/mixed`, stdout: "a.b\n"},
	{name: "grep -F is literal", cmd: "grep -F a.b /results/mixed", stdout: "a.b\n"},
	{name: "grep -F with two patterns", cmd: "grep -F -e a.b -e foo /results/mixed", stdout: "a.b\nfoo|bar\n"},
	{name: "grep -F -i", cmd: "grep -Fi A.B /results/mixed", stdout: "a.b\n"},
	{name: "grep -E alternation", cmd: "grep -E 'apple|banana' /results/mixed", stdout: "apple pie\nbanana\n"},
	{name: "grep BRE \\| alternation", cmd: `grep 'apple\|banana' /results/mixed`, stdout: "apple pie\nbanana\n"},
	{name: "grep BRE | is literal", cmd: "grep 'o|b' /results/mixed", stdout: "foo|bar\n"},
	{name: "grep BRE ( ) are literal", cmd: "grep '(red)' /results/mixed", stdout: "cherry (red)\n"},
	{name: "grep ERE escaped parens are literal", cmd: `grep -E '\(red\)' /results/mixed`, stdout: "cherry (red)\n"},
	{name: "grep ERE group", cmd: "grep -E '(an)+a$' /results/mixed", stdout: "banana\n"},
	{name: "grep BRE group", cmd: `grep '\(an\)\{2\}' /results/mixed`, stdout: "banana\n"},
	{name: "grep BRE \\+", cmd: `grep 'p\+ie' /results/mixed`, stdout: "apple pie\n"},
	{name: "grep BRE + is literal", cmd: "grep 'p+ie' /results/mixed", code: 1},
	{name: "grep ERE ?", cmd: "grep -E 'axb?' /results/mixed", stdout: "axb\n"},
	{name: "grep BRE interval", cmd: `grep 'p\{2\}' /results/mixed`, stdout: "apple pie\nApple tart\n"},
	{name: "grep BRE { is literal", cmd: "grep 'p{2}' /results/mixed", code: 1},
	{name: "grep ERE interval", cmd: "grep -E 'p{2}' /results/mixed", stdout: "apple pie\nApple tart\n"},
	{name: "grep ERE interval with a missing low bound", cmd: "grep -E 'p{,2}ie' /results/mixed", stdout: "apple pie\n"},
	{name: "grep ERE brace that is not an interval", cmd: "grep -E 'a{x' /results/mixed", code: 1},
	{name: "grep anchors", cmd: "grep '^a' /results/mixed", stdout: "apple pie\na.b\naxb\n"},
	{name: "grep end anchor", cmd: "grep 'e$' /results/mixed", stdout: "apple pie\n"},
	{name: "grep empty lines", cmd: "grep -n '^$' /results/mixed", stdout: "4:\n"},
	{name: "grep BRE leading star is literal", cmd: "grep '*a' /results/star", stdout: "x*a\n"},
	{name: "grep BRE star repeats", cmd: "grep -c 'x*a' /results/star", stdout: "2\n"},
	{name: "grep BRE star after the anchor is literal", cmd: "grep '^*a' /results/star", code: 1},
	{name: "grep BRE ^ in the middle is literal", cmd: "grep 'a^b' /results/mixed", code: 1},
	{name: "grep BRE $ in the middle is literal", cmd: `grep -c 'a$b' /results/mixed`, stdout: "0\n", code: 1},
	{name: "grep bracket", cmd: "grep '[bc]a' /results/mixed", stdout: "banana\nfoo|bar\n"},
	{name: "grep negated bracket", cmd: "grep -c '^[^a]' /results/mixed", stdout: "4\n"},
	{name: "grep bracket range", cmd: "grep -c '^[a-b]' /results/mixed", stdout: "4\n"},
	{name: "grep bracket with ] first", cmd: "grep '[]|]bar' /results/mixed", stdout: "foo|bar\n"},
	{name: "grep bracket backslash is literal", cmd: `grep '[\n]' /results/mixed`, stdout: "banana\n"},
	{name: "grep bracket class", cmd: "grep '[[:upper:]]' /results/mixed", stdout: "Apple tart\n"},
	{name: "grep bracket digit class", cmd: "grep -c '[[:digit:]]' /results/twelve", stdout: "12\n"},
	{name: "grep word boundary", cmd: `grep '\bbeta\b' /results/a`, stdout: "beta\n"},
	{name: "grep \\w", cmd: `grep -c '^\w*$' /results/a`, stdout: "4\n"},
	{name: "grep -x", cmd: "grep -x banana /results/mixed", stdout: "banana\n"},
	{name: "grep -x no partial match", cmd: "grep -x ban /results/mixed", code: 1},
	{name: "grep -x with alternation", cmd: "grep -Ex 'ban|banana' /results/mixed", stdout: "banana\n"},
	{name: "grep leftmost-longest alternation with -x", cmd: "grep -Ex 'a|ab' /results/mixed", code: 1},
	{name: "grep dot matches a multi-byte character", cmd: "grep -c '^.$' /results/uni", stdout: "1\n"},

	// grep: output of several files and the exit status
	{name: "grep two files prefixes names", cmd: "grep beta /results/a /results/b", stdout: "/results/a:beta\n"},
	{name: "grep -i two files", cmd: "grep -i beta /results/a /results/b", stdout: "/results/a:beta\n/results/b:Beta two\n"},
	{name: "grep -n two files", cmd: "grep -n -i beta /results/a /results/b", stdout: "/results/a:2:beta\n/results/b:1:Beta two\n"},
	{name: "grep -c two files", cmd: "grep -c a /results/a /results/b", stdout: "/results/a:4\n/results/b:2\n"},
	{name: "grep -h two files", cmd: "grep -h -i beta /results/a /results/b", stdout: "beta\nBeta two\n"},
	{name: "grep -H one file", cmd: "grep -H beta /results/a", stdout: "/results/a:beta\n"},
	{name: "grep -l", cmd: "grep -l banana /results/mixed /results/a", stdout: "/results/mixed\n"},
	{name: "grep -l no match", cmd: "grep -l zzz /results/a", code: 1},
	{name: "grep -q found", cmd: "grep -q beta /results/a", code: 0},
	{name: "grep -q not found", cmd: "grep -q zzz /results/a", code: 1},
	{name: "grep missing file", cmd: "grep x /results/zzz", stderr: "grep: /results/zzz: No such file or directory\n", code: 2},
	{name: "grep missing file and a match", cmd: "grep beta /results/zzz /results/a", stdout: "/results/a:beta\n", stderr: "grep: /results/zzz: No such file or directory\n", code: 2},
	{name: "grep -q found and a missing file", cmd: "grep -q beta /results/zzz /results/a", stderr: "grep: /results/zzz: No such file or directory\n", code: 0},
	{name: "grep directory", cmd: "grep x /results", stderr: "grep: /results: Is a directory\n", code: 2},
	{name: "grep without a pattern", cmd: "grep", stderr: "Usage: grep [OPTION]... PATTERNS [FILE]...\nTry 'grep --help' for more information.\n", code: 2},
	{name: "grep unmatched paren in an ERE", cmd: "grep -E '(a' /results/a", stderr: "grep: Unmatched ( or \\(\n", code: 2},

	// pipelines and the shell around them
	{name: "and runs the second on success", cmd: "grep -q beta /results/a && cat /results/b", stdout: "Beta two\nzeta\n"},
	{name: "and skips the second on failure", cmd: "grep -q zzz /results/a && cat /results/b", code: 1},
	{name: "or runs the second on failure", cmd: "grep -q zzz /results/a || cat /results/b", stdout: "Beta two\nzeta\n"},
	{name: "semicolon runs both", cmd: "cat /results/b; head -n 1 /results/a", stdout: "Beta two\nzeta\nalpha\n"},
	{name: "newline runs both", cmd: "cat /results/b\nhead -n 1 /results/a", stdout: "Beta two\nzeta\nalpha\n"},
	{name: "the exit status is the last command's", cmd: "cat /results/zzz; grep -q beta /results/a", code: 0, stderr: "cat: /results/zzz: No such file or directory\n"},
	{name: "negation", cmd: "! grep -q zzz /results/a", code: 0},
	{name: "a glob", cmd: "cat /results/e*", stdout: ""},
	{name: "a glob with several files, in order", cmd: "cat /results/[ab]", stdout: "alpha\nbeta\ngamma\ndelta\nBeta two\nzeta\n"},
	{name: "a glob that matches nothing is passed as written", cmd: "cat /results/q*", stderr: "cat: '/results/q*': No such file or directory\n", code: 1},
	{name: "cat quotes a missing name with a space", cmd: "cat '/results/a b'", stderr: "cat: '/results/a b': No such file or directory\n", code: 1},
	{name: "cat writes a missing name with a single quote in double quotes", cmd: `cat "/results/it's"`, stderr: "cat: \"/results/it's\": No such file or directory\n", code: 1},
	{name: "cat leaves a plain missing name as it is", cmd: "cat /results/nope", stderr: "cat: /results/nope: No such file or directory\n", code: 1},
	{name: "head always quotes a missing name", cmd: "head /results/q*", stderr: "head: cannot open '/results/q*' for reading: No such file or directory\n", code: 1},
	{name: "brace expansion", cmd: "cat /results/{b,empty}", stdout: "Beta two\nzeta\n"},
	{name: "a variable", cmd: "f=/results/b; cat $f", stdout: "Beta two\nzeta\n"},
	{name: "a variable in a pattern", cmd: "w=beta; grep $w /results/a", stdout: "beta\n"},
	{name: "single quotes keep a dollar", cmd: "grep -c 'a$' /results/a", stdout: "4\n"},
	{name: "a command that is stopped by a closed pipe", cmd: "cat /results/twelve /results/twelve | head -n 1", stdout: "line 1\n"},
	{name: "a group", cmd: "{ cat /results/b; cat /results/b; } | grep -c zeta", stdout: "2\n"},
	{name: "if", cmd: "if grep -q beta /results/a; then cat /results/b; else cat /results/a; fi", stdout: "Beta two\nzeta\n"},
	{name: "case", cmd: "case x in x) cat /results/b;; *) cat /results/a;; esac", stdout: "Beta two\nzeta\n"},
	{name: "stderr to the pipe", cmd: "cat /results/zzz 2>&1 | cat", stdout: "cat: /results/zzz: No such file or directory\n"},
	{name: "stderr to /dev/null", cmd: "cat /results/zzz 2>/dev/null", code: 1},
	{name: "stdout to /dev/null", cmd: "cat /results/b > /dev/null", stdout: ""},
	{name: "input from /dev/null", cmd: "cat < /dev/null", stdout: ""},
}

func TestGNUConformance(t *testing.T) {
	files := conformanceFiles()
	files["results/uni"] = "é\nab\n"
	for _, c := range allCases() {
		t.Run(c.name, func(t *testing.T) {
			got := run(t, c.cmd, files)
			if got.Stdout != c.stdout || got.Stderr != c.stderr || got.ExitCode != c.code || got.Refused {
				t.Errorf("%s\n got: stdout %q stderr %q exit %d refused %v\nwant: stdout %q stderr %q exit %d", c.cmd, got.Stdout, got.Stderr, got.ExitCode, got.Refused, c.stdout, c.stderr, c.code)
			}
		})
	}
}
