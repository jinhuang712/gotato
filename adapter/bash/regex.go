package bash

import (
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"strings"
	"unicode/utf8"
)

// This file turns the patterns of GNU grep into Go's regexp (RE2) syntax. The
// forms that mean the same are translated, and the forms RE2 cannot match the
// same way (back-references, word-edge anchors) are refused with an error, so
// a pattern never silently matches something else.

type patternSyntax int

const (
	syntaxBRE   patternSyntax = iota // grep, grep -G
	syntaxERE                        // grep -E
	syntaxFixed                      // grep -F
)

// patternExpr translates the patterns, any of which may match, into one RE2
// expression (not yet compiled, and without the case flag).
func patternExpr(patterns []string, syn patternSyntax, wholeLine bool) (string, error) {
	parts := make([]string, 0, len(patterns))
	for _, p := range patterns {
		var re string
		switch syn {
		case syntaxFixed:
			re = regexp.QuoteMeta(p)
		default:
			var err error
			if re, err = translate(p, syn == syntaxERE); err != nil {
				return "", err
			}
		}
		parts = append(parts, "(?:"+re+")")
	}
	expr := strings.Join(parts, "|")
	if wholeLine {
		expr = "^(?:" + expr + ")$"
	}
	return expr, nil
}

// compileExpr compiles an expression with POSIX leftmost-longest matching.
func compileExpr(expr string, ignoreCase bool) (*regexp.Regexp, error) {
	if ignoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, regexpError(err)
	}
	re.Longest()
	return re, nil
}

// hasAnchor reports whether an RE2 expression holds a ^ or $ anchor outside a
// bracket expression.
func hasAnchor(expr string) bool {
	for i := 0; i < len(expr); i++ {
		switch expr[i] {
		case '\\':
			i++
		case '[':
			for i++; i < len(expr) && expr[i] != ']'; i++ {
				if expr[i] == '\\' {
					i++
				}
			}
		case '^', '$':
			return true
		}
	}
	return false
}

// translate converts one GNU pattern, basic (BRE) or extended (ERE), to RE2.
func translate(p string, ere bool) (string, error) {
	var b strings.Builder
	t := translator{src: p, ere: ere, out: &b}
	if err := t.run(); err != nil {
		return "", err
	}
	return b.String(), nil
}

type translator struct {
	src string
	pos int
	ere bool
	out *strings.Builder
}

func (t *translator) run() error {
	atStart := true // at the start of the pattern, or of a group or an alternative
	for t.pos < len(t.src) {
		c := t.src[t.pos]
		wasStart := atStart
		atStart = false
		switch c {
		case '\\':
			if t.pos+1 >= len(t.src) {
				return gnuError("Trailing backslash")
			}
			next, size := utf8.DecodeRuneInString(t.src[t.pos+1:])
			t.pos += 1 + size
			switch {
			case !t.ere && (next == '(' || next == '|'):
				t.out.WriteRune(next)
				atStart = true
			case !t.ere && (next == ')' || next == '+' || next == '?'):
				t.out.WriteRune(next)
			case !t.ere && next == '{':
				if err := t.interval(true); err != nil {
					return err
				}
			case next >= '1' && next <= '9':
				return fmt.Errorf("back-references (\\%c) are not supported", next)
			case next == '<' || next == '>':
				return fmt.Errorf("\\%c is not supported; use \\b for a word edge", next)
			case next == '`' || next == '\'':
				return fmt.Errorf("\\%c is not supported; use ^ and $", next)
			case next == 'w':
				t.out.WriteString(`[\p{L}\p{N}_]`)
			case next == 'W':
				t.out.WriteString(`[^\p{L}\p{N}_]`)
			case next == 's':
				t.out.WriteString(`[\t\n\v\f\r ]`)
			case next == 'S':
				t.out.WriteString(`[^\t\n\v\f\r ]`)
			case next == 'b':
				t.out.WriteString(`\b`)
			case next == 'B':
				t.out.WriteString(`\B`)
			default:
				t.out.WriteString(regexp.QuoteMeta(string(next)))
			}
		case '[':
			if err := t.bracket(); err != nil {
				return err
			}
		case '*':
			t.pos++
			if wasStart {
				t.out.WriteString(`\*`) // a star with nothing to repeat is a plain star
			} else {
				t.out.WriteByte('*')
			}
			// BRE: "^*" is a star after the anchor; the anchor keeps the start state.
		case '^':
			t.pos++
			if t.ere || wasStart {
				t.out.WriteByte('^')
				atStart = wasStart && !t.ere // in a BRE a star after ^ is literal
			} else {
				t.out.WriteString(`\^`)
			}
		case '$':
			t.pos++
			if t.ere || t.dollarIsAnchor() {
				t.out.WriteByte('$')
			} else {
				t.out.WriteString(`\$`)
			}
		case '.':
			t.pos++
			t.out.WriteString(`.`)
		case '(', ')', '|':
			t.pos++
			if t.ere {
				t.out.WriteByte(c)
				atStart = c != ')'
			} else {
				t.out.WriteString(`\` + string(c))
			}
		case '+', '?':
			t.pos++
			switch {
			case !t.ere:
				t.out.WriteString(`\` + string(c))
			case wasStart:
				t.out.WriteString(`\` + string(c))
			default:
				t.out.WriteByte(c)
			}
		case '{':
			if t.ere {
				if err := t.interval(false); err != nil {
					return err
				}
			} else {
				t.pos++
				t.out.WriteString(`\{`)
			}
		case '}':
			t.pos++
			t.out.WriteString(`\}`)
		default:
			r, size := utf8.DecodeRuneInString(t.src[t.pos:])
			t.pos += size
			if r == utf8.RuneError && size == 1 {
				return fmt.Errorf("invalid UTF-8 in the pattern")
			}
			t.out.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	return nil
}

// dollarIsAnchor reports whether the $ just read is an anchor in a BRE: it is
// at the end of the pattern or before \) or \|.
func (t *translator) dollarIsAnchor() bool {
	rest := t.src[t.pos:]
	return rest == "" || strings.HasPrefix(rest, `\)`) || strings.HasPrefix(rest, `\|`)
}

// interval translates {n}, {n,}, {n,m} and {,m} (written \{ \} in a BRE). In an
// ERE a { that does not start a valid interval is a plain brace.
func (t *translator) interval(escaped bool) error {
	start := t.pos
	if !escaped {
		t.pos++ // the {
	}
	i := t.pos
	for i < len(t.src) && t.src[i] >= '0' && t.src[i] <= '9' {
		i++
	}
	lo := t.src[t.pos:i]
	hi, comma := "", false
	if i < len(t.src) && t.src[i] == ',' {
		comma = true
		j := i + 1
		for j < len(t.src) && t.src[j] >= '0' && t.src[j] <= '9' {
			j++
		}
		hi = t.src[i+1 : j]
		i = j
	}
	closer := "}"
	if escaped {
		closer = `\}`
	}
	if !strings.HasPrefix(t.src[i:], closer) || (lo == "" && hi == "") {
		if !escaped {
			t.pos = start + 1
			t.out.WriteString(`\{`)
			return nil
		}
		return gnuError("Invalid content of \\{\\}")
	}
	t.pos = i + len(closer)
	if lo == "" {
		lo = "0"
	}
	switch {
	case !comma:
		fmt.Fprintf(t.out, "{%s}", lo)
	default:
		fmt.Fprintf(t.out, "{%s,%s}", lo, hi)
	}
	return nil
}

// bracket translates a bracket expression [...]: inside it a backslash is an
// ordinary character, a ] first is a member, and [:class:] names a class.
func (t *translator) bracket() error {
	i := t.pos + 1
	var b strings.Builder
	b.WriteByte('[')
	if i < len(t.src) && t.src[i] == '^' {
		b.WriteByte('^')
		i++
	}
	first := true
	items := 0
	for {
		if i >= len(t.src) {
			return gnuError("Unmatched [, [^, [:, [., or [=")
		}
		c := t.src[i]
		if c == ']' && !first {
			i++
			break
		}
		first = false
		if c == '[' && i+1 < len(t.src) && (t.src[i+1] == ':' || t.src[i+1] == '.' || t.src[i+1] == '=') {
			kind := t.src[i+1]
			end := strings.Index(t.src[i+2:], string(kind)+"]")
			if end < 0 {
				return gnuError("Unmatched [, [^, [:, [., or [=")
			}
			name := t.src[i+2 : i+2+end]
			i += 2 + end + 2
			if kind != ':' {
				return fmt.Errorf("[%c%s%c] is not supported in a bracket expression", kind, name, kind)
			}
			class, ok := bracketClasses[name]
			if !ok {
				return gnuError("Invalid character class name")
			}
			b.WriteString(class)
			items++
			continue
		}
		r, size := utf8.DecodeRuneInString(t.src[i:])
		if r == utf8.RuneError && size == 1 {
			return fmt.Errorf("invalid UTF-8 in the pattern")
		}
		i += size
		if r == '-' && items > 0 && i < len(t.src) && t.src[i] != ']' {
			b.WriteByte('-') // a range
			continue
		}
		items++
		b.WriteString(escapeInClass(r))
	}
	t.pos = i
	b.WriteByte(']')
	t.out.WriteString(b.String())
	return nil
}

func escapeInClass(r rune) string {
	switch r {
	case '\\', ']', '[', '^', '-':
		return `\` + string(r)
	}
	return string(r)
}

// bracketClasses maps the POSIX classes to RE2. The letter and case classes
// are Unicode, as GNU grep's are in a UTF-8 locale; the others are ASCII.
var bracketClasses = map[string]string{
	"alpha":  `\p{L}`,
	"alnum":  `\p{L}\p{N}`,
	"upper":  `\p{Lu}`,
	"lower":  `\p{Ll}`,
	"digit":  `0-9`,
	"xdigit": `0-9A-Fa-f`,
	"space":  `\t\n\v\f\r `,
	"blank":  `\t `,
	"punct":  `!-/:-@\[-` + "`" + `{-~`,
	"print":  ` -~\x{80}-\x{10FFFF}`,
	"graph":  `!-~\x{80}-\x{10FFFF}`,
	"cntrl":  `\x00-\x1f\x7f`,
}

// regexpError words an error of regexp as GNU grep words the same mistake.
// gnuError is an error text worded, and capitalized, as GNU grep words it.
type gnuError string

func (e gnuError) Error() string { return string(e) }

func regexpError(err error) error {
	if syn, ok := errors.AsType[*syntax.Error](err); ok {
		switch syn.Code {
		case syntax.ErrMissingParen:
			return gnuError(`Unmatched ( or \(`)
		case syntax.ErrUnexpectedParen:
			return gnuError(`Unmatched ) or \)`)
		case syntax.ErrMissingBracket:
			return gnuError("Unmatched [, [^, [:, [., or [=")
		case syntax.ErrInvalidRepeatSize:
			return gnuError("Regular expression too big")
		}
		return fmt.Errorf("this pattern is not supported: %s", syn)
	}
	return err
}
