package bash

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"syscall"
)

const sortUsage = `Usage: sort [OPTION]... [FILE]...
Write sorted concatenation of all FILE(s) to standard output. With no FILE, or
when FILE is -, read standard input.

Supported options:
  -b, --ignore-leading-blanks  ignore leading blanks
  -f, --ignore-case            fold lower case to upper case characters
  -n, --numeric-sort           compare according to string numerical value
  -r, --reverse                reverse the result of comparisons
  -s, --stable                 stabilize sort by disabling last-resort comparison
  -u, --unique                 output only the first of an equal run
  -k, --key=KEYDEF             sort via a key; KEYDEF gives location and type
  -t, --field-separator=SEP    use SEP instead of non-blank to blank transition
      --help                   display this help and exit

KEYDEF is F[.C][OPTS][,F[.C][OPTS]] for start and stop position, where F is a
field number and C a character position in the field, both origin 1; OPTS is
one or more of b, f, n, r, which override the global ordering options for that
key. The order is that of the C.UTF-8 locale: by code point (so upper case
letters come before lower case), and not by the collation of a language.
The input is held in memory, up to a bound.
`

var sortOptions = []optSpec{
	{short: 'b', long: "ignore-leading-blanks"},
	{short: 'f', long: "ignore-case"},
	{short: 'n', long: "numeric-sort"},
	{short: 'r', long: "reverse"},
	{short: 's', long: "stable"},
	{short: 'u', long: "unique"},
	{short: 'k', long: "key", arg: true},
	{short: 't', long: "field-separator", arg: true},
}

// sortKey is one -k key, or the whole line.
type sortKey struct {
	sword, schar int // 0-based start field, and offset in it
	eword, echar int // end field (1-based as written, 0 = none), and char (0 = end of field)
	hasEnd       bool
	skipSBlanks  bool
	skipEBlanks  bool
	numeric      bool
	fold         bool
	reverse      bool
}

func (k *sortKey) noFlags() bool {
	return !k.skipSBlanks && !k.skipEBlanks && !k.numeric && !k.fold && !k.reverse
}

type sortOrder struct {
	keys    []*sortKey
	tab     int // -1 when fields are split at blanks
	unique  bool
	stable  bool
	globalR bool
	keyed   bool // keys are used (explicit -k, or a global option that orders by a key)
}

func sortBlank(b byte) bool { return b == ' ' || b == '\t' }

// parseKeyDef parses F[.C][OPTS][,F[.C][OPTS]].
func parseKeyDef(s string) (*sortKey, error) {
	k := &sortKey{}
	i := 0
	number := func() (int, bool) {
		j := i
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j == i {
			return 0, false
		}
		n := int(min(parseUint(s[i:j]), 1<<30))
		i = j
		return n, true
	}
	flags := func(start bool) error {
		for i < len(s) && s[i] != ',' {
			switch s[i] {
			case 'b':
				if start {
					k.skipSBlanks = true
				} else {
					k.skipEBlanks = true
				}
			case 'n':
				k.numeric = true
			case 'f':
				k.fold = true
			case 'r':
				k.reverse = true
			default:
				return fmt.Errorf("the key option '%c' is not supported (the supported ones are b, f, n and r)", s[i])
			}
			i++
		}
		return nil
	}
	f, ok := number()
	if !ok {
		return nil, fmt.Errorf("invalid number at field start: invalid count at start of '%s'", s)
	}
	if f == 0 {
		return nil, errors.New("field number is zero: invalid field specification '" + s + "'")
	}
	k.sword = f - 1
	if i < len(s) && s[i] == '.' {
		i++
		c, ok := number()
		if !ok {
			return nil, fmt.Errorf("invalid number after '.': invalid count at start of '%s'", s[i:])
		}
		if c == 0 {
			return nil, errors.New("character offset is zero: invalid field specification '" + s + "'")
		}
		k.schar = c - 1
	}
	if err := flags(true); err != nil {
		return nil, err
	}
	if i < len(s) && s[i] == ',' {
		i++
		k.hasEnd = true
		e, ok := number()
		if !ok {
			return nil, fmt.Errorf("invalid number after ',': invalid count at start of '%s'", s[i:])
		}
		if e == 0 {
			return nil, errors.New("field number is zero: invalid field specification '" + s + "'")
		}
		k.eword = e
		if i < len(s) && s[i] == '.' {
			i++
			c, ok := number()
			if !ok {
				return nil, fmt.Errorf("invalid number after '.': invalid count at start of '%s'", s[i:])
			}
			k.echar = c
		}
		if err := flags(false); err != nil {
			return nil, err
		}
	}
	if i < len(s) {
		return nil, fmt.Errorf("stray character in field spec: invalid field specification '%s'", s)
	}
	return k, nil
}

// begin and limit find the key in a line, as GNU sort's begfield and limfield.
func (o *sortOrder) begin(line []byte, k *sortKey) int {
	ptr, lim := 0, len(line)
	sword := k.sword
	if o.tab >= 0 {
		for ptr < lim && sword > 0 {
			sword--
			for ptr < lim && int(line[ptr]) != o.tab {
				ptr++
			}
			if ptr < lim {
				ptr++
			}
		}
	} else {
		for ptr < lim && sword > 0 {
			sword--
			for ptr < lim && sortBlank(line[ptr]) {
				ptr++
			}
			for ptr < lim && !sortBlank(line[ptr]) {
				ptr++
			}
		}
	}
	if k.skipSBlanks {
		for ptr < lim && sortBlank(line[ptr]) {
			ptr++
		}
	}
	return min(lim, ptr+k.schar)
}

func (o *sortOrder) limit(line []byte, k *sortKey) int {
	if !k.hasEnd {
		return len(line)
	}
	ptr, lim := 0, len(line)
	eword, echar := k.eword-1, k.echar // the field number is written from 1
	if echar == 0 {
		eword++ // skip all of the end field
	}
	if o.tab >= 0 {
		for ptr < lim && eword > 0 {
			eword--
			for ptr < lim && int(line[ptr]) != o.tab {
				ptr++
			}
			if ptr < lim && (eword > 0 || echar > 0) {
				ptr++
			}
		}
	} else {
		for ptr < lim && eword > 0 {
			eword--
			for ptr < lim && sortBlank(line[ptr]) {
				ptr++
			}
			for ptr < lim && !sortBlank(line[ptr]) {
				ptr++
			}
		}
	}
	if echar != 0 {
		if k.skipEBlanks {
			for ptr < lim && sortBlank(line[ptr]) {
				ptr++
			}
		}
		ptr = min(lim, ptr+echar)
	}
	return ptr
}

// compareKey compares the parts of two lines that a key selects.
func (o *sortOrder) compareKey(a, b []byte, k *sortKey) int {
	as, ae := o.begin(a, k), o.limit(a, k)
	bs, be := o.begin(b, k), o.limit(b, k)
	ae, be = max(as, ae), max(bs, be) // an end before the start is an empty key
	ta, tb := a[as:ae], b[bs:be]
	var diff int
	switch {
	case k.numeric:
		diff = compareNumbers(ta, tb)
	case k.fold:
		diff = compareFold(ta, tb)
	default:
		diff = bytes.Compare(ta, tb)
	}
	if k.reverse {
		return -diff
	}
	return diff
}

func compareFold(a, b []byte) int {
	n := min(len(a), len(b))
	for i := range n {
		x, y := upperASCII(a[i]), upperASCII(b[i])
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

func upperASCII(b byte) byte {
	if b >= 'a' && b <= 'z' {
		return b - 'a' + 'A'
	}
	return b
}

// number is a parsed numeric key: GNU sort -n reads blanks, an optional minus
// sign, digits, and a fraction after a point; anything else ends the number,
// and a text with no digits is zero.
type number struct {
	neg        bool
	whole, fra []byte
}

func parseNumber(s []byte) number {
	i := 0
	for i < len(s) && sortBlank(s[i]) {
		i++
	}
	var n number
	if i < len(s) && s[i] == '-' {
		n.neg = true
		i++
	}
	j := i
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	n.whole = bytes.TrimLeft(s[i:j], "0")
	if j < len(s) && s[j] == '.' {
		k := j + 1
		for k < len(s) && s[k] >= '0' && s[k] <= '9' {
			k++
		}
		n.fra = bytes.TrimRight(s[j+1:k], "0")
	}
	if len(n.whole) == 0 && len(n.fra) == 0 {
		n.neg = false // zero has no sign
	}
	return n
}

func compareNumbers(a, b []byte) int {
	x, y := parseNumber(a), parseNumber(b)
	switch {
	case x.neg && !y.neg:
		return -1
	case !x.neg && y.neg:
		return 1
	}
	mag := 0
	switch {
	case len(x.whole) != len(y.whole):
		mag = len(x.whole) - len(y.whole)
	default:
		if mag = bytes.Compare(x.whole, y.whole); mag == 0 {
			mag = bytes.Compare(x.fra, y.fra)
		}
	}
	mag = min(max(mag, -1), 1)
	if x.neg {
		return -mag
	}
	return mag
}

// compare orders two lines: by each key, then (unless the sort is stable or
// unique) by the whole line.
func (o *sortOrder) compare(a, b []byte) int {
	if o.keyed {
		for _, k := range o.keys {
			if d := o.compareKey(a, b, k); d != 0 {
				return d
			}
		}
		if o.unique || o.stable {
			return 0
		}
	}
	d := bytes.Compare(a, b)
	if o.globalR {
		return -d
	}
	return d
}

func runSort(ctx context.Context, c *Call) int {
	if hasHelp(c.Args) {
		return c.help(sortUsage)
	}
	opts, files, err := getopt("sort", sortOptions, c.Args)
	if err != nil {
		return c.fail(err, 2)
	}
	o := sortOrder{tab: -1}
	global := &sortKey{}
	var keys []*sortKey
	for _, opt := range opts {
		switch opt.id {
		case "ignore-leading-blanks":
			global.skipSBlanks, global.skipEBlanks = true, true
		case "ignore-case":
			global.fold = true
		case "numeric-sort":
			global.numeric = true
		case "reverse":
			global.reverse = true
		case "stable":
			o.stable = true
		case "unique":
			o.unique = true
		case "key":
			k, kerr := parseKeyDef(opt.value)
			if kerr != nil {
				c.errf("sort", "%s", kerr)
				return 2
			}
			keys = append(keys, k)
		case "field-separator":
			switch {
			case opt.value == "":
				c.errf("sort", "empty tab")
				return 2
			case len(opt.value) > 1:
				c.errf("sort", "multi-character tab %s", quoteAlways(opt.value))
				return 2
			}
			if o.tab >= 0 && o.tab != int(opt.value[0]) {
				c.errf("sort", "incompatible tabs")
				return 2
			}
			o.tab = int(opt.value[0])
		}
	}
	o.globalR = global.reverse
	// A key with no ordering option of its own takes the global ones.
	for _, k := range keys {
		if k.noFlags() {
			k.skipSBlanks, k.skipEBlanks = global.skipSBlanks, global.skipEBlanks
			k.numeric, k.fold, k.reverse = global.numeric, global.fold, global.reverse
		}
	}
	if len(keys) == 0 && (global.numeric || global.fold || global.skipSBlanks) {
		whole := *global
		keys = []*sortKey{&whole}
	}
	o.keys, o.keyed = keys, len(keys) > 0

	var lines [][]byte
	size := 0
	for _, name := range stdinOrFiles(files) {
		in, err := c.open(name)
		if err != nil {
			what := "cannot read"
			if errors.Is(err, syscall.EISDIR) {
				what = "read failed"
			}
			c.errf("sort", "%s: %s: %s", what, quoteName(name), errText(err))
			return 2
		}
		rerr := forEachLine(ctx, in, func(line []byte) error {
			if size += len(line) + 24; size > MaxBufferBytes {
				return errTooLarge
			}
			lines = append(lines, line)
			return nil
		})
		_ = in.Close()
		if rerr != nil {
			if ctx.Err() != nil {
				return 2
			}
			c.errf("sort", "read failed: %s: %s", quoteName(name), errText(rerr))
			return 2
		}
	}
	// Compare lines without their newline, as GNU does.
	for i, l := range lines {
		lines[i], _ = stripNL(l)
	}
	slices.SortStableFunc(lines, o.compare)

	out := newOutput(c.Stdout)
	defer func() { _ = out.Flush() }()
	for i, l := range lines {
		if i&0x3ff == 0 && ctx.Err() != nil {
			return 2
		}
		if o.unique && i > 0 && o.compare(lines[i-1], l) == 0 {
			continue
		}
		_, _ = out.Write(l)
		_ = out.WriteByte('\n')
		if out.failed() {
			return 2
		}
	}
	if err := out.Flush(); err != nil {
		return 2
	}
	return 0
}

// fail writes an option error and returns the status.
func (c *Call) fail(err error, status int) int {
	_, _ = fmt.Fprintln(c.Stderr, strings.TrimRight(err.Error(), "\n"))
	return status
}
