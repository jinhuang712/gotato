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
	add := func(name, value string) error {
		if !knownQuirks[name] {
			return fmt.Errorf("fakemodel: unknown quirk %q", name)
		}
		switch name {
		case "delay":
			if _, err := time.ParseDuration(value); err != nil {
				return fmt.Errorf("fakemodel: quirk delay=%q is not a duration", value)
			}
		case "cached":
			if value == "" {
				return fmt.Errorf("fakemodel: quirk cached needs a count, as cached=1200")
			}
			if n, err := strconv.Atoi(value); err != nil || n < 0 {
				return fmt.Errorf("fakemodel: quirk %s=%q is not a count", name, value)
			}
		}
		q.byName[name] = value
		return nil
	}
	text := ""
	if i := p.lastUser(); i >= 0 {
		raw := p.events[i].text
		for _, m := range directive.FindAllStringSubmatch(raw, -1) {
			if err := add(m[1], m[2]); err != nil {
				return nil, "", err
			}
		}
		text = strings.TrimSpace(directive.ReplaceAllString(raw, ""))
		q.key = api + "|" + raw
	}
	return q, text, nil
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
