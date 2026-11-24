package fakemodel

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var directive = regexp.MustCompile(`\[quirk:([A-Za-z0-9-]*)(?:=([^\]]*))?\]`)

var knownQuirks = map[string]bool{
	"overflow": true, "overflow-midstream": true, "cut-after-text": true, "cut-in-args": true,
	"cached": true, "reasoning": true, "delay": true, "load": true,
}

// quirks are the quirks of one request.
type quirks struct {
	byName map[string]string
	// key identifies the conversation for the quirks that play a limited
	// number of times.
	key string
}

func (q *quirks) has(name string) bool { _, ok := q.byName[name]; return ok }

func hasFlag(value, flag string) bool {
	return slices.Contains(strings.Split(value, "+"), flag)
}

// extractQuirks reads the directives of the last user text.
func extractQuirks(p *parsed, _ http.Header, api string) (*quirks, string, error) {
	q := &quirks{byName: map[string]string{}}
	i := p.lastUser()
	if i < 0 {
		return q, "", nil
	}
	raw := p.events[i].text
	for _, m := range directive.FindAllStringSubmatch(raw, -1) {
		if err := checkQuirk(m[1], m[2]); err != nil {
			return nil, "", err
		}
		q.byName[m[1]] = m[2]
	}
	q.key = api + "|" + raw
	return q, strings.TrimSpace(directive.ReplaceAllString(raw, "")), nil
}

// checkQuirk refuses a quirk the fake does not know, or a value it does not
// understand, so a typo fails loudly.
func checkQuirk(name, value string) error {
	if !knownQuirks[name] {
		return fmt.Errorf("fakemodel: unknown quirk %q", name)
	}
	switch name {
	case "delay":
		if _, err := time.ParseDuration(value); err != nil {
			return fmt.Errorf("fakemodel: quirk delay=%q is not a duration", value)
		}
	case "cached":
		return checkCount(name, value)
	case "cut-after-text", "cut-in-args":
		if value != "" {
			return fmt.Errorf("fakemodel: quirk %s takes no value, got %q", name, value)
		}
	case "overflow-midstream":
		if hasFlag(value, "once") {
			return fmt.Errorf("fakemodel: quirk %s has no flag \"once\" any more; it plays on every request", name)
		}
	}
	return nil
}

// checkCount refuses a value that is not a count.
func checkCount(name, value string) error {
	if value == "" {
		return fmt.Errorf("fakemodel: quirk cached needs a count, as cached=1200")
	}
	if n, err := strconv.Atoi(value); err != nil || n < 0 {
		return fmt.Errorf("fakemodel: quirk %s=%q is not a count", name, value)
	}
	return nil
}

// times reports whether a quirk that plays a limited number of times plays
// now, and counts it.
func (g *Server) times(q *quirks, name string, limit int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := q.key + "|" + name
	if g.counts[key] >= limit {
		return false
	}
	g.counts[key]++
	return true
}

// cutNow reports whether the cut quirk plays now.
func (pl *plan) cutNow(name string) bool {
	if !pl.q.has(name) {
		return false
	}
	return pl.g.times(pl.q, name, 1)
}

// midOverflow reports whether the mid-stream overflow plays now.
func (pl *plan) midOverflow() (flags string, ok bool) {
	flags, ok = pl.q.byName["overflow-midstream"]
	return flags, ok
}

// chunked is a part of the reply that streams in chunks, named by the quirk
// that cuts the stream after its first chunk: the text, or a tool call's
// arguments.
type chunked string

const (
	textChunks chunked = "cut-after-text"
	argsChunks chunked = "cut-in-args"
)

// The size of a chunk of each part, in bytes or so.
const (
	textChunkBytes      = 5
	argsChunkBytes      = 6
	reasoningChunkBytes = 12
)

// size is the size of a chunk of the part.
func (c chunked) size() int {
	if c == argsChunks {
		return argsChunkBytes
	}
	return textChunkBytes
}

// play streams s in the chunks of part, each one through send, and after the
// first one plays the quirks that act mid-stream: the overflow, which
// overflow writes and which ends the stream (play then reports false), and
// the cut of part.
func (pl *plan) play(sw *sseWriter, part chunked, s string, send func(string), overflow func(flags string)) bool {
	for n, c := range chunks(s, part.size()) {
		send(c)
		if n > 0 {
			continue
		}
		if flags, ok := pl.midOverflow(); ok {
			pl.outcome("overflow-midstream")
			overflow(flags)
			return false
		}
		if pl.cutNow(string(part)) {
			sw.cut(pl)
		}
	}
	return true
}
