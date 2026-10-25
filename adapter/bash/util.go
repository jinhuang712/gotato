package bash

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
)

// MaxBufferBytes is the most bytes a command that must hold its input (sort,
// and tail with a large count) keeps in memory. Past it the command fails with
// an error that says so, instead of using more.
const MaxBufferBytes = 64 << 20

var errTooLarge = fmt.Errorf("input too large to hold in memory (the most is %d bytes)", MaxBufferBytes)

// errStop is returned by the function given to forEachLine to end the loop
// early without an error.
var errStop = errors.New("stop")

// forEachLine calls fn for each line of in, with its newline when it has one.
// The slice is the caller's to keep. A line over MaxLineBytes is an error.
func forEachLine(ctx context.Context, in io.Reader, fn func(line []byte) error) error {
	reader := bufio.NewReaderSize(in, 32<<10)
	for n := 1; ; n++ {
		if n&0x3ff == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		line, err := readLine(reader)
		if len(line) > 0 {
			if ferr := fn(line); ferr != nil {
				if errors.Is(ferr, errStop) {
					return nil
				}
				return ferr
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

// parseUint reads a number of digits; a number too large for 64 bits becomes
// math.MaxInt64, which is more than any input holds.
func parseUint(digits string) uint64 {
	n, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return math.MaxInt64
	}
	return n
}

// tryHelp writes the line GNU tools end a usage error with.
func (c *Call) tryHelp(name string) {
	_, _ = fmt.Fprintf(c.Stderr, "Try '%s --help' for more information.\n", name)
}

// stdinOrFiles returns the operands, or "-" when there are none.
func stdinOrFiles(files []string) []string {
	if len(files) == 0 {
		return []string{"-"}
	}
	return files
}

// stripNL removes one trailing newline.
func stripNL(line []byte) ([]byte, bool) {
	if n := len(line); n > 0 && line[n-1] == '\n' {
		return line[:n-1], true
	}
	return line, false
}
