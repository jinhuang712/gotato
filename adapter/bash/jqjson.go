package bash

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"math/big"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This file is the JSON side of jq: a parser that records the key order of
// each object it reads, and an encoder that writes values the way jq 1.7 does.
//
// gojq keeps an object as a map[string]any, so the order of its keys is lost
// as soon as the object is built. The parser keeps the order of every object
// whose keys are not already sorted, in a side table keyed by the identity of
// the map; the encoder looks an object up there. An object that went through
// the program unchanged (for example `.`, `.items[0]`, `.[]`) is written in
// its input order, as jq does. An object the program built or changed is a new
// map and is written with its keys sorted. That is the one known difference in
// output order (see the usage text of jq).

// maxJSONDepth is how deep a value may nest, as jq's parser allows.
const maxJSONDepth = 10000

// keyOrders remembers the order of keys of the objects a parser built.
type keyOrders map[uintptr]orderedKeys

type orderedKeys struct {
	keys []string
	m    map[string]any // keeps the map alive, so its address is not reused
}

// record notes the order of the keys of m, when it is not the sorted one.
func (o keyOrders) record(m map[string]any, keys []string) {
	if !slices.IsSorted(keys) {
		o[reflect.ValueOf(m).Pointer()] = orderedKeys{keys: keys, m: m}
	}
}

func (o keyOrders) of(m map[string]any) []string {
	if e, ok := o[reflect.ValueOf(m).Pointer()]; ok && len(e.keys) == len(m) {
		for _, k := range e.keys {
			if _, ok := m[k]; !ok {
				return nil
			}
		}
		return e.keys
	}
	return nil
}

// jsonParser reads the values of a JSON text one after another.
type jsonParser struct {
	data   []byte
	pos    int
	orders keyOrders
}

// jsonSyntaxError is a text that is not JSON.
type jsonSyntaxError struct {
	msg       string
	line, col int
}

func (e *jsonSyntaxError) Error() string {
	return fmt.Sprintf("%s at line %d, column %d", e.msg, e.line, e.col)
}

func (p *jsonParser) fail(msg string) error {
	line, col := 1, 0
	for _, b := range p.data[:min(p.pos+1, len(p.data))] {
		if b == '\n' {
			line++
			col = 0
		} else {
			col++
		}
	}
	return &jsonSyntaxError{msg: msg, line: line, col: col}
}

func (p *jsonParser) skipSpace() {
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

// next parses the next value; ok is false at the end of the text.
func (p *jsonParser) next() (v any, ok bool, err error) {
	if p.pos == 0 && bytes.HasPrefix(p.data, []byte("\xef\xbb\xbf")) {
		p.pos = 3
	}
	p.skipSpace()
	if p.pos >= len(p.data) {
		return nil, false, nil
	}
	v, err = p.value(0)
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

func (p *jsonParser) value(depth int) (any, error) {
	if depth > maxJSONDepth {
		return nil, p.fail("Exceeds depth limit for parsing")
	}
	p.skipSpace()
	if p.pos >= len(p.data) {
		return nil, p.fail("Unfinished JSON term at EOF")
	}
	switch c := p.data[p.pos]; {
	case c == '{':
		return p.object(depth)
	case c == '[':
		return p.array(depth)
	case c == '"':
		return p.str()
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	default:
		for _, lit := range []struct {
			text string
			v    any
		}{{"true", true}, {"false", false}, {"null", nil}} {
			if bytes.HasPrefix(p.data[p.pos:], []byte(lit.text)) {
				if end := p.pos + len(lit.text); end >= len(p.data) || !isLiteralByte(p.data[end]) {
					p.pos = end
					return lit.v, nil
				}
			}
		}
		if c := p.data[p.pos]; c == 't' || c == 'f' || c == 'n' {
			return nil, p.failWord("Invalid literal")
		}
		return nil, p.failWord("Invalid numeric literal")
	}
}

// failWord reports a word that is not a literal, at the end of the word: jq
// finds the error when it reads the byte after it.
func (p *jsonParser) failWord(msg string) error {
	for p.pos < len(p.data) && isLiteralByte(p.data[p.pos]) {
		p.pos++
	}
	return p.fail(msg)
}

func isLiteralByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

func (p *jsonParser) array(depth int) (any, error) {
	p.pos++
	out := []any{}
	p.skipSpace()
	if p.pos < len(p.data) && p.data[p.pos] == ']' {
		p.pos++
		return out, nil
	}
	for {
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.skipSpace()
		if p.pos >= len(p.data) {
			return nil, p.fail("Unfinished JSON term at EOF")
		}
		switch p.data[p.pos] {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return out, nil
		default:
			return nil, p.fail("Expected separator between values")
		}
	}
}

func (p *jsonParser) object(depth int) (any, error) {
	p.pos++
	out := map[string]any{}
	var keys []string
	p.skipSpace()
	if p.pos < len(p.data) && p.data[p.pos] == '}' {
		p.pos++
		return out, nil
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.data) {
			return nil, p.fail("Unfinished JSON term at EOF")
		}
		if p.data[p.pos] != '"' {
			return nil, p.fail("Object keys must be strings")
		}
		k, err := p.str()
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if p.pos >= len(p.data) || p.data[p.pos] != ':' {
			return nil, p.fail("Objects must consist of key:value pairs")
		}
		p.pos++
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		if _, dup := out[k.(string)]; !dup {
			keys = append(keys, k.(string))
		}
		out[k.(string)] = v
		p.skipSpace()
		if p.pos >= len(p.data) {
			return nil, p.fail("Unfinished JSON term at EOF")
		}
		switch p.data[p.pos] {
		case ',':
			p.pos++
		case '}':
			p.pos++
			if !slices.IsSorted(keys) {
				p.orders[reflect.ValueOf(out).Pointer()] = orderedKeys{keys: keys, m: out}
			}
			return out, nil
		default:
			return nil, p.fail("Expected separator between values")
		}
	}
}

func (p *jsonParser) str() (any, error) {
	p.pos++ // the opening quote
	start := p.pos
	// The common case: no escape and no invalid byte.
	for i := start; i < len(p.data); i++ {
		c := p.data[i]
		if c == '"' {
			p.pos = i + 1
			if utf8.Valid(p.data[start:i]) {
				return string(p.data[start:i]), nil
			}
			return validUTF8(string(p.data[start:i])), nil
		}
		if c == '\\' {
			break
		}
		if c < 0x20 {
			p.pos = i
			return nil, p.fail("Invalid string: control characters from U+0000 through U+001F must be escaped")
		}
	}
	var sb strings.Builder
	sb.Write(p.data[start:p.pos])
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		switch {
		case c == '"':
			p.pos++
			return validUTF8(sb.String()), nil
		case c < 0x20:
			return nil, p.fail("Invalid string: control characters from U+0000 through U+001F must be escaped")
		case c != '\\':
			sb.WriteByte(c)
			p.pos++
		default:
			p.pos++
			if p.pos >= len(p.data) {
				return nil, p.fail("Unfinished string")
			}
			e := p.data[p.pos]
			p.pos++
			switch e {
			case '"', '\\', '/':
				sb.WriteByte(e)
			case 'b':
				sb.WriteByte('\b')
			case 'f':
				sb.WriteByte('\f')
			case 'n':
				sb.WriteByte('\n')
			case 'r':
				sb.WriteByte('\r')
			case 't':
				sb.WriteByte('\t')
			case 'u':
				r, ok := p.hex4()
				if !ok {
					return nil, p.fail("Invalid escape")
				}
				if r >= 0xD800 && r < 0xDC00 {
					if p.pos+1 < len(p.data) && p.data[p.pos] == '\\' && p.data[p.pos+1] == 'u' {
						save := p.pos
						p.pos += 2
						if r2, ok := p.hex4(); ok && r2 >= 0xDC00 && r2 < 0xE000 {
							sb.WriteRune(0x10000 + (r-0xD800)<<10 + (r2 - 0xDC00))
							continue
						}
						p.pos = save
					}
					sb.WriteRune(utf8.RuneError)
				} else if r >= 0xDC00 && r < 0xE000 {
					sb.WriteRune(utf8.RuneError)
				} else {
					sb.WriteRune(r)
				}
			default:
				return nil, p.fail("Invalid escape")
			}
		}
	}
	return nil, p.fail("Unfinished string")
}

func (p *jsonParser) hex4() (rune, bool) {
	if p.pos+4 > len(p.data) {
		return 0, false
	}
	n, err := strconv.ParseUint(string(p.data[p.pos:p.pos+4]), 16, 32)
	if err != nil {
		return 0, false
	}
	p.pos += 4
	return rune(n), true
}

func (p *jsonParser) number() (any, error) {
	start := p.pos
	i := p.pos
	if p.data[i] == '-' {
		i++
	}
	digits := i
	for i < len(p.data) && p.data[i] >= '0' && p.data[i] <= '9' {
		i++
	}
	if i == digits {
		p.pos = i
		return nil, p.failWord("Invalid numeric literal")
	}
	integer := true
	if i < len(p.data) && p.data[i] == '.' {
		integer = false
		i++
		for i < len(p.data) && p.data[i] >= '0' && p.data[i] <= '9' {
			i++
		}
	}
	if i < len(p.data) && (p.data[i] == 'e' || p.data[i] == 'E') {
		integer = false
		i++
		if i < len(p.data) && (p.data[i] == '+' || p.data[i] == '-') {
			i++
		}
		expDigits := i
		for i < len(p.data) && p.data[i] >= '0' && p.data[i] <= '9' {
			i++
		}
		if i == expDigits {
			p.pos = i
			return nil, p.failWord("Invalid numeric literal")
		}
	}
	if i < len(p.data) && isLiteralByte(p.data[i]) {
		p.pos = i
		return nil, p.failWord("Invalid numeric literal")
	}
	lit := string(p.data[start:i])
	p.pos = i
	if lit == "-0" {
		return math.Copysign(0, -1), nil
	}
	if integer {
		if n, err := strconv.ParseInt(lit, 10, 64); err == nil && int64(int(n)) == n {
			return int(n), nil
		}
		if b, ok := new(big.Int).SetString(lit, 10); ok {
			return b, nil
		}
	}
	f, err := strconv.ParseFloat(lit, 64)
	if err != nil && !isRangeError(err) {
		p.pos = i
		return nil, p.failWord("Invalid numeric literal")
	}
	return f, nil
}

func isRangeError(err error) bool {
	ne, ok := err.(*strconv.NumError)
	return ok && ne.Err == strconv.ErrRange
}

// jsonEncoder writes values as jq does.
type jsonEncoder struct {
	w      io.Writer
	orders keyOrders
	indent string // "" for compact output
	sorted bool   // -S
	ascii  bool   // -a
	buf    []byte
}

// encode writes v, and reports a failed write.
func (e *jsonEncoder) encode(v any) error {
	e.buf = e.buf[:0]
	e.value(v, 0)
	_, err := e.w.Write(e.buf)
	return err
}

func (e *jsonEncoder) newline(depth int) {
	if e.indent == "" {
		return
	}
	e.buf = append(e.buf, '\n')
	for range depth {
		e.buf = append(e.buf, e.indent...)
	}
}

func (e *jsonEncoder) value(v any, depth int) {
	switch v := v.(type) {
	case nil:
		e.buf = append(e.buf, "null"...)
	case bool:
		e.buf = strconv.AppendBool(e.buf, v)
	case int:
		e.buf = strconv.AppendInt(e.buf, int64(v), 10)
	case *big.Int:
		e.buf = append(e.buf, v.String()...)
	case float64:
		e.buf = appendJQFloat(e.buf, v)
	case string:
		e.str(v)
	case []any:
		if len(v) == 0 {
			e.buf = append(e.buf, "[]"...)
			return
		}
		e.buf = append(e.buf, '[')
		for i, x := range v {
			if i > 0 {
				e.buf = append(e.buf, ',')
			}
			e.newline(depth + 1)
			e.value(x, depth+1)
		}
		e.newline(depth)
		e.buf = append(e.buf, ']')
	case map[string]any:
		if len(v) == 0 {
			e.buf = append(e.buf, "{}"...)
			return
		}
		var keys []string
		if !e.sorted {
			keys = e.orders.of(v)
		}
		if keys == nil {
			keys = make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			slices.Sort(keys)
		}
		e.buf = append(e.buf, '{')
		for i, k := range keys {
			if i > 0 {
				e.buf = append(e.buf, ',')
			}
			e.newline(depth + 1)
			e.str(k)
			e.buf = append(e.buf, ':')
			if e.indent != "" {
				e.buf = append(e.buf, ' ')
			}
			e.value(v[k], depth+1)
		}
		e.newline(depth)
		e.buf = append(e.buf, '}')
	default:
		e.buf = append(e.buf, fmt.Sprint(v)...)
	}
}

const hexDigits = "0123456789abcdef"

// str writes a string with jq's escapes: the short forms for the usual
// control characters, \u00XX for the others and for DEL, U+FFFD for an invalid
// byte, and everything else as it is (as \uXXXX with -a).
func (e *jsonEncoder) str(s string) {
	e.buf = append(e.buf, '"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			i++
			switch c {
			case '"':
				e.buf = append(e.buf, '\\', '"')
			case '\\':
				e.buf = append(e.buf, '\\', '\\')
			case '\n':
				e.buf = append(e.buf, '\\', 'n')
			case '\t':
				e.buf = append(e.buf, '\\', 't')
			case '\r':
				e.buf = append(e.buf, '\\', 'r')
			case '\b':
				e.buf = append(e.buf, '\\', 'b')
			case '\f':
				e.buf = append(e.buf, '\\', 'f')
			default:
				if c < 0x20 || c == 0x7f {
					e.buf = append(e.buf, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&15])
				} else {
					e.buf = append(e.buf, c)
				}
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == utf8.RuneError && size == 1:
			if e.ascii {
				e.buf = fmt.Appendf(e.buf, `\u%04x`, 0xFFFD)
			} else {
				e.buf = utf8.AppendRune(e.buf, utf8.RuneError)
			}
		case e.ascii && r >= 0x10000:
			r -= 0x10000
			e.buf = fmt.Appendf(e.buf, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3ff))
		case e.ascii:
			e.buf = fmt.Appendf(e.buf, `\u%04x`, r)
		default:
			e.buf = utf8.AppendRune(e.buf, r)
		}
	}
	e.buf = append(e.buf, '"')
}

// appendJQFloat writes a number as jq's printer does: the shortest digits that
// read back as the same double, in plain form unless the exponent is below -4
// or more than 15 past the digits, and then as d.ddde+XX. NaN is null and an
// infinity is the largest double.
func appendJQFloat(buf []byte, f float64) []byte {
	switch {
	case math.IsNaN(f):
		return append(buf, "null"...)
	case math.IsInf(f, 1):
		f = math.MaxFloat64
	case math.IsInf(f, -1):
		f = -math.MaxFloat64
	}
	if f == 0 {
		if math.Signbit(f) {
			return append(buf, "-0"...)
		}
		return append(buf, '0')
	}
	if f < 0 {
		buf = append(buf, '-')
		f = -f
	}
	sci := strconv.FormatFloat(f, 'e', -1, 64) // d.ddde±XX
	mant, exp, _ := strings.Cut(sci, "e")
	digits := strings.Replace(mant, ".", "", 1)
	x, _ := strconv.Atoi(exp)
	decpt := x + 1
	nd := len(digits)
	switch {
	case decpt <= -4 || decpt > nd+15:
		buf = append(buf, digits[0])
		if nd > 1 {
			buf = append(buf, '.')
			buf = append(buf, digits[1:]...)
		}
		buf = append(buf, 'e')
		if x < 0 {
			buf = append(buf, '-')
			x = -x
		} else {
			buf = append(buf, '+')
		}
		if x < 10 {
			buf = append(buf, '0')
		}
		return strconv.AppendInt(buf, int64(x), 10)
	case decpt <= 0:
		buf = append(buf, "0."...)
		for range -decpt {
			buf = append(buf, '0')
		}
		return append(buf, digits...)
	case decpt >= nd:
		buf = append(buf, digits...)
		for range decpt - nd {
			buf = append(buf, '0')
		}
		return buf
	default:
		buf = append(buf, digits[:decpt]...)
		buf = append(buf, '.')
		return append(buf, digits[decpt:]...)
	}
}
