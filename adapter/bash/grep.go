package bash

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

const grepUsage = `Usage: grep [OPTION]... PATTERNS [FILE]...
Search for PATTERNS in each FILE. With no FILE, or when FILE is -, read standard
input. PATTERNS is a basic regular expression (BRE) unless -E or -F is given.

Supported options:
  -E, --extended-regexp   PATTERNS are extended regular expressions
  -F, --fixed-strings     PATTERNS are strings
  -G, --basic-regexp      PATTERNS are basic regular expressions (the default)
  -e, --regexp=PATTERNS   use PATTERNS for matching; may be given more than once
  -i, --ignore-case       ignore case distinctions
  -v, --invert-match      select non-matching lines
  -x, --line-regexp       match only whole lines
  -c, --count             print only a count of selected lines per FILE
  -l, --files-with-matches  print only names of FILEs with selected lines
  -q, --quiet             suppress all normal output
  -n, --line-number       print line number with output lines
  -H, --with-filename     print the file name for each match
  -h, --no-filename       suppress the file name prefix on output
      --help              display this help and exit

Several patterns separated by a newline match if any of them does.
Not yet supported: -o -w -r -A -B -C -m -s -f -L -z -P and --color.
Back-references and \< \> are not supported in a pattern.
`

var grepOptions = []optSpec{
	{short: 'E', long: "extended-regexp"},
	{short: 'F', long: "fixed-strings"},
	{short: 'G', long: "basic-regexp"},
	{short: 'e', long: "regexp", arg: true},
	{short: 'i', long: "ignore-case"},
	{short: 'v', long: "invert-match"},
	{short: 'x', long: "line-regexp"},
	{short: 'c', long: "count"},
	{short: 'l', long: "files-with-matches"},
	{short: 'q', long: "quiet"},
	{short: 'n', long: "line-number"},
	{short: 'H', long: "with-filename"},
	{short: 'h', long: "no-filename"},
}

type grepFlags struct {
	invert, count, listFiles, quiet, lineNo bool
	prefix                                  bool
}

func runGrep(ctx context.Context, c *Call) int {
	if hasHelp(c.Args) {
		return c.help(grepUsage)
	}
	opts, operands, err := getopt("grep", grepOptions, c.Args)
	if err != nil {
		_, _ = fmt.Fprintln(c.Stderr, err)
		return 2
	}
	var (
		patterns            []string
		havePattern         bool
		syn                 = syntaxBRE
		ignoreCase, wholeLn bool
		flags               grepFlags
		withName, noName    bool
	)
	for _, opt := range opts {
		switch opt.id {
		case "extended-regexp":
			syn = syntaxERE
		case "fixed-strings":
			syn = syntaxFixed
		case "basic-regexp":
			syn = syntaxBRE
		case "regexp":
			patterns = append(patterns, strings.Split(opt.value, "\n")...)
			havePattern = true
		case "ignore-case":
			ignoreCase = true
		case "invert-match":
			flags.invert = true
		case "line-regexp":
			wholeLn = true
		case "count":
			flags.count = true
		case "files-with-matches":
			flags.listFiles = true
		case "quiet":
			flags.quiet = true
		case "line-number":
			flags.lineNo = true
		case "with-filename":
			withName, noName = true, false
		case "no-filename":
			noName, withName = true, false
		}
	}
	if !havePattern {
		if len(operands) == 0 {
			_, _ = fmt.Fprint(c.Stderr, "Usage: grep [OPTION]... PATTERNS [FILE]...\nTry 'grep --help' for more information.\n")
			return 2
		}
		patterns = strings.Split(operands[0], "\n")
		operands = operands[1:]
	}
	files := operands
	if len(files) == 0 {
		files = []string{"-"}
	}
	flags.prefix = (len(files) > 1 || withName) && !noName

	re, err := compilePatterns(patterns, syn, ignoreCase, wholeLn)
	if err != nil {
		c.errf("grep", "%s", err)
		return 2
	}

	out := newOutput(c.Stdout)
	defer func() { _ = out.Flush() }()
	matched, failed := false, false
	for _, name := range files {
		in, err := c.open(name)
		if err != nil {
			c.errf("grep", "%s: %s", name, errText(err))
			failed = true
			continue
		}
		display := name
		if name == "-" {
			display = "(standard input)"
		}
		found, err := grepReader(ctx, in, out, re, display, flags)
		_ = in.Close()
		matched = matched || found
		if err != nil {
			if out.failed() || ctx.Err() != nil {
				return 2
			}
			c.errf("grep", "%s: %s", display, errText(err))
			failed = true
		}
		if found && flags.quiet {
			break
		}
	}
	if err := out.Flush(); err != nil && (!matched || !flags.quiet) {
		return 2
	}
	switch {
	case matched && flags.quiet:
		return 0
	case failed:
		return 2
	case matched:
		return 0
	}
	return 1
}

var errBinary = errors.New("binary input (it contains a NUL byte) is not supported")

// grepReader selects the lines of in and writes them as grep does. It reports
// whether any line was selected, and stops reading when -q or -l has its answer.
func grepReader(ctx context.Context, in io.Reader, out *output, re *regexp.Regexp, name string, f grepFlags) (bool, error) {
	reader := bufio.NewReaderSize(in, 32<<10)
	if head, _ := reader.Peek(32 << 10); bytes.IndexByte(head, 0) >= 0 {
		return false, errBinary
	}
	count := 0
	for lineNo := 1; ; lineNo++ {
		if lineNo&0x3ff == 0 {
			if err := ctx.Err(); err != nil {
				return count > 0, err
			}
		}
		line, err := readLine(reader)
		if len(line) > 0 || err == nil {
			text := bytes.TrimSuffix(line, []byte("\n"))
			if re.Match(text) != f.invert {
				count++
				if f.quiet || f.listFiles {
					break
				}
				if !f.count {
					if f.prefix {
						_, _ = out.WriteString(name)
						_ = out.WriteByte(':')
					}
					if f.lineNo {
						_, _ = out.WriteString(strconv.Itoa(lineNo))
						_ = out.WriteByte(':')
					}
					_, _ = out.Write(text)
					_ = out.WriteByte('\n')
					if out.failed() {
						return count > 0, io.ErrClosedPipe
					}
				}
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return count > 0, err
			}
			break
		}
	}
	switch {
	case f.quiet:
	case f.listFiles:
		if count > 0 {
			_, _ = out.WriteString(name + "\n")
		}
	case f.count:
		if f.prefix {
			_, _ = out.WriteString(name + ":")
		}
		_, _ = out.WriteString(strconv.Itoa(count) + "\n")
	}
	return count > 0, nil
}
