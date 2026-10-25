package bash

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"syscall"
)

const tailUsage = `Usage: tail [OPTION]... [FILE]...
Print the last 10 lines of each FILE to standard output. With more than one FILE,
precede each with a header giving the file name. With no FILE, or when FILE is -,
read standard input.

Supported options:
  -c, --bytes=[+]NUM   output the last NUM bytes; or use -c +NUM to output
                       starting with byte NUM of each file
  -n, --lines=[+]NUM   output the last NUM lines, instead of the last 10;
                       or use -n +NUM to output starting with line NUM
  -q, --quiet          never output headers giving file names
  -v, --verbose        always output headers giving file names
      --help           display this help and exit

NUM is a plain number. Suffixes such as K and M are not supported.
Following a file (-f, -F, --follow) is not supported: a result does not grow.
`

var tailOptions = []optSpec{
	{short: 'c', long: "bytes", arg: true},
	{short: 'n', long: "lines", arg: true},
	{short: 'q', long: "quiet"},
	{short: 'v', long: "verbose"},
}

// tailCount is the value of -n or -c: a count, and whether it is counted from
// the start (written +NUM) instead of from the end.
type tailCount struct {
	n         uint64
	fromStart bool
	bytes     bool
}

func runTail(ctx context.Context, c *Call) int {
	if hasHelp(c.Args) {
		return c.help(tailUsage)
	}
	args := c.Args
	// tail -5 is the obsolete form of tail -n 5, and only as the first argument.
	if len(args) > 0 && len(args[0]) > 1 && args[0][0] == '-' && isDigits(args[0][1:]) {
		args = append([]string{"-n", args[0][1:]}, args[1:]...)
	}
	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "-f" || a == "-F" || a == "--follow" || len(a) > 9 && a[:9] == "--follow=" || a == "--retry" {
			c.errf("tail", "following a file (%s) is not supported: a result does not grow. Read it with tail -n or tail -c", a)
			return 1
		}
	}
	opts, files, err := getopt("tail", tailOptions, args)
	if err != nil {
		_, _ = fmt.Fprintln(c.Stderr, err)
		return 1
	}
	count := tailCount{n: 10}
	quiet, verbose := false, false
	for _, opt := range opts {
		switch opt.id {
		case "lines", "bytes":
			parsed, perr := parseTailCount(opt.id, opt.value)
			if perr != nil {
				c.errf("tail", "%s", perr)
				return 1
			}
			count = parsed
		case "quiet":
			quiet, verbose = true, false
		case "verbose":
			verbose, quiet = true, false
		}
	}
	files = stdinOrFiles(files)
	headers := (len(files) > 1 && !quiet) || verbose

	out := newOutput(c.Stdout)
	defer func() { _ = out.Flush() }()
	status := 0
	first := true
	for _, name := range files {
		in, err := c.open(name)
		isDir := errors.Is(err, syscall.EISDIR)
		if err != nil && !isDir {
			c.errf("tail", "cannot open %s for reading: %s", quoteAlways(name), errText(err))
			status = 1
			continue
		}
		if headers {
			title := name
			if name == "-" {
				title = "standard input"
			}
			sep := ""
			if !first {
				sep = "\n"
			}
			_, _ = fmt.Fprintf(out, "%s==> %s <==\n", sep, title)
		}
		first = false
		if isDir {
			c.errf("tail", "error reading %s: %s", quoteAlways(name), errText(err))
			status = 1
			continue
		}
		var copyErr error
		switch {
		case count.bytes && count.fromStart:
			copyErr = tailBytesFrom(ctx, in, out, count.n)
		case count.bytes:
			copyErr = tailLastBytes(ctx, in, out, count.n)
		case count.fromStart:
			copyErr = tailLinesFrom(ctx, in, out, count.n)
		default:
			copyErr = tailLastLines(ctx, in, out, count.n)
		}
		_ = in.Close()
		if copyErr != nil {
			if out.failed() || ctx.Err() != nil {
				return 1
			}
			c.errf("tail", "error reading %s: %s", quoteAlways(name), errText(copyErr))
			status = 1
		}
	}
	if err := out.Flush(); err != nil {
		return 1
	}
	return status
}

// parseTailCount reads the value of -n or -c: digits, with a plus sign to
// count from the start. GNU also takes suffixes (K, M, b...), which are refused.
func parseTailCount(kind, value string) (tailCount, error) {
	count := tailCount{bytes: kind == "bytes"}
	digits := value
	if len(digits) > 0 && digits[0] == '+' {
		count.fromStart, digits = true, digits[1:]
	} else if len(digits) > 0 && digits[0] == '-' {
		digits = digits[1:]
	}
	what := "number of lines"
	if count.bytes {
		what = "number of bytes"
	}
	if !isDigits(digits) {
		if len(digits) > 1 && isDigits(digits[:len(digits)-1]) || len(digits) > 2 && isDigits(digits[:len(digits)-2]) {
			return count, fmt.Errorf("invalid %s: '%s' (number suffixes such as K, M and b are not supported; write the number out)", what, value)
		}
		return count, fmt.Errorf("invalid %s: '%s'", what, value)
	}
	count.n = parseUint(digits)
	return count, nil
}

// tailLinesFrom copies in from line n on (n = 0 and n = 1 both mean all).
func tailLinesFrom(ctx context.Context, in io.Reader, out io.Writer, n uint64) error {
	skip := uint64(0)
	if n > 1 {
		skip = n - 1
	}
	buf := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		got, err := in.Read(buf)
		chunk := buf[:got]
		for skip > 0 && len(chunk) > 0 {
			i := bytes.IndexByte(chunk, '\n')
			if i < 0 {
				chunk = nil
				break
			}
			chunk = chunk[i+1:]
			skip--
		}
		if len(chunk) > 0 {
			if _, werr := out.Write(chunk); werr != nil {
				return werr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// tailBytesFrom copies in from byte n on (n = 0 and n = 1 both mean all).
func tailBytesFrom(ctx context.Context, in io.Reader, out io.Writer, n uint64) error {
	skip := uint64(0)
	if n > 1 {
		skip = n - 1
	}
	buf := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		got, err := in.Read(buf)
		chunk := buf[:got]
		if skip > 0 {
			d := min(skip, uint64(len(chunk)))
			chunk = chunk[d:]
			skip -= d
		}
		if len(chunk) > 0 {
			if _, werr := out.Write(chunk); werr != nil {
				return werr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// tailLastLines writes the last n lines of in. It holds at most n lines (and
// fails past MaxBufferBytes), so the lines before them cost nothing.
func tailLastLines(ctx context.Context, in io.Reader, out io.Writer, n uint64) error {
	if n == 0 {
		return nil
	}
	var held [][]byte
	start, size := 0, 0
	err := forEachLine(ctx, in, func(line []byte) error {
		held = append(held, line)
		size += len(line)
		for uint64(len(held)-start) > n {
			size -= len(held[start])
			held[start] = nil
			start++
		}
		if start > 1024 && start > len(held)/2 {
			held = append([][]byte(nil), held[start:]...)
			start = 0
		}
		if size > MaxBufferBytes {
			return errTooLarge
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, line := range held[start:] {
		if _, werr := out.Write(line); werr != nil {
			return werr
		}
	}
	return nil
}

// tailLastBytes writes the last n bytes of in, kept in a ring of n bytes.
func tailLastBytes(ctx context.Context, in io.Reader, out io.Writer, n uint64) error {
	if n == 0 {
		return nil
	}
	if n > MaxBufferBytes {
		return errTooLarge
	}
	ring := make([]byte, n)
	var total uint64
	buf := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		got, err := in.Read(buf)
		for _, b := range buf[:got] {
			ring[total%n] = b
			total++
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return err
			}
			break
		}
	}
	if total <= n {
		_, werr := out.Write(ring[:total])
		return werr
	}
	pos := total % n
	if _, werr := out.Write(ring[pos:]); werr != nil {
		return werr
	}
	_, werr := out.Write(ring[:pos])
	return werr
}
