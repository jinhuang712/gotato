package bash

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
)

const catUsage = `Usage: cat [OPTION]... [FILE]...
Concatenate FILE(s) to standard output. With no FILE, or when FILE is -, read standard input.

Supported options:
  -n, --number   number all output lines
      --help     display this help and exit
`

var catOptions = []optSpec{{short: 'n', long: "number"}}

// runCat is cat: the files in order to stdout, with -n numbering the lines.
func runCat(ctx context.Context, c *Call) int {
	if hasHelp(c.Args) {
		return c.help(catUsage)
	}
	opts, files, err := getopt("cat", catOptions, c.Args)
	if err != nil {
		_, _ = fmt.Fprintln(c.Stderr, err)
		return 1
	}
	number := len(opts) > 0
	if len(files) == 0 {
		files = []string{"-"}
	}
	out := newOutput(c.Stdout)
	defer func() { _ = out.Flush() }()
	status := 0
	lineNo := 0
	atLineStart := true
	for _, name := range files {
		in, err := c.open(name)
		if err != nil {
			c.errf("cat", "%s: %s", quoteName(name), errText(err))
			status = 1
			continue
		}
		var copyErr error
		if number {
			copyErr = numberLines(ctx, in, out, &lineNo, &atLineStart)
		} else {
			_, copyErr = io.Copy(out, in)
		}
		_ = in.Close()
		if copyErr != nil {
			if out.failed() || ctx.Err() != nil {
				return 1 // the reader of the pipe is gone, or the run was cancelled
			}
			c.errf("cat", "%s: %s", quoteName(name), errText(copyErr))
			status = 1
		}
	}
	if err := out.Flush(); err != nil {
		return 1
	}
	return status
}

// numberLines copies in to out with a number before each line, as cat -n does:
// the number is right-aligned in six columns, then a tab. A line is not held
// in memory whole, so a very long line costs nothing extra, and the numbering
// goes on from one file to the next.
func numberLines(ctx context.Context, in io.Reader, out *output, lineNo *int, atLineStart *bool) error {
	reader := bufio.NewReaderSize(in, 32<<10)
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(chunk) > 0 {
			if *atLineStart {
				*lineNo++
				if _, werr := fmt.Fprintf(out, "%6s\t", strconv.Itoa(*lineNo)); werr != nil {
					return werr
				}
			}
			if _, werr := out.Write(chunk); werr != nil {
				return werr
			}
			*atLineStart = chunk[len(chunk)-1] == '\n'
		}
		switch {
		case err == nil, errors.Is(err, bufio.ErrBufferFull):
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
		case errors.Is(err, io.EOF):
			return nil
		default:
			return err
		}
	}
}
