package bash_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/adapter/bash"
)

var safetyFiles = map[string]string{"results/a": "alpha\nbeta\n", "results/b": "Beta two\n"}

// A script cannot read a file of the machine, by an absolute path, a relative
// one or a redirect.
func TestACommandCannotReadARealFile(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP-SECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{
		"cat /etc/passwd",
		"cat " + secret,
		"cat " + strings.TrimPrefix(secret, "/"),
		"cat < /etc/passwd",
		"grep -r root /etc",
		"grep root /etc/passwd",
		"head /etc/passwd",
		"cat /results/../etc/passwd",
		"cat /../../etc/passwd",
		"cat /proc/self/environ",
		"cat /dev/zero",
		"cat /dev/stdin",
	} {
		got := run(t, cmd, safetyFiles)
		if got.ExitCode == 0 || strings.Contains(got.Stdout+got.Stderr, "root:") || strings.Contains(got.Stdout, "TOP-SECRET") {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", cmd, got.ExitCode, got.Stdout, got.Stderr)
		}
	}
}

// The environment holds no value of the process, and the few it holds are
// constants.
func TestTheEnvironmentIsEmpty(t *testing.T) {
	t.Setenv("HOME", "/home/someone-real")
	t.Setenv("SECRET_TOKEN", "hunter2")
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("USER", "someone-real")
	got := run(t, `cat <<EOF
HOME=$HOME
TOKEN=$SECRET_TOKEN
PATH=$PATH
USER=$USER
PWD=$PWD
UID=$UID
GID=$GID
SHELL=$SHELL
HOST=$HOSTNAME
OLDPWD=$OLDPWD
SECONDS=$SECONDS
EOF`, safetyFiles)
	want := "HOME=/\nTOKEN=\nPATH=\nUSER=\nPWD=/\nUID=0\nGID=0\nSHELL=\nHOST=\nOLDPWD=\nSECONDS=\n"
	if got.Stdout != want || got.ExitCode != 0 {
		t.Fatalf("got %q (exit %d, stderr %q), want %q", got.Stdout, got.ExitCode, got.Stderr, want)
	}
}

// Nothing starts a program or reaches a builtin of the interpreter.
func TestNoCommandStartsAProgram(t *testing.T) {
	cases := map[string]string{
		"awk 1":                          "awk",
		"/bin/awk":                       "/bin/awk",
		"./awk":                          "./awk",
		"exec cat /results/a":            "exec",
		"eval cat /results/a":            "eval",
		"source /etc/profile":            "source",
		". /etc/profile":                 ".",
		"trap 'cat' EXIT":                "trap",
		"command ls":                     "command",
		"builtin echo hi":                "builtin",
		"printf hi":                      "printf",
		"cd /results":                    "cd",
		"set -x":                         "set",
		"read x":                         "read",
		"test -f /results/a":             "test",
		"[ -f /results/a ]":              "[",
		"true":                           "true",
		"sh -c 'cat /results/a'":         "sh",
		"bash -c 'cat /results/a'":       "bash",
		"xargs cat":                      "xargs",
		"cat /results/a | tee /tmp/x":    "tee",
		"env":                            "env",
		"alias x=cat":                    "alias",
		"wait":                           "wait",
		"kill 1":                         "kill",
		"unset HOME":                     "unset",
		"find / -name passwd":            "find",
		"cat /results/a; curl localhost": "curl",
	}
	for cmd, name := range cases {
		got := run(t, cmd, safetyFiles)
		if got.ExitCode != 127 || !got.Refused || got.Stdout != "" {
			t.Errorf("%s: exit %d refused %v stdout %q", cmd, got.ExitCode, got.Refused, got.Stdout)
		}
		if !strings.Contains(got.Stderr, name+":") || !strings.Contains(got.Stderr, "The commands are: cat, grep, head, sed, tail, wc.") {
			t.Errorf("%s: stderr %q does not name the command and list the commands", cmd, got.Stderr)
		}
	}
}

// A command name that is not a literal is decided when it runs, after
// expansion, and a name that is not a command still reaches no program.
func TestACommandNameBuiltByExpansionIsChecked(t *testing.T) {
	for _, cmd := range []string{"c=awk; $c /", "c=eval; $c cat", "c=/bin/sh; $c -c id", "c=exec; $c cat", "c=''; x=awk; \"$x\" /"} {
		got := run(t, cmd, safetyFiles)
		if got.ExitCode != 127 || got.Refused || !strings.Contains(got.Stderr, "The commands are: cat, grep, head, sed, tail, wc.") {
			t.Errorf("%s: exit %d refused %v stderr %q", cmd, got.ExitCode, got.Refused, got.Stderr)
		}
	}
	// A name built by expansion that is a command works, as in bash.
	if got := run(t, "c=cat; $c /results/b", safetyFiles); got.Stdout != "Beta two\n" || got.ExitCode != 0 {
		t.Errorf("c=cat; $c: %+v", got)
	}
}

// Syntax the subset does not have is refused before anything runs.
func TestUnsupportedSyntaxIsRefused(t *testing.T) {
	cases := []struct {
		cmd  string
		want string // a phrase the message holds
	}{
		{"for f in /results/*; do cat $f; done", "for loop"},
		{"for ((i=0;i<3;i++)); do cat /results/a; done", "for loop"},
		{"select x in a b; do cat; done", "select loop"},
		{"while true; do cat /results/a; done", "while loop"},
		{"until false; do cat /results/a; done", "until loop"},
		{"f() { cat /results/a; }; f", "function"},
		{"function f { cat; }", "function"},
		{"cat $(echo /results/a)", "command substitution"},
		{"cat `echo /results/a`", "command substitution"},
		{"cat \"$(cat /results/a)\"", "command substitution"},
		{"x=$(cat /results/a)", "command substitution"},
		{"cat <(cat /results/a)", "process substitution"},
		{"cat /results/a >(cat)", "process substitution"},
		{"cat /results/a &", "background"},
		{"coproc cat", "coproc"},
		{"(( 1 + 1 ))", "arithmetic"},
		{"cat $((1+1))", "arithmetic"},
		{"cat ${x:RANDOM}", "plain numbers"},
		{"[[ -f /results/a ]]", "[[ ]]"},
		{"let x=1", "let"},
		{"time cat /results/a", "time"},
		{"declare x=1", "declare"},
		{"export X=1", "export"},
		{"local x=1", "local"},
		{"readonly X=1", "readonly"},
		{"cat /results/a > /tmp/out", "writes a file"},
		{"cat /results/a >> /tmp/out", "writes a file"},
		{"cat /results/a &> /tmp/out", "writes a file"},
		{"cat /results/a &>> /tmp/out", "writes a file"},
		{"cat /results/a >| /tmp/out", "redirect"},
		{"cat /results/a <> /tmp/out", "redirect"},
		{"cat /results/a 2> /tmp/out", "writes a file"},
		{"cat > /tmp/out <<EOF\nx\nEOF", "writes a file"},
		{"cat /results/a > $HOME/x", "writes a file"},
		{"cat /results/a >&3", "redirect"},
		{"cat $$", "$$"},
		{"cat $RANDOM", "$RANDOM"},
		{"cat $SRANDOM", "$SRANDOM"},
		{"cat $PPID", "$PPID"},
		{"cat ${RANDOM}", "$RANDOM"},
		{"x=RANDOM; cat ${!x}", "${!name}"},
		{"cat ${x[1]}", "array"},
		{"cat ${x@P}", "${name@op}"},
		{"a=xx; b=$a$a", "assignment"},
		{"a=(x y)", "name=value"},
		{"a+=x", "name=value"},
		{"cat {1..1000000}", "brace expansion makes"},
		{"cat {1..100}{1..100}{1..100}", "brace expansion makes"},
		{"cat {a,b,c}{a,b,c}{a,b,c}{a,b,c}{a,b,c}{a,b,c}{a,b,c}{a,b,c}{a,b,c}{a,b,c}{a,b,c}{a,b,c}{a,b,c}{a,b,c}", "brace expansion makes"},
		{"cat 'unterminated", "syntax error"},
		{"cat |", "syntax error"},
		{"if cat; then", "syntax error"},
	}
	for _, c := range cases {
		got := run(t, c.cmd, safetyFiles)
		if !got.Refused || got.ExitCode != 2 || got.Stdout != "" || !strings.Contains(got.Stderr, c.want) {
			t.Errorf("%q: refused %v exit %d stdout %q stderr %q; want a refusal that says %q", c.cmd, got.Refused, got.ExitCode, got.Stdout, got.Stderr, c.want)
		}
	}
}

// A refusal of a loop or a substitution suggests a pipeline and lists the
// commands, so a model can correct itself in one call.
func TestARefusalSuggestsAPipeline(t *testing.T) {
	got := run(t, "for f in /results/*; do grep beta $f; done", safetyFiles)
	for _, want := range []string{"line 1, column 1", "for loop is not supported", "use a pipeline", "cat, grep, head, sed, tail, wc", "grep x /results/*"} {
		if !strings.Contains(got.Stderr, want) {
			t.Errorf("stderr %q lacks %q", got.Stderr, want)
		}
	}
}

// Nothing was written anywhere, even by a script that has a writing redirect
// after something that runs.
func TestARefusedScriptRunsNothing(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out")
	got := run(t, "cat /results/a; cat /results/b > "+target, safetyFiles)
	if !got.Refused || got.Stdout != "" {
		t.Fatalf("got %+v", got)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("the file was created")
	}
}

// What is allowed still works.
func TestAllowedSyntax(t *testing.T) {
	cases := map[string]string{
		"cat /results/a | head -n 1":                   "alpha\n",
		"cat /results/a | grep beta && cat /results/b": "beta\nBeta two\n",
		"x=1; cat /results/b":                          "Beta two\n",
		"x='/results/b'; cat \"$x\"":                   "Beta two\n",
		"cat ${x:-/results/b}":                         "Beta two\n",
		"x=/results/b; cat ${x:0:20}":                  "Beta two\n",
		"cat /results/{a,b} | head -n 3":               "alpha\nbeta\nBeta two\n",
		"cat {1..3} 2>/dev/null; cat /results/b":       "Beta two\n",
		"cat /results/?":                               "alpha\nbeta\nBeta two\n",
		"(cat /results/b)":                             "Beta two\n",
		"cat /results/b | (cat; cat < /results/a)":     "Beta two\nalpha\nbeta\n",
		"cat < /results/b | cat":                       "Beta two\n",
		"cat /results/b | cat | cat | cat":             "Beta two\n",
		"cat /results/b |& cat":                        "Beta two\n",
		"# a comment\ncat /results/b":                  "Beta two\n",
		"cat /results/a | grep -v beta | grep -c .":    "1\n",
		"cat \\\n/results/b":                           "Beta two\n",
		"cat /results/* | head -n 1":                   "alpha\n",
		"cat ~/results/b":                              "Beta two\n",
		"cd=1; cat /results/b":                         "Beta two\n",
		"cat <<'EOF'\n$HOME `x` $(y)\nEOF":             "$HOME `x` $(y)\n",
		"cat <<-EOF\n\t$HOME\n\tEOF":                   "/\n",
	}
	for cmd, want := range cases {
		got := run(t, cmd, safetyFiles)
		if got.Stdout != want || got.ExitCode != 0 || got.Refused {
			t.Errorf("%q: stdout %q (want %q) exit %d refused %v stderr %q", cmd, got.Stdout, want, got.ExitCode, got.Refused, got.Stderr)
		}
	}
}

// head ends a pipeline early: the file behind it is not read to the end, and
// the run does not wait for a file that never ends.
func TestHeadStopsTheUpstreamEarly(t *testing.T) {
	fsys := &endless{}
	for _, cmd := range []string{"cat /inf | head -n 3", "head -n 3 /inf", "head -n 3 < /inf", "cat /inf | grep line | head -n 3", "cat /inf | cat | head -c 100"} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		start := time.Now()
		got, err := bash.Run(ctx, cmd, fsys)
		cancel()
		if err != nil {
			t.Fatalf("%s: %v after %v", cmd, err, time.Since(start))
		}
		if strings.Count(got.Stdout, "\n") < 3 && !strings.Contains(cmd, "-c") {
			t.Errorf("%s: stdout %q", cmd, got.Stdout)
		}
	}
	if n := fsys.read.Load(); n > 64<<20 {
		t.Errorf("the endless file was read for %d bytes", n)
	}
}

// Cancelling ctx ends a pipeline that would never end by itself.
func TestCancellationEndsAPipeline(t *testing.T) {
	for _, cmd := range []string{"cat /inf | grep -c zzz", "grep -c zzz /inf", "cat -n /inf | grep -c zzz", "head -n -1 /inf | grep -c zzz"} {
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
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := bash.Run(ctx, "cat /results/a", mapFS(safetyFiles)); err == nil {
		t.Error("a cancelled ctx ran the command")
	}
}

// The output is bounded: past the bound the pipeline stops and says so.
func TestOutputIsBounded(t *testing.T) {
	got, err := bash.Run(context.Background(), "cat /inf", &endless{}, bash.WithMaxOutput(1000))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || len(got.Stdout) != 1000 {
		t.Fatalf("truncated %v, %d bytes", got.Truncated, len(got.Stdout))
	}
	got = run(t, "cat /results/b", safetyFiles, bash.WithMaxOutput(1000))
	if got.Truncated || got.Stdout != "Beta two\n" {
		t.Fatalf("%+v", got)
	}
	got = run(t, "cat /results/a", safetyFiles, bash.WithMaxOutput(8))
	if !got.Truncated || got.Stdout != "alpha\nbe" {
		t.Fatalf("%+v", got)
	}
}

// The command is bounded too.
func TestALongCommandIsRefused(t *testing.T) {
	got := run(t, "cat "+strings.Repeat("a", bash.MaxCommandBytes), safetyFiles)
	if !got.Refused || !strings.Contains(got.Stderr, "the most is") {
		t.Fatalf("%+v", got)
	}
}

// Input with a NUL byte is refused by grep with an error, not matched.
func TestGrepRefusesBinaryInput(t *testing.T) {
	got := run(t, "grep text /results/bin", map[string]string{"results/bin": "text\x00binary\n"})
	if got.ExitCode != 2 || !strings.Contains(got.Stderr, "binary input") || got.Stdout != "" {
		t.Fatalf("%+v", got)
	}
}

// Every command answers --help with the options it supports, and an option it
// does not support is an error that lists the ones it does.
func TestHelpAndUnsupportedOptions(t *testing.T) {
	for _, name := range bash.Commands() {
		if name == "echo" {
			continue // bash's echo takes no --help and no other option than -n -e -E
		}
		got := run(t, name+" --help", safetyFiles)
		if got.ExitCode != 0 || !strings.HasPrefix(got.Stdout, "Usage: "+name) || !strings.Contains(got.Stdout, "Supported options") {
			t.Errorf("%s --help: %+v", name, got)
		}
		got = run(t, name+" --frobnicate /results/a", safetyFiles)
		if got.ExitCode == 0 || !strings.Contains(got.Stderr, "is not supported. Supported options:") || !strings.Contains(got.Stderr, "--help") {
			t.Errorf("%s --frobnicate: %+v", name, got)
		}
	}
	for cmd, flag := range map[string]string{"cat -A /results/a": "'-A'", "head -z /results/a": "'-z'", "grep -o x /results/a": "'-o'", "grep -r x /results": "'-r'", "grep -P x /results/a": "'-P'", "grep -A 1 x /results/a": "'-A'"} {
		got := run(t, cmd, safetyFiles)
		if got.ExitCode == 0 || !strings.Contains(got.Stderr, "option "+flag+" is not supported") {
			t.Errorf("%s: %+v", cmd, got)
		}
	}
	for cmd, want := range map[string]string{
		"grep -E 'a\\1' /results/a": "back-references",
		"grep '\\<a' /results/a":    "\\< is not supported",
		"head -n 1K /results/a":     "suffixes",
		"head -c -1 /results/a":     "negative byte count",
	} {
		got := run(t, cmd, safetyFiles)
		if got.ExitCode == 0 || !strings.Contains(got.Stderr, want) {
			t.Errorf("%s: %+v (want %q)", cmd, got, want)
		}
	}
}

// The same command over the same files gives the same bytes every time, in
// the order of the file system.
func TestOutputIsDeterministic(t *testing.T) {
	files := conformanceFiles()
	delete(files, "results/bin")
	first := run(t, "cat /results/* | grep -n a | head -n 20", files)
	for range 5 {
		if again := run(t, "cat /results/* | grep -n a | head -n 20", files); again != first {
			t.Fatalf("%+v != %+v", again, first)
		}
	}
}
