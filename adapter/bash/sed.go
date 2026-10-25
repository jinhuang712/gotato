package bash

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"syscall"
)

const sedUsage = `Usage: sed -n [OPTION]... {script-only-if-no-other-script} [input-file]...

Print the lines of the input that a script of line-number addresses selects.

Supported options:
  -n, --quiet, --silent   suppress automatic printing of the pattern space
  -e, --expression=SCRIPT add the script to the commands to be executed
  -s, --separate          consider files as separate rather than as a single
                          continuous long stream
      --help              display this help and exit

Only -n is supported (without it every line is printed, and sed -n is how a
selection is made). A script is one or more commands separated by ; or a
newline, each one of:
  p         print every line
  Np        print line N
  N,Mp      print lines N to M
  N,$p      print line N to the last line
  $p        print the last line
Regular expression addresses, N~M, addr,+N, !, s, d, q and every other command
are not supported. To search for text, use grep.
`

var sedOptions = []optSpec{
	{short: 'n', long: "quiet"},
	{long: "silent"},
	{short: 'e', long: "expression", arg: true},
	{short: 's', long: "separate"},
}

// sedAddr is a line address: a number, or the last line.
type sedAddr struct {
	n    uint64
	last bool
}

// sedCmd is one command "addr1[,addr2]p".
type sedCmd struct {
	a1     sedAddr
	a2     sedAddr
	hasA2  bool
	all    bool // no address: every line
	active bool // inside a range
}

func (a sedAddr) matches(line uint64, isLast bool) bool {
	if a.last {
		return isLast
	}
	return a.n == line
}

// sedUnsupported is the error for a script outside the subset.
func sedUnsupported(script, why string) error {
	return fmt.Errorf("sed: unsupported script '%s': %s. Supported: sed -n with commands Np, N,Mp, N,$p and $p, joined with ; or newline. To search for text, use grep", script, why)
}

// parseSedScript parses the subset: commands of line-number addresses and p.
func parseSedScript(script string) ([]*sedCmd, error) {
	var cmds []*sedCmd
	i := 0
	skipBlank := func() {
		for i < len(script) && (script[i] == ' ' || script[i] == '\t') {
			i++
		}
	}
	addr := func() (sedAddr, error) {
		skipBlank()
		switch {
		case i < len(script) && script[i] == '$':
			i++
			return sedAddr{last: true}, nil
		case i < len(script) && script[i] >= '0' && script[i] <= '9':
			j := i
			for j < len(script) && script[j] >= '0' && script[j] <= '9' {
				j++
			}
			n := parseUint(script[i:j])
			i = j
			if i < len(script) && script[i] == '~' {
				return sedAddr{}, sedUnsupported(script, "the step address first~step is not supported")
			}
			return sedAddr{n: n}, nil
		case i < len(script) && (script[i] == '/' || script[i] == '\\'):
			return sedAddr{}, sedUnsupported(script, "regular expression addresses are not supported")
		}
		return sedAddr{}, sedUnsupported(script, "a command must start with a line number or $")
	}
	for {
		// separators between commands
		for i < len(script) && (script[i] == ';' || script[i] == '\n' || script[i] == ' ' || script[i] == '\t') {
			i++
		}
		if i >= len(script) {
			break
		}
		cmd := &sedCmd{}
		var err error
		if ch := script[i]; ch == 'p' {
			cmd.all = true
		} else if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch == '{' || ch == '}' || ch == '#' || ch == '!' {
			return nil, sedUnsupported(script, fmt.Sprintf("the command '%c' is not supported", ch))
		} else if cmd.a1, err = addr(); err != nil {
			return nil, err
		}
		skipBlank()
		if !cmd.all && i < len(script) && script[i] == ',' {
			i++
			skipBlank()
			if i < len(script) && script[i] == '+' {
				return nil, sedUnsupported(script, "addr,+N is not supported")
			}
			if i < len(script) && script[i] == '~' {
				return nil, sedUnsupported(script, "addr,~N is not supported")
			}
			if cmd.a2, err = addr(); err != nil {
				return nil, err
			}
			cmd.hasA2 = true
		}
		skipBlank()
		if i < len(script) && script[i] == '!' {
			return nil, sedUnsupported(script, "! is not supported")
		}
		if i >= len(script) {
			return nil, fmt.Errorf("sed: -e expression #1, char %d: missing command", len(script))
		}
		if script[i] != 'p' {
			return nil, sedUnsupported(script, fmt.Sprintf("the command '%c' is not supported", script[i]))
		}
		i++
		pEnd := i
		skipBlank()
		if i < len(script) && script[i] != ';' && script[i] != '\n' {
			return nil, sedUnsupported(script, fmt.Sprintf("extra characters after the command: '%s'", script[i:]))
		}
		if !cmd.all && !cmd.a1.last && cmd.a1.n == 0 || cmd.hasA2 && !cmd.a2.last && cmd.a2.n == 0 {
			return nil, fmt.Errorf("sed: -e expression #1, char %d: invalid usage of line address 0", pEnd)
		}
		cmds = append(cmds, cmd)
	}
	return cmds, nil
}

// selects reports whether the command selects line number n, updating its
// range state, as sed does: a range ends at its last line, and an end address
// that is not after the start selects one line.
func (cmd *sedCmd) selects(n uint64, isLast bool) bool {
	if cmd.all {
		return true
	}
	if !cmd.hasA2 {
		return cmd.a1.matches(n, isLast)
	}
	if cmd.active {
		if cmd.a2.matches(n, isLast) {
			cmd.active = false
		}
		return true
	}
	if !cmd.a1.matches(n, isLast) {
		return false
	}
	if cmd.a2.last {
		cmd.active = !isLast
	} else {
		cmd.active = cmd.a2.n > n
	}
	return true
}

// lineSource reads the lines of the input files one after the other, one line
// ahead, so the last line is known when it is read.
type lineSource struct {
	ctx    context.Context
	c      *Call
	files  []string
	next   int
	cur    io.ReadCloser
	curRd  *lineReader
	pend   []byte
	have   bool
	status int
}

// lineReader is a line-by-line reader that keeps its own buffer.
type lineReader struct {
	r    io.Reader
	buf  []byte
	pos  int
	end  int
	done bool
}

func newLineReader(r io.Reader) *lineReader { return &lineReader{r: r, buf: make([]byte, 32<<10)} }

func (l *lineReader) readLine() ([]byte, error) {
	var line []byte
	for {
		if l.pos < l.end {
			chunk := l.buf[l.pos:l.end]
			for i, b := range chunk {
				if b == '\n' {
					line = append(line, chunk[:i+1]...)
					l.pos += i + 1
					return line, nil
				}
			}
			line = append(line, chunk...)
			l.pos = l.end
			if len(line) > MaxLineBytes {
				return nil, errLineTooLong
			}
		}
		if l.done {
			if len(line) == 0 {
				return nil, io.EOF
			}
			return line, nil
		}
		n, err := l.r.Read(l.buf)
		l.pos, l.end = 0, n
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return nil, err
			}
			l.done = true
		}
	}
}

// fill makes pend the next line, opening the next file when the current one
// is finished. A file that cannot be read is reported and skipped.
func (s *lineSource) fill() {
	for !s.have {
		if err := s.ctx.Err(); err != nil {
			return
		}
		if s.curRd == nil {
			if s.next >= len(s.files) {
				return
			}
			name := s.files[s.next]
			s.next++
			in, err := s.c.open(name)
			if err != nil {
				if errors.Is(err, syscall.EISDIR) {
					s.c.errf("sed", "read error on %s: %s", name, errText(err))
				} else {
					s.c.errf("sed", "can't read %s: %s", name, errText(err))
				}
				s.status = 2
				continue
			}
			s.cur, s.curRd = in, newLineReader(in)
		}
		line, err := s.curRd.readLine()
		if err != nil {
			_ = s.cur.Close()
			s.cur, s.curRd = nil, nil
			if !errors.Is(err, io.EOF) {
				s.c.errf("sed", "read error on %s: %s", s.files[s.next-1], errText(err))
				s.status = 2
			}
			continue
		}
		s.pend, s.have = line, true
		return
	}
}

func runSed(ctx context.Context, c *Call) int {
	if hasHelp(c.Args) {
		return c.help(sedUsage)
	}
	opts, operands, err := getopt("sed", sedOptions, c.Args)
	if err != nil {
		_, _ = fmt.Fprintln(c.Stderr, err)
		return 1
	}
	quiet, separate := false, false
	var scripts []string
	for _, opt := range opts {
		switch opt.id {
		case "quiet", "silent":
			quiet = true
		case "expression":
			scripts = append(scripts, opt.value)
		case "separate":
			separate = true
		}
	}
	if len(scripts) == 0 {
		if len(operands) == 0 {
			_, _ = io.WriteString(c.Stderr, "Usage: sed -n [OPTION]... {script-only-if-no-other-script} [input-file]...\nTry 'sed --help' for more information.\n")
			return 1
		}
		scripts, operands = []string{operands[0]}, operands[1:]
	}
	if !quiet {
		c.errf("sed", "without -n every line is printed, and only selection by line number is supported: write sed -n 'Np' or sed -n 'N,Mp'")
		return 1
	}
	cmds, perr := parseSedScript(strings.Join(scripts, "\n"))
	if perr != nil {
		_, _ = fmt.Fprintln(c.Stderr, perr)
		return 1
	}
	// Reading can stop after the last line any command can select.
	limit, bounded := uint64(0), true
	for _, cmd := range cmds {
		if cmd.all {
			bounded = false
		}
		for _, a := range []sedAddr{cmd.a1, cmd.a2} {
			if a.last {
				bounded = false
			}
			limit = max(limit, a.n)
		}
	}

	files := stdinOrFiles(operands)
	out := newOutput(c.Stdout)
	defer func() { _ = out.Flush() }()
	status := 0
	missingNL := false
	runStream := func(files []string) {
		src := &lineSource{ctx: ctx, c: c, files: files}
		var n uint64
		for {
			src.fill()
			if !src.have {
				break
			}
			line := src.pend
			src.have = false
			src.fill() // one line ahead, to know the last line
			isLast := !src.have
			n++
			for _, cmd := range cmds {
				if !cmd.selects(n, isLast) {
					continue
				}
				// each command that selects the line prints it
				text, hadNL := stripNL(line)
				if missingNL {
					_ = out.WriteByte('\n')
				}
				_, _ = out.Write(text)
				if hadNL {
					_ = out.WriteByte('\n')
				}
				missingNL = !hadNL
				if out.failed() {
					break
				}
			}
			if out.failed() || bounded && n >= limit && !anyActive(cmds) {
				break
			}
		}
		if src.cur != nil {
			_ = src.cur.Close()
		}
		if src.status != 0 {
			status = src.status
		}
	}
	if separate {
		for _, f := range files {
			for _, cmd := range cmds {
				cmd.active = false
			}
			runStream([]string{f})
		}
	} else {
		runStream(files)
	}
	if ctx.Err() != nil || out.failed() {
		return 4
	}
	if err := out.Flush(); err != nil {
		return 4
	}
	return status
}

func anyActive(cmds []*sedCmd) bool {
	for _, cmd := range cmds {
		if cmd.active {
			return true
		}
	}
	return false
}
