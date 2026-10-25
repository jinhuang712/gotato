package bash

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"syscall"
)

const headUsage = `Usage: head [OPTION]... [FILE]...
Print the first 10 lines of each FILE to standard output. With more than one FILE,
precede each with a header giving the file name. With no FILE, or when FILE is -,
read standard input.

Supported options:
  -c, --bytes=NUM    print the first NUM bytes of each file
  -n, --lines=NUM    print the first NUM lines instead of the first 10;
                     with -NUM, print all but the last NUM lines of each file
  -q, --quiet        never print headers giving file names
  -v, --verbose      always print headers giving file names
      --help         display this help and exit

NUM is a plain number. Suffixes such as K and M are not supported.
`

var headOptions = []optSpec{
	{short: 'c', long: "bytes", arg: true},
	{short: 'n', long: "lines", arg: true},
	{short: 'q', long: "quiet"},
	{short: 'v', long: "verbose"},
}

// headCount is the value of -n or -c: a count, and whether it was written
// with a minus sign (all but the last).
type headCount struct {
	n     uint64
	but   bool
	bytes bool
}

func runHead(ctx context.Context, c *Call) int {
	if hasHelp(c.Args) {
		return c.help(headUsage)
	}
	args := c.Args
	// head -5 is the obsolete form of head -n 5, and only as the first argument.
	if len(args) > 0 && len(args[0]) > 1 && args[0][0] == '-' && isDigits(args[0][1:]) {
		args = append([]string{"-n", args[0][1:]}, args[1:]...)
	}
	opts, files, err := getopt("head", headOptions, args)
	if err != nil {
		_, _ = fmt.Fprintln(c.Stderr, err)
		return 1
	}
	count := headCount{n: 10}
	quiet, verbose := false, false
	for _, opt := range opts {
		switch opt.id {
		case "lines", "bytes":
			parsed, perr := parseHeadCount(opt.id, opt.value)
			if perr != nil {
				c.errf("head", "%s", perr)
				return 1
			}
			count = parsed
		case "quiet":
			quiet, verbose = true, false
		case "verbose":
			verbose, quiet = true, false
		}
	}
	if len(files) == 0 {
		files = []string{"-"}
	}
	headers := (len(files) > 1 && !quiet) || verbose

	out := newOutput(c.Stdout)
	defer func() { _ = out.Flush() }()
	status := 0
	first := true
	for _, name := range files {
		in, err := c.open(name)
		if errors.Is(err, syscall.EISDIR) {
			c.errf("head", "error reading '%s': %s", name, errText(err))
			status = 1
			continue
		}
		if err != nil {
			c.errf("head", "cannot open '%s' for reading: %s", name, errText(err))
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
		var copyErr error
		switch {
		case count.bytes && count.but:
			copyErr = errors.New("a negative byte count is not supported; use head -n with a number of lines, or a positive -c")
		case count.bytes:
			copyErr = headBytes(ctx, in, out, count.n)
		case count.but:
			copyErr = headAllButLast(ctx, in, out, count.n)
		default:
			copyErr = headLines(ctx, in, out, count.n)
		}
		_ = in.Close()
		if copyErr != nil {
			if out.failed() || ctx.Err() != nil {
				return 1
			}
			c.errf("head", "error reading '%s': %s", name, errText(copyErr))
			status = 1
		}
	}
	if err := out.Flush(); err != nil {
		return 1
	}
	return status
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// parseHeadCount reads the value of -n or -c: digits, optionally after a minus
// sign. GNU also takes suffixes (K, M, b...), which are refused here.
func parseHeadCount(kind, value string) (headCount, error) {
	count := headCount{bytes: kind == "bytes"}
	digits := value
	if len(digits) > 0 && digits[0] == '-' {
		count.but, digits = true, digits[1:]
	} else if len(digits) > 0 && digits[0] == '+' {
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
	n, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		n = math.MaxInt64 // more than any file holds
	}
	count.n = n
	return count, nil
}

// headLines copies the first n lines of in to out, and stops reading as soon as
// it has them, so the command upstream in a pipeline is stopped early.
func headLines(ctx context.Context, in io.Reader, out io.Writer, n uint64) error {
	if n == 0 {
		return nil
	}
	buf := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		got, err := in.Read(buf)
		chunk := buf[:got]
		for len(chunk) > 0 {
			i := bytes.IndexByte(chunk, '\n')
			if i < 0 {
				if _, werr := out.Write(chunk); werr != nil {
					return werr
				}
				break
			}
			if _, werr := out.Write(chunk[:i+1]); werr != nil {
				return werr
			}
			chunk = chunk[i+1:]
			if n--; n == 0 {
				return nil
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

// headBytes copies the first n bytes of in to out.
func headBytes(ctx context.Context, in io.Reader, out io.Writer, n uint64) error {
	buf := make([]byte, 32<<10)
	for n > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		want := buf
		if uint64(len(want)) > n {
			want = want[:n]
		}
		got, err := in.Read(want)
		if got > 0 {
			if _, werr := out.Write(want[:got]); werr != nil {
				return werr
			}
			n -= uint64(got)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
	return nil
}

// headAllButLast copies all of in but its last n lines to out. It holds n lines
// at a time, and fails if one is longer than MaxLineBytes.
func headAllButLast(ctx context.Context, in io.Reader, out io.Writer, n uint64) error {
	reader := bufio.NewReaderSize(in, 32<<10)
	var held [][]byte
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := readLine(reader)
		if len(line) > 0 {
			held = append(held, line)
			if uint64(len(held)) > n {
				if _, werr := out.Write(held[0]); werr != nil {
					return werr
				}
				held[0] = nil
				held = held[1:]
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

var errLineTooLong = fmt.Errorf("line too long (the most is %d bytes)", MaxLineBytes)

// readLine reads one line with its newline, or the last one without. It returns
// io.EOF with the last line, if any, and errLineTooLong past MaxLineBytes.
func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > MaxLineBytes {
			return nil, errLineTooLong
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}
