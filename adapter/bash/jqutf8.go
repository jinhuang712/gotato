package bash

import (
	"strings"
	"unicode/utf8"
)

// validUTF8 replaces each byte that is not part of a valid UTF-8 sequence with
// U+FFFD, as jq does (strings.ToValidUTF8 would replace a whole run with one).
func validUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			sb.WriteRune(utf8.RuneError)
		} else {
			sb.WriteString(s[i : i+size])
		}
		i += size
	}
	return sb.String()
}
