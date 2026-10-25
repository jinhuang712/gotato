package bash

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// quoteName quotes a file name as GNU tools do in their messages with quotef:
// a name made only of characters a shell leaves alone is written as it is, any
// other is quoted for a shell.
func quoteName(name string) string {
	if name != "" && !needsQuotes(name) {
		return name
	}
	return quoteAlways(name)
}

// quoteAlways quotes a file name as GNU tools do with quoteaf: always, in the
// form a shell reads back as the same name. A name with a single quote and no
// character special inside double quotes is written in double quotes; a
// control character is written as $'\n'.
func quoteAlways(name string) string {
	if strings.Contains(name, "'") && !strings.ContainsAny(name, "\"$`\\!") && !hasControl(name) {
		return `"` + name + `"`
	}
	var b strings.Builder
	open := false
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			if open {
				b.WriteByte('\'')
				open = false
			}
			b.WriteString(`$'` + controlEscape(r) + `'`)
			continue
		}
		if !open {
			b.WriteByte('\'')
			open = true
		}
		if r == '\'' {
			b.WriteString(`'\''`)
			continue
		}
		b.WriteRune(r)
	}
	if open || b.Len() == 0 {
		if b.Len() == 0 {
			b.WriteByte('\'')
		}
		b.WriteByte('\'')
	}
	return b.String()
}

// needsQuotes reports whether a shell would treat any character of name as
// special, as gnulib's shell quoting style decides.
func needsQuotes(name string) bool {
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("%+,-./:@]_", r):
		case r == '#' || r == '~':
			if i == 0 {
				return true
			}
		case r == '{' || r == '}':
			if utf8.RuneCountInString(name) == 1 {
				return true
			}
		case r >= utf8.RuneSelf:
			if r == utf8.RuneError || !unicode.IsPrint(r) {
				return true
			}
		default:
			return true
		}
	}
	return false
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// controlEscape is the C escape of a control character inside $'...'.
func controlEscape(r rune) string {
	switch r {
	case '\a':
		return `\a`
	case '\b':
		return `\b`
	case '\t':
		return `\t`
	case '\n':
		return `\n`
	case '\v':
		return `\v`
	case '\f':
		return `\f`
	case '\r':
		return `\r`
	}
	return `\` + string('0'+byte(r>>6&7)) + string('0'+byte(r>>3&7)) + string('0'+byte(r&7))
}
