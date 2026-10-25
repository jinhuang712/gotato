package bash

import (
	"context"
	"strings"
	"unicode/utf8"
)

// runEcho is the echo builtin of bash, not the program of GNU coreutils: the
// model writes bash. Its options are -n, -e and -E, in any group of those
// letters; the first argument that is not such a group, and every one after
// it, is printed. It has no --help: bash's echo prints "--help" as it does any
// other text.
func runEcho(_ context.Context, c *Call) int {
	args := c.Args
	newline, escapes := true, false
	for len(args) > 0 {
		a := args[0]
		if len(a) < 2 || a[0] != '-' || strings.Trim(a[1:], "neE") != "" {
			break
		}
		for _, f := range a[1:] {
			switch f {
			case 'n':
				newline = false
			case 'e':
				escapes = true
			case 'E':
				escapes = false
			}
		}
		args = args[1:]
	}
	text := strings.Join(args, " ")
	if escapes {
		var stop bool
		text, stop = echoEscapes(text)
		if stop {
			newline = false
		}
	}
	if newline {
		text += "\n"
	}
	if _, err := c.Stdout.Write([]byte(text)); err != nil {
		return 1
	}
	return 0
}

// echoEscapes expands the backslash escapes of echo -e. stop is true when \c
// ended the output.
func echoEscapes(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch ch := s[i]; ch {
		case 'a':
			b.WriteByte(7)
		case 'b':
			b.WriteByte(8)
		case 'c':
			return b.String(), true
		case 'e', 'E':
			b.WriteByte(27)
		case 'f':
			b.WriteByte(12)
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte(11)
		case '\\':
			b.WriteByte('\\')
		case '0', '1', '2', '3', '4', '5', '6', '7':
			// \0nnn takes up to three digits after the 0; \nnn takes up to three.
			start, limit := i, 3
			if ch == '0' {
				start, limit = i+1, 3
			}
			j, v := start, 0
			for j < len(s) && j-start < limit && s[j] >= '0' && s[j] <= '7' {
				v = v*8 + int(s[j]-'0')
				j++
			}
			b.WriteByte(byte(v))
			i = j - 1
		case 'x', 'u', 'U':
			digits := map[byte]int{'x': 2, 'u': 4, 'U': 8}[ch]
			j, v := i+1, 0
			for j < len(s) && j-(i+1) < digits && isHex(s[j]) {
				v = v*16 + hexVal(s[j])
				j++
			}
			switch {
			case j == i+1:
				b.WriteByte('\\')
				b.WriteByte(ch)
			case ch == 'x':
				b.WriteByte(byte(v))
				i = j - 1
			default:
				if v > utf8.MaxRune || v >= 0xd800 && v <= 0xdfff {
					b.WriteString(s[i-1 : j])
				} else {
					b.WriteRune(rune(v))
				}
				i = j - 1
			}
		default:
			b.WriteByte('\\')
			b.WriteByte(ch)
		}
	}
	return b.String(), false
}

func isHex(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}

func hexVal(b byte) int {
	switch {
	case b >= 'a':
		return int(b-'a') + 10
	case b >= 'A':
		return int(b-'A') + 10
	}
	return int(b - '0')
}
