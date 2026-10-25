package bash

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

const grepUsage = `Usage: grep [OPTION]... PATTERNS [FILE]...
Search for PATTERNS in each FILE. With no FILE, or when FILE is -, read standard
input; with -r and no FILE, search the root directory. PATTERNS is a basic
regular expression (BRE) unless -E or -F is given.

Supported options:
  -E, --extended-regexp   PATTERNS are extended regular expressions
  -F, --fixed-strings     PATTERNS are strings
  -G, --basic-regexp      PATTERNS are basic regular expressions (the default)
  -e, --regexp=PATTERNS   use PATTERNS for matching; may be given more than once
  -i, --ignore-case       ignore case distinctions
  -w, --word-regexp       match only whole words (not with ^ or $ in a pattern)
  -x, --line-regexp       match only whole lines
  -v, --invert-match      select non-matching lines
  -c, --count             print only a count of selected lines per FILE
  -l, --files-with-matches  print only names of FILEs with selected lines
  -q, --quiet             suppress all normal output
  -o, --only-matching     show only nonempty parts of lines that match
  -n, --line-number       print line number with output lines
  -H, --with-filename     print the file name for each match
  -h, --no-filename       suppress the file name prefix on output
  -r, --recursive         read all files under each directory, in name order
  -A, --after-context=NUM   print NUM lines of trailing context
  -B, --before-context=NUM  print NUM lines of leading context
  -C, --context=NUM       print NUM lines of output context (also -NUM)
      --group-separator=SEP  print SEP on line between matches with context
      --no-group-separator   do not print separator for matches with context
      --help              display this help and exit

Several patterns separated by a newline match if any of them does.
Not yet supported: -m -s -f -L -z -P -b -T -a -I -R, --color and --include.
Back-references and \< \> are not supported in a pattern.
`

var grepOptions = []optSpec{
	{short: 'E', long: "extended-regexp"},
	{short: 'F', long: "fixed-strings"},
	{short: 'G', long: "basic-regexp"},
	{short: 'e', long: "regexp", arg: true},
	{short: 'i', long: "ignore-case"},
	{short: 'w', long: "word-regexp"},
	{short: 'v', long: "invert-match"},
	{short: 'x', long: "line-regexp"},
	{short: 'c', long: "count"},
	{short: 'l', long: "files-with-matches"},
	{short: 'q', long: "quiet"},
	{short: 'o', long: "only-matching"},
	{short: 'n', long: "line-number"},
	{short: 'H', long: "with-filename"},
	{short: 'h', long: "no-filename"},
	{short: 'r', long: "recursive"},
	{short: 'A', long: "after-context", arg: true},
	{short: 'B', long: "before-context", arg: true},
	{short: 'C', long: "context", arg: true},
	{long: "group-separator", arg: true},
	{long: "no-group-separator"},
}

// grepMatcher finds the parts of a line a pattern matches. With word set it
// keeps only the matches that GNU grep -w keeps: those with no word character
// on either side.
type grepMatcher struct {
	re         *regexp.Regexp
	word       bool
	anch, full *regexp.Regexp // the pattern anchored at the start, and at both ends, for -w
}

func (m *grepMatcher) matchLine(text []byte) bool {
	if !m.re.Match(text) {
		return false
	}
	if !m.word {
		return true
	}
	_, _, ok := m.findWord(text, 0)
	return ok
}

// spans returns the nonempty matches in a line, left to right, not
// overlapping.
func (m *grepMatcher) spans(text []byte) [][2]int {
	var out [][2]int
	if !m.word {
		for _, loc := range m.re.FindAllIndex(text, -1) {
			if loc[1] > loc[0] {
				out = append(out, [2]int{loc[0], loc[1]})
			}
		}
		return out
	}
	for from := 0; from <= len(text); {
		s, e, ok := m.findWord(text, from)
		if !ok {
			break
		}
		if e == s {
			_, size := utf8.DecodeRune(text[e:])
			from = e + max(size, 1)
			continue
		}
		out = append(out, [2]int{s, e})
		from = e
	}
	return out
}

func isWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// findWord looks for a match from position from that is a whole word: GNU
// grep tries the longest match at each start, then shorter ones at the same
// start, then the next start.
func (m *grepMatcher) findWord(text []byte, from int) (int, int, bool) {
	rightOK := func(e int) bool {
		if e >= len(text) {
			return true
		}
		r, _ := utf8.DecodeRune(text[e:])
		return !isWordRune(r)
	}
	for s := from; s <= len(text); {
		_, size := utf8.DecodeRune(text[min(s, len(text)):])
		if s < len(text) && size == 0 {
			size = 1
		}
		leftOK := true
		if s > 0 {
			r, _ := utf8.DecodeLastRune(text[:s])
			leftOK = !isWordRune(r)
		}
		if leftOK {
			if loc := m.anch.FindIndex(text[s:]); loc != nil {
				e := s + loc[1]
				if rightOK(e) {
					return s, e, true
				}
				for e2 := e - 1; e2 >= s; e2-- {
					if e2 < len(text) && !utf8.RuneStart(text[e2]) {
						continue
					}
					if rightOK(e2) && m.full.Match(text[s:e2]) {
						return s, e2, true
					}
				}
			}
		}
		if s >= len(text) {
			break
		}
		s += max(size, 1)
	}
	return 0, 0, false
}

type grepRun struct {
	out                             *output
	m                               *grepMatcher
	invert, count, listFiles, quiet bool
	lineNo, onlyMatching            bool
	before, after                   int // -1 when not given
	sep                             string
	noSep                           bool
	used                            bool // something was written, in this or an earlier file
	prefixName                      bool
	name                            string
	lastPrinted                     int
	afterLeft                       int
	ring                            []ctxLine
	ringStart                       int
}

type ctxLine struct {
	no   int
	text []byte
}

func (g *grepRun) contextOn() bool { return g.before >= 0 || g.after >= 0 }

// writeLine writes one line (or one match of it) with its prefix: the name,
// the line number, and ':' for a selected line or '-' for a context line.
func (g *grepRun) writeLine(no int, text []byte, selected bool) {
	sep := byte(':')
	if !selected {
		sep = '-'
	}
	if g.prefixName {
		_, _ = g.out.WriteString(g.name)
		_ = g.out.WriteByte(sep)
	}
	if g.lineNo {
		_, _ = g.out.WriteString(strconv.Itoa(no))
		_ = g.out.WriteByte(sep)
	}
	_, _ = g.out.Write(text)
	_ = g.out.WriteByte('\n')
}

// emit writes a line that is selected or is context. With -o a context line
// writes nothing, and a selected line writes its matches.
func (g *grepRun) emit(no int, text []byte, selected bool) {
	if g.onlyMatching {
		if selected && !g.invert {
			for _, sp := range g.m.spans(text) {
				g.writeLine(no, text[sp[0]:sp[1]], true)
			}
		}
	} else {
		g.writeLine(no, text, selected)
	}
	g.lastPrinted = no
	g.used = true
}

func runGrep(ctx context.Context, c *Call) int {
	if hasHelp(c.Args) {
		return c.help(grepUsage)
	}
	args := make([]string, len(c.Args))
	for i, a := range c.Args {
		if a == "--" {
			copy(args[i:], c.Args[i:])
			break
		}
		// -NUM is -C NUM.
		if len(a) > 1 && a[0] == '-' && isDigits(a[1:]) {
			a = "-C" + a[1:]
		}
		args[i] = a
	}
	opts, operands, err := getopt("grep", grepOptions, args)
	if err != nil {
		_, _ = fmt.Fprintln(c.Stderr, err)
		return 2
	}
	var (
		patterns            []string
		havePattern         bool
		syn                 = syntaxBRE
		ignoreCase, wholeLn bool
		word, recursive     bool
		withName, noName    bool
		g                   = &grepRun{before: -1, after: -1, sep: "--"}
		defaultCtx          = -1
	)
	ctxArg := func(value string) (int, bool) {
		if !isDigits(value) {
			c.errf("grep", "%s: invalid context length argument", value)
			return 0, false
		}
		return int(min(parseUint(value), 1<<30)), true
	}
	for _, opt := range opts {
		var ok = true
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
		case "word-regexp":
			word = true
		case "invert-match":
			g.invert = true
		case "line-regexp":
			wholeLn = true
		case "count":
			g.count = true
		case "files-with-matches":
			g.listFiles = true
		case "quiet":
			g.quiet = true
		case "only-matching":
			g.onlyMatching = true
		case "line-number":
			g.lineNo = true
		case "with-filename":
			withName, noName = true, false
		case "no-filename":
			noName, withName = true, false
		case "recursive":
			recursive = true
		case "after-context":
			g.after, ok = ctxArg(opt.value)
		case "before-context":
			g.before, ok = ctxArg(opt.value)
		case "context":
			defaultCtx, ok = ctxArg(opt.value)
		case "group-separator":
			g.sep, g.noSep = opt.value, false
		case "no-group-separator":
			g.noSep = true
		}
		if !ok {
			return 2
		}
	}
	// -A and -B win over -C, in whatever order they are written.
	if g.after < 0 {
		g.after = defaultCtx
	}
	if g.before < 0 {
		g.before = defaultCtx
	}
	if g.count || g.listFiles || g.quiet {
		g.before, g.after = -1, -1
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
	implicitRoot := false
	if len(files) == 0 {
		if recursive {
			files, implicitRoot = []string{"."}, true
		} else {
			files = []string{"-"}
		}
	}
	multiple := len(files) > 1

	expr, err := patternExpr(patterns, syn, wholeLn)
	if err != nil {
		c.errf("grep", "%s", err)
		return 2
	}
	re, err := compileExpr(expr, ignoreCase)
	if err != nil {
		c.errf("grep", "%s", err)
		return 2
	}
	g.m = &grepMatcher{re: re}
	if word && !wholeLn {
		if hasAnchor(expr) {
			c.errf("grep", "-w is not supported with ^ or $ in a pattern: a word match already stops at the word edges, and -x matches whole lines")
			return 2
		}
		g.m.word = true
		if g.m.anch, err = compileExpr("^(?:"+expr+")", ignoreCase); err == nil {
			g.m.full, err = compileExpr("^(?:"+expr+")$", ignoreCase)
		}
		if err != nil {
			c.errf("grep", "%s", err)
			return 2
		}
	}

	g.out = newOutput(c.Stdout)
	defer func() { _ = g.out.Flush() }()
	matched, failed := false, false

	var searchFile func(name, display string, descended bool) bool // returns false to stop
	searchFile = func(name, display string, descended bool) bool {
		in, err := c.open(name)
		if errors.Is(err, syscall.EISDIR) && recursive {
			return walkDir(ctx, c, name, display, implicitRoot && name == ".", func(child, childDisplay string) bool {
				return searchFile(child, childDisplay, true)
			}, &failed)
		}
		if err != nil {
			c.errf("grep", "%s: %s", display, errText(err))
			failed = true
			return true
		}
		if name == "-" {
			display = "(standard input)"
		}
		g.name = display
		g.prefixName = (multiple || withName || descended) && !noName
		found, err := g.search(ctx, in)
		_ = in.Close()
		matched = matched || found
		if err != nil {
			if g.out.failed() || ctx.Err() != nil {
				failed = true
				return false
			}
			c.errf("grep", "%s: %s", display, errText(err))
			failed = true
		}
		return !found || !g.quiet
	}
	for _, name := range files {
		if !searchFile(name, name, false) {
			break
		}
	}
	if ctx.Err() != nil {
		return 2
	}
	if err := g.out.Flush(); err != nil && (!matched || !g.quiet) {
		return 2
	}
	switch {
	case matched && g.quiet:
		return 0
	case failed:
		return 2
	case matched:
		return 0
	}
	return 1
}

// walkDir visits the files under a directory in name order, calling visit
// with the name to open and the name to show. A name is the directory's, a
// slash and the entry's; under the implicit root "." the entries are shown
// without a prefix, as GNU grep -r does when it is given no file.
func walkDir(ctx context.Context, c *Call, dir, display string, implicit bool, visit func(name, display string) bool, failed *bool) bool {
	entries, err := fs.ReadDir(c.FS, fsName(c.Dir, dir))
	if err != nil {
		c.errf("grep", "%s: %s", display, errText(errnoOf(err)))
		*failed = true
		return true
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			return false
		}
		child := strings.TrimSuffix(dir, "/") + "/" + e.Name()
		shown := strings.TrimSuffix(display, "/") + "/" + e.Name()
		if implicit {
			shown = e.Name()
		}
		if e.IsDir() {
			if !walkDir(ctx, c, child, shown, false, visit, failed) {
				return false
			}
			continue
		}
		if !visit(child, shown) {
			return false
		}
	}
	return true
}

var errBinary = errors.New("binary input (it contains a NUL byte) is not supported")

// search selects the lines of in and writes them as grep does. It reports
// whether any line was selected, and stops reading when -q or -l has its
// answer.
func (g *grepRun) search(ctx context.Context, in io.Reader) (bool, error) {
	reader := bufio.NewReaderSize(in, 32<<10)
	if head, _ := reader.Peek(32 << 10); bytes.IndexByte(head, 0) >= 0 {
		return false, errBinary
	}
	count := 0
	g.lastPrinted, g.afterLeft = 0, 0
	g.ring, g.ringStart = g.ring[:0], 0
	for lineNo := 1; ; lineNo++ {
		if lineNo&0x3ff == 0 {
			if err := ctx.Err(); err != nil {
				return count > 0, err
			}
		}
		line, err := readLine(reader)
		if len(line) > 0 || err == nil {
			text := bytes.TrimSuffix(line, []byte("\n"))
			if g.m.matchLine(text) != g.invert {
				count++
				if g.quiet || g.listFiles {
					break
				}
				if !g.count {
					g.selected(lineNo, text)
					if g.out.failed() {
						return count > 0, io.ErrClosedPipe
					}
				}
			} else if !g.count && g.contextOn() {
				g.unselected(lineNo, text)
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
	case g.quiet:
	case g.listFiles:
		if count > 0 {
			_, _ = g.out.WriteString(g.name + "\n")
		}
	case g.count:
		if g.prefixName {
			_, _ = g.out.WriteString(g.name + ":")
		}
		_, _ = g.out.WriteString(strconv.Itoa(count) + "\n")
	}
	return count > 0, nil
}

// selected writes a selected line, with the context before it, and a group
// separator when it does not follow what was written last.
func (g *grepRun) selected(no int, text []byte) {
	pending := g.ring[g.ringStart:]
	first := no
	if len(pending) > 0 {
		first = pending[0].no
	}
	if g.contextOn() && g.used && !g.noSep && (g.lastPrinted == 0 || first != g.lastPrinted+1) {
		_, _ = g.out.WriteString(g.sep + "\n")
	}
	for _, p := range pending {
		g.emit(p.no, p.text, false)
	}
	g.ring, g.ringStart = g.ring[:0], 0
	g.emit(no, text, true)
	g.afterLeft = max(g.after, 0)
}

// unselected handles a line that is not selected: it is trailing context
// while the count lasts, and leading context kept for the next selected line
// otherwise.
func (g *grepRun) unselected(no int, text []byte) {
	if g.afterLeft > 0 {
		g.afterLeft--
		g.emit(no, text, false)
		return
	}
	if g.before > 0 {
		g.ring = append(g.ring, ctxLine{no, text})
		if len(g.ring)-g.ringStart > g.before {
			g.ring[g.ringStart] = ctxLine{}
			g.ringStart++
		}
		if g.ringStart > 1024 && g.ringStart > len(g.ring)/2 {
			g.ring = append(g.ring[:0], g.ring[g.ringStart:]...)
			g.ringStart = 0
		}
	}
}
