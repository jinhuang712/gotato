package bash

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
)

const cutUsage = `Usage: cut OPTION... [FILE]...
Print selected parts of lines from each FILE to standard output. With no FILE,
or when FILE is -, read standard input.

Supported options:
  -b, --bytes=LIST        select only these bytes
  -c, --characters=LIST   select only these characters (GNU cut counts bytes here, as for -b)
  -d, --delimiter=DELIM   use DELIM instead of TAB for field delimiter (one byte)
  -f, --fields=LIST       select only these fields; also print any line that
                          contains no delimiter character, unless -s is given
  -s, --only-delimited    do not print lines not containing delimiters
      --complement        select the complement of the selected bytes, characters or fields
      --help              display this help and exit

Use one, and only one of -b, -c or -f. LIST is one range, or many ranges
separated by commas. Each range is one of N, N-M, N- or -M, counting from 1.
`

var cutOptions = []optSpec{
	{short: 'b', long: "bytes", arg: true},
	{short: 'c', long: "characters", arg: true},
	{short: 'd', long: "delimiter", arg: true},
	{short: 'f', long: "fields", arg: true},
	{short: 's', long: "only-delimited"},
	{long: "complement"},
}

// cutRange is a range of positions, counting from 1; end 0 means no end.
type cutRange struct{ lo, hi uint64 }

// parseCutList parses N, N-M, N- and -M ranges separated by commas (or
// blanks), as GNU cut does, and returns them sorted and merged.
func parseCutList(list string, fields bool) ([]cutRange, error) {
	what := "byte or character"
	if fields {
		what = "field"
	}
	var ranges []cutRange
	for _, item := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		lo, hi, dash := strings.Cut(item, "-")
		if dash && lo == "" && hi == "" {
			return nil, errors.New("invalid range with no endpoint: -")
		}
		var r cutRange
		bad := func(s string) bool { return s != "" && !isDigits(s) }
		if bad(lo) || bad(hi) {
			return nil, errors.New("invalid " + what + " range")
		}
		if lo != "" {
			r.lo = parseUint(lo)
		} else {
			r.lo = 1
		}
		if dash {
			if hi != "" {
				r.hi = parseUint(hi)
			}
		} else {
			r.hi = r.lo
		}
		if (lo != "" && r.lo == 0) || (lo == "" && hi != "" && r.hi == 0) {
			if fields {
				return nil, errors.New("fields are numbered from 1")
			}
			return nil, errors.New("byte/character positions are numbered from 1")
		}
		if dash && hi != "" && lo != "" && r.hi < r.lo {
			return nil, errors.New("invalid decreasing range")
		}
		ranges = append(ranges, r)
	}
	if len(ranges) == 0 {
		return nil, errors.New("invalid " + what + " range")
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].lo < ranges[j].lo })
	merged := ranges[:1]
	for _, r := range ranges[1:] {
		last := &merged[len(merged)-1]
		if last.hi == 0 || r.lo <= last.hi+1 {
			if last.hi != 0 && (r.hi == 0 || r.hi > last.hi) {
				last.hi = r.hi
			}
			continue
		}
		merged = append(merged, r)
	}
	return merged, nil
}

// selected reports whether position pos (from 1) is in the ranges.
func cutSelected(ranges []cutRange, pos uint64, complement bool) bool {
	in := false
	for _, r := range ranges {
		if pos >= r.lo && (r.hi == 0 || pos <= r.hi) {
			in = true
			break
		}
	}
	return in != complement
}

func runCut(ctx context.Context, c *Call) int {
	if hasHelp(c.Args) {
		return c.help(cutUsage)
	}
	opts, files, err := getopt("cut", cutOptions, c.Args)
	if err != nil {
		return c.fail(err, 1)
	}
	var mode, list string
	delim := byte('\t')
	var onlyDelimited, complement bool
	for _, opt := range opts {
		switch opt.id {
		case "bytes", "characters", "fields":
			if mode != "" && mode != opt.id {
				c.errf("cut", "only one list may be specified")
				c.tryHelp("cut")
				return 1
			}
			mode, list = opt.id, opt.value
		case "delimiter":
			if len(opt.value) != 1 {
				c.errf("cut", "the delimiter must be a single character")
				c.tryHelp("cut")
				return 1
			}
			delim = opt.value[0]
		case "only-delimited":
			onlyDelimited = true
		case "complement":
			complement = true
		}
	}
	if mode == "" {
		c.errf("cut", "you must specify a list of bytes, characters, or fields")
		c.tryHelp("cut")
		return 1
	}
	if mode != "fields" {
		for _, opt := range opts {
			if opt.id == "delimiter" {
				c.errf("cut", "an input delimiter may be specified only when operating on fields")
				c.tryHelp("cut")
				return 1
			}
			if opt.id == "only-delimited" {
				c.errf("cut", "suppressing non-delimited lines makes sense\n\tonly when operating on fields")
				c.tryHelp("cut")
				return 1
			}
		}
	}
	ranges, perr := parseCutList(list, mode == "fields")
	if perr != nil {
		c.errf("cut", "%s", perr)
		c.tryHelp("cut")
		return 1
	}

	out := newOutput(c.Stdout)
	defer func() { _ = out.Flush() }()
	status := 0
	for _, name := range stdinOrFiles(files) {
		in, err := c.open(name)
		if err != nil {
			c.errf("cut", "%s: %s", quoteName(name), errText(err))
			status = 1
			continue
		}
		rerr := forEachLine(ctx, in, func(line []byte) error {
			text, _ := stripNL(line)
			if mode == "fields" {
				cutFields(out, text, ranges, delim, onlyDelimited, complement)
			} else {
				cutBytes(out, text, ranges, complement)
			}
			if out.failed() {
				return errStop
			}
			return nil
		})
		_ = in.Close()
		if rerr != nil {
			if out.failed() || ctx.Err() != nil {
				return 1
			}
			c.errf("cut", "%s: %s", quoteName(name), errText(rerr))
			status = 1
		}
	}
	if err := out.Flush(); err != nil {
		return 1
	}
	return status
}

func cutBytes(out *output, text []byte, ranges []cutRange, complement bool) {
	for i, b := range text {
		if cutSelected(ranges, uint64(i)+1, complement) {
			_ = out.WriteByte(b)
		}
	}
	_ = out.WriteByte('\n')
}

func cutFields(out *output, text []byte, ranges []cutRange, delim byte, onlyDelimited, complement bool) {
	if bytes.IndexByte(text, delim) < 0 {
		if !onlyDelimited {
			_, _ = out.Write(text)
			_ = out.WriteByte('\n')
		}
		return
	}
	first := true
	for i, field := range bytes.Split(text, []byte{delim}) {
		if !cutSelected(ranges, uint64(i)+1, complement) {
			continue
		}
		if !first {
			_ = out.WriteByte(delim)
		}
		first = false
		_, _ = out.Write(field)
	}
	_ = out.WriteByte('\n')
}
