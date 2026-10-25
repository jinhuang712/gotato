package bash

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

const wcUsage = `Usage: wc [OPTION]... [FILE]...
Print newline, word, and byte counts for each FILE, and a total line if more
than one FILE is specified. With no FILE, or when FILE is -, read standard input.

Supported options:
  -c, --bytes   print the byte counts
  -m, --chars   print the character counts
  -l, --lines   print the newline counts
  -w, --words   print the word counts
      --help    display this help and exit

The counts are printed in this order: newline, word, character, byte. With no
option, the newline, word and byte counts are printed. Characters are UTF-8.
`

var wcOptions = []optSpec{
	{short: 'c', long: "bytes"},
	{short: 'm', long: "chars"},
	{short: 'l', long: "lines"},
	{short: 'w', long: "words"},
}

type wcCounts struct {
	lines, words, chars, bytes uint64
}

// wcSpace reports whether r ends a word as GNU wc decides it in a UTF-8
// locale: the white space of the C library (without the no-break spaces,
// which wc adds back as separators).
func wcSpace(r rune) bool {
	switch {
	case r >= 0x09 && r <= 0x0d, r == 0x20, r == 0x1680, r >= 0x2000 && r <= 0x2006, r >= 0x2008 && r <= 0x200a,
		r == 0x2028, r == 0x2029, r == 0x205f, r == 0x3000,
		r == 0xa0, r == 0x2007, r == 0x202f, r == 0x2060:
		return true
	}
	return false
}

// wcCount counts a stream. Lines are newlines, words are runs of printable
// non-space characters, characters are UTF-8 sequences (a byte that is not
// part of one is not counted), bytes are bytes.
func wcCount(ctx context.Context, in io.Reader, chars, words bool) (wcCounts, error) {
	var total wcCounts
	buf := make([]byte, 32<<10)
	have := 0 // bytes carried over from the last read: the start of a character
	inWord := false
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		got, err := in.Read(buf[have:])
		total.bytes += uint64(got)
		data := buf[:have+got]
		eof := err != nil
		i := 0
		for i < len(data) {
			if data[i] == '\n' {
				total.lines++
			}
			if !chars && !words {
				i++
				continue
			}
			if data[i] < utf8.RuneSelf {
				r := rune(data[i])
				i++
				total.chars++
				switch {
				case wcSpace(r):
					if inWord {
						total.words++
					}
					inWord = false
				case unicode.IsPrint(r):
					inWord = true
				}
				continue
			}
			if !eof && !utf8.FullRune(data[i:]) {
				break
			}
			r, size := utf8.DecodeRune(data[i:])
			i += size
			if r == utf8.RuneError && size == 1 {
				continue
			}
			total.chars++
			switch {
			case wcSpace(r):
				if inWord {
					total.words++
				}
				inWord = false
			case unicode.IsPrint(r):
				inWord = true
			}
		}
		have = copy(buf, data[i:])
		if err != nil {
			if errors.Is(err, io.EOF) {
				if inWord {
					total.words++
				}
				return total, nil
			}
			return total, err
		}
	}
}

func runWc(ctx context.Context, c *Call) int {
	if hasHelp(c.Args) {
		return c.help(wcUsage)
	}
	opts, files, err := getopt("wc", wcOptions, c.Args)
	if err != nil {
		_, _ = fmt.Fprintln(c.Stderr, err)
		return 1
	}
	var showL, showW, showM, showC bool
	for _, opt := range opts {
		switch opt.id {
		case "lines":
			showL = true
		case "words":
			showW = true
		case "chars":
			showM = true
		case "bytes":
			showC = true
		}
	}
	if !showL && !showW && !showM && !showC {
		showL, showW, showC = true, true, true
	}
	shown := 0
	for _, b := range []bool{showL, showW, showM, showC} {
		if b {
			shown++
		}
	}
	explicit := len(files)
	files = stdinOrFiles(files)

	width := wcWidth(c, files, shown)
	out := newOutput(c.Stdout)
	defer func() { _ = out.Flush() }()
	write := func(n wcCounts, name string) {
		first := true
		field := func(v uint64) {
			if !first {
				_ = out.WriteByte(' ')
			}
			first = false
			s := strconv.FormatUint(v, 10)
			_, _ = out.WriteString(strings.Repeat(" ", max(0, width-len(s))) + s)
		}
		if showL {
			field(n.lines)
		}
		if showW {
			field(n.words)
		}
		if showM {
			field(n.chars)
		}
		if showC {
			field(n.bytes)
		}
		if name != "" {
			_ = out.WriteByte(' ')
			if strings.Contains(name, "\n") {
				name = quoteAlways(name)
			}
			_, _ = out.WriteString(name)
		}
		_ = out.WriteByte('\n')
	}

	status := 0
	var sum wcCounts
	for _, name := range files {
		in, err := c.open(name)
		isDir := errors.Is(err, syscall.EISDIR)
		if err != nil && !isDir {
			c.errf("wc", "%s: %s", quoteName(name), errText(err))
			status = 1
			continue
		}
		var n wcCounts
		if isDir {
			c.errf("wc", "%s: %s", quoteName(name), errText(err))
			status = 1
		} else {
			n, err = wcCount(ctx, in, showM, showW)
			_ = in.Close()
			if err != nil {
				if out.failed() || ctx.Err() != nil {
					return 1
				}
				c.errf("wc", "%s: %s", quoteName(name), errText(err))
				status = 1
			}
		}
		sum.lines += n.lines
		sum.words += n.words
		sum.chars += n.chars
		sum.bytes += n.bytes
		label := name
		if explicit == 0 {
			label = ""
		}
		write(n, label)
	}
	if explicit > 1 {
		write(sum, "total")
	}
	if err := out.Flush(); err != nil {
		return 1
	}
	return status
}

// wcWidth is the width of every number, as GNU wc computes it: 1 for a single
// count of a single input; else the digits of the total size of the regular
// files that exist (a missing one is left out), or at least 7 when an input is not a regular file (standard input,
// a directory). Standard input is always taken for a pipe: GNU wc pads a
// redirect from a file (wc < file) to the digits of its size, which the
// interpreter does not let a command see.
func wcWidth(c *Call, files []string, shown int) int {
	if len(files) == 1 && shown == 1 {
		return 1
	}
	stat := func(name string) (fs.FileInfo, bool) {
		if name == "-" {
			return nil, false // standard input is a pipe: the interpreter copies a redirect through one
		}
		info, err := fs.Stat(c.FS, fsName(c.Dir, name))
		return info, err == nil
	}
	minimum := 1
	var total uint64
	for _, name := range files {
		if name == "-" {
			minimum = 7
			continue
		}
		info, ok := stat(name)
		if !ok {
			continue
		}
		if info.Mode().IsRegular() {
			total += uint64(max(info.Size(), 0))
		} else {
			minimum = 7
		}
	}
	width := len(strconv.FormatUint(total, 10))
	return max(width, minimum)
}
