package bash

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
)

const uniqUsage = `Usage: uniq [OPTION]... [INPUT]
Filter adjacent matching lines from INPUT (or standard input), writing to
standard output. Only adjacent lines are compared, so sort the input first.

Supported options:
  -c, --count          prefix lines by the number of occurrences
  -d, --repeated       only print duplicate lines, one for each group
  -u, --unique         only print unique lines
  -i, --ignore-case    ignore differences in case when comparing
      --help           display this help and exit

Writing to an output file (a second operand) is not supported; use a pipeline.
Not supported: -D -f -s -w -z.
`

var uniqOptions = []optSpec{
	{short: 'c', long: "count"},
	{short: 'd', long: "repeated"},
	{short: 'u', long: "unique"},
	{short: 'i', long: "ignore-case"},
}

func runUniq(ctx context.Context, c *Call) int {
	if hasHelp(c.Args) {
		return c.help(uniqUsage)
	}
	opts, operands, err := getopt("uniq", uniqOptions, c.Args)
	if err != nil {
		return c.fail(err, 1)
	}
	var count, fold bool
	printUnique, printRepeated := true, true
	for _, opt := range opts {
		switch opt.id {
		case "count":
			count = true
		case "repeated":
			printUnique = false
		case "unique":
			printRepeated = false
		case "ignore-case":
			fold = true
		}
	}
	if len(operands) > 1 {
		if len(operands) > 2 {
			c.errf("uniq", "extra operand %s", quoteAlways(operands[2]))
			c.tryHelp("uniq")
			return 1
		}
		c.errf("uniq", "writing to an output file (%s) is not supported; write the result to standard output", quoteName(operands[1]))
		return 1
	}
	name := "-"
	if len(operands) == 1 {
		name = operands[0]
	}
	in, err := c.open(name)
	if err != nil {
		c.errf("uniq", "%s: %s", quoteName(name), errText(err))
		return 1
	}
	defer func() { _ = in.Close() }()

	out := newOutput(c.Stdout)
	defer func() { _ = out.Flush() }()
	var prev []byte
	var prevKey []byte
	var n uint64
	flush := func() {
		if n == 0 {
			return
		}
		if (n == 1 && printUnique) || (n > 1 && printRepeated) {
			if count {
				_, _ = fmt.Fprintf(out, "%7s ", strconv.FormatUint(n, 10))
			}
			_, _ = out.Write(prev)
			_ = out.WriteByte('\n')
		}
	}
	rerr := forEachLine(ctx, in, func(line []byte) error {
		text, _ := stripNL(line)
		key := text
		if fold {
			key = foldASCII(text)
		}
		if n > 0 && bytes.Equal(key, prevKey) {
			n++
			return nil
		}
		flush()
		if out.failed() {
			return errStop
		}
		prev, prevKey, n = text, key, 1
		return nil
	})
	if rerr != nil {
		if out.failed() || ctx.Err() != nil {
			return 1
		}
		c.errf("uniq", "error reading %s: %s", quoteAlways(name), errText(rerr))
		return 1
	}
	flush()
	if err := out.Flush(); err != nil {
		return 1
	}
	return 0
}

// foldASCII returns a copy of b with the ASCII lower case letters in upper
// case; other bytes are kept, as GNU uniq -i compares in a UTF-8 locale.
func foldASCII(b []byte) []byte {
	out := make([]byte, len(b))
	for i, x := range b {
		out[i] = upperASCII(x)
	}
	return out
}
