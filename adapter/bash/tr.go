package bash

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

const trUsage = `Usage: tr [OPTION]... SET1 [SET2]
Translate, squeeze, and/or delete bytes from standard input, writing to
standard output.

Supported options:
  -c, -C, --complement    use the complement of SET1
  -d, --delete            delete bytes in SET1, do not translate
  -s, --squeeze-repeats   replace each sequence of a repeated byte that is
                          listed in the last SET, with a single occurrence
      --help              display this help and exit

SETs are specified as strings of bytes. Interpreted sequences are:
  \NNN            byte with octal value NNN (1 to 3 octal digits)
  \\              backslash
  \a \b \f \n \r \t \v   the usual control characters
  A-Z             all bytes from A to Z, in ascending order
  [:alnum:] [:alpha:] [:blank:] [:cntrl:] [:digit:] [:graph:] [:lower:]
  [:print:] [:punct:] [:space:] [:upper:] [:xdigit:]
                  the ASCII bytes of the class (tr works on bytes, so a
                  multi-byte character is not a unit)
  [=C=]           all bytes equivalent to C, which is C itself
Not supported: -t, [C*N] and [C*].
`

var trOptions = []optSpec{
	{short: 'c', long: "complement"},
	{short: 'C'},
	{short: 'd', long: "delete"},
	{short: 's', long: "squeeze-repeats"},
}

var trClasses = map[string]func(b byte) bool{
	"alnum":  func(b byte) bool { return isAlpha(b) || b >= '0' && b <= '9' },
	"alpha":  isAlpha,
	"blank":  func(b byte) bool { return b == ' ' || b == '\t' },
	"cntrl":  func(b byte) bool { return b < 0x20 || b == 0x7f },
	"digit":  func(b byte) bool { return b >= '0' && b <= '9' },
	"graph":  func(b byte) bool { return b > 0x20 && b < 0x7f },
	"lower":  func(b byte) bool { return b >= 'a' && b <= 'z' },
	"print":  func(b byte) bool { return b >= 0x20 && b < 0x7f },
	"punct":  func(b byte) bool { return b > 0x20 && b < 0x7f && !isAlpha(b) && (b < '0' || b > '9') },
	"space":  func(b byte) bool { return b == ' ' || b >= 0x09 && b <= 0x0d },
	"upper":  func(b byte) bool { return b >= 'A' && b <= 'Z' },
	"xdigit": func(b byte) bool { return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F' },
}

func isAlpha(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }

// trSet is a parsed SET: the bytes it lists in order, and whether it names a
// class other than upper and lower.
type trSet struct {
	list       []byte
	otherClass bool
}

// parseTrSet expands a SET string as GNU tr does.
func parseTrSet(c *Call, s string) (trSet, error) {
	var set trSet
	i := 0
	readChar := func() (byte, error) {
		ch := s[i]
		if ch != '\\' {
			i++
			return ch, nil
		}
		if i+1 >= len(s) {
			c.errf("tr", "warning: an unescaped backslash at end of string is not portable")
			i++
			return '\\', nil
		}
		i++
		ch = s[i]
		switch ch {
		case 'a':
			i++
			return 7, nil
		case 'b':
			i++
			return 8, nil
		case 'f':
			i++
			return 12, nil
		case 'n':
			i++
			return 10, nil
		case 'r':
			i++
			return 13, nil
		case 't':
			i++
			return 9, nil
		case 'v':
			i++
			return 11, nil
		}
		if ch >= '0' && ch <= '7' {
			v, n := 0, 0
			for n < 3 && i < len(s) && s[i] >= '0' && s[i] <= '7' {
				v = v*8 + int(s[i]-'0')
				i++
				n++
			}
			return byte(v), nil
		}
		i++
		return ch, nil
	}
	var tokens [][]byte // each token is a list of bytes; a lone char is a token of one byte
	var isSingle []int  // 0: a class, 1: a char, 2: an unescaped -
	for i < len(s) {
		if s[i] == '[' && strings.HasPrefix(s[i:], "[:") {
			if end := strings.Index(s[i+2:], ":]"); end >= 0 {
				name := s[i+2 : i+2+end]
				pred, ok := trClasses[name]
				if !ok {
					return set, fmt.Errorf("invalid character class %s", quoteAlways(name))
				}
				var members []byte
				for b := range 256 {
					if pred(byte(b)) {
						members = append(members, byte(b))
					}
				}
				if name != "upper" && name != "lower" {
					set.otherClass = true
				}
				tokens, isSingle = append(tokens, members), append(isSingle, 0)
				i += 2 + end + 2
				continue
			}
		}
		if s[i] == '[' && strings.HasPrefix(s[i:], "[=") && i+2 < len(s) {
			save := i
			i += 2
			ch, err := readChar()
			if err == nil && strings.HasPrefix(s[i:], "=]") {
				i += 2
				tokens, isSingle = append(tokens, []byte{ch}), append(isSingle, 0)
				continue
			}
			i = save
		}
		if s[i] == '[' && i+2 < len(s) && s[i+2] == '*' {
			if end := strings.IndexByte(s[i+3:], ']'); end >= 0 && (end == 0 || isDigits(s[i+3:i+3+end])) {
				return set, errors.New("[C*N] and [C*] repeats are not supported")
			}
		}
		kind := 1
		if s[i] == '-' {
			kind = 2
		}
		ch, err := readChar()
		if err != nil {
			return set, err
		}
		tokens, isSingle = append(tokens, []byte{ch}), append(isSingle, kind)
	}
	// Ranges: a single char, a '-' that is not first or last, and a single char.
	for k := 0; k < len(tokens); k++ {
		if k+2 < len(tokens) && isSingle[k] != 0 && isSingle[k+2] != 0 && isSingle[k+1] == 2 {
			lo, hi := tokens[k][0], tokens[k+2][0]
			if lo > hi {
				return set, fmt.Errorf("range-endpoints of '%s-%s' are in reverse collating sequence order", trEscape(lo), trEscape(hi))
			}
			for b := int(lo); b <= int(hi); b++ {
				set.list = append(set.list, byte(b))
			}
			k += 2
			continue
		}
		set.list = append(set.list, tokens[k]...)
	}
	return set, nil
}

func trEscape(b byte) string { return string([]byte{b}) }

func runTr(ctx context.Context, c *Call) int {
	if hasHelp(c.Args) {
		return c.help(trUsage)
	}
	opts, operands, err := getopt("tr", trOptions, c.Args)
	if err != nil {
		return c.fail(err, 1)
	}
	var complement, del, squeeze bool
	for _, opt := range opts {
		switch opt.id {
		case "complement", "C":
			complement = true
		case "delete":
			del = true
		case "squeeze-repeats":
			squeeze = true
		}
	}
	if len(operands) == 0 {
		c.errf("tr", "missing operand")
		c.tryHelp("tr")
		return 1
	}
	maxOperands := 2
	if del && !squeeze {
		maxOperands = 1
	}
	if len(operands) > maxOperands {
		c.errf("tr", "extra operand %s", quoteLocale(operands[maxOperands]))
		if maxOperands == 1 {
			_, _ = fmt.Fprintln(c.Stderr, "Only one string may be given when deleting without squeezing repeats.")
		}
		c.tryHelp("tr")
		return 1
	}
	if len(operands) == 1 && (!del && !squeeze || del && squeeze) {
		c.errf("tr", "missing operand after %s", quoteLocale(operands[0]))
		if del {
			_, _ = fmt.Fprintln(c.Stderr, "Two strings must be given when both deleting and squeezing repeats.")
		} else {
			_, _ = fmt.Fprintln(c.Stderr, "Two strings must be given when translating.")
		}
		c.tryHelp("tr")
		return 1
	}

	set1, err := parseTrSet(c, operands[0])
	if err != nil {
		c.errf("tr", "%s", err)
		return 1
	}
	if complement {
		var in [256]bool
		for _, b := range set1.list {
			in[b] = true
		}
		set1.list = set1.list[:0]
		for b := range 256 {
			if !in[b] {
				set1.list = append(set1.list, byte(b))
			}
		}
	}
	var set2 trSet
	if len(operands) == 2 {
		if set2, err = parseTrSet(c, operands[1]); err != nil {
			c.errf("tr", "%s", err)
			return 1
		}
	}

	var deleteSet, squeezeSet [256]bool
	var xlate [256]byte
	for b := range xlate {
		xlate[b] = byte(b)
	}
	translate := len(operands) == 2 && !del
	switch {
	case del:
		for _, b := range set1.list {
			deleteSet[b] = true
		}
		if squeeze {
			for _, b := range set2.list {
				squeezeSet[b] = true
			}
		}
	case translate:
		if set2.otherClass {
			c.errf("tr", "when translating, the only character classes that may appear in\nstring2 are 'upper' and 'lower'")
			return 1
		}
		if len(set2.list) == 0 {
			if len(set1.list) > 0 {
				c.errf("tr", "when not truncating set1, string2 must be non-empty")
				return 1
			}
		} else {
			for i, b := range set1.list {
				xlate[b] = set2.list[min(i, len(set2.list)-1)]
			}
		}
		if squeeze {
			for _, b := range set2.list {
				squeezeSet[b] = true
			}
		}
	default: // squeeze only
		for _, b := range set1.list {
			squeezeSet[b] = true
		}
	}

	out := newOutput(c.Stdout)
	defer func() { _ = out.Flush() }()
	buf := make([]byte, 32<<10)
	last := -1
	for {
		if err := ctx.Err(); err != nil {
			return 1
		}
		got, rerr := c.Stdin.Read(buf)
		for _, b := range buf[:got] {
			if deleteSet[b] {
				continue
			}
			b = xlate[b]
			if squeeze && squeezeSet[b] && last == int(b) {
				continue
			}
			last = int(b)
			_ = out.WriteByte(b)
		}
		if out.failed() {
			return 1
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				c.errf("tr", "read error: %s", errText(rerr))
				return 1
			}
			break
		}
	}
	if err := out.Flush(); err != nil {
		return 1
	}
	return 0
}

// quoteLocale quotes an operand with ASCII quotes, as the message of head does.
// GNU quotes with the quotation marks of the locale, which a UTF-8 locale
// writes as curly quotes.
func quoteLocale(s string) string { return "'" + s + "'" }
