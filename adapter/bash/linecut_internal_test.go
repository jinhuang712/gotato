package bash

import (
	"bytes"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

// cutReference is the cut written the plain way: whole text in hand, line by
// line.
func cutReference(text string, max int) string {
	var out strings.Builder
	for len(text) > 0 {
		line, rest, hadNL := strings.Cut(text, "\n")
		text = rest
		if len(line) > max {
			n := max
			for n > 0 && !utf8.RuneStart(line[n]) {
				n--
			}
			keep := line[:n]
			out.WriteString(keep + lineCutMarker(len(line)))
		} else {
			out.WriteString(line)
		}
		if hadNL {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

// However the writes are split, the cut gives the same bytes as the plain way:
// the first max bytes of a line, ended at a character boundary, and a marker.
func TestLineCutterAgreesWithTheReference(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	alphabet := []string{"a", "b", "é", "日", "😀", "\n", "\n", " "}
	for range 2000 {
		var sb strings.Builder
		for range rng.Intn(60) {
			sb.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		text := sb.String()
		max := 1 + rng.Intn(12)
		var got bytes.Buffer
		c := newLineCutter(&got, max)
		for rest := []byte(text); len(rest) > 0; {
			n := 1 + rng.Intn(7)
			n = min(n, len(rest))
			if _, err := c.Write(rest[:n]); err != nil {
				t.Fatal(err)
			}
			rest = rest[n:]
		}
		_ = c.Close()
		if want := cutReference(text, max); got.String() != want {
			t.Fatalf("max %d, text %q\n got %q\nwant %q", max, text, got.String(), want)
		}
		if !utf8.ValidString(got.String()) {
			t.Fatalf("not valid UTF-8: %q", got.String())
		}
	}
}
