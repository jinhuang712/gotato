package fakegateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
)

// plan is the reply the model gives.
type plan struct {
	text      string
	call      *toolCall
	reasoning string
	input     int
	cached    int
	output    int
	id        string
	outcome   func(string)
	g         *Gateway
	q         *quirks
}

type toolCall struct {
	name string
	args string
	// search says the call is the model's search, a tool_search_call item on
	// Responses.
	search bool
}

func (g *Gateway) answer(p *parsed, text string, q *quirks, tokens int) *plan {
	pl := &plan{input: tokens, id: shortHash(p.body)}
	if v, ok := q.byName["cached"]; ok {
		pl.cached, _ = strconv.Atoi(v)
		pl.cached = min(pl.cached, tokens)
	}
	last := p.lastUser()
	result, hasResult, searched := "", false, false
	for i := last + 1; i < len(p.events); i++ {
		if p.events[i].toolResult && p.events[i].search {
			searched = true
		} else if p.events[i].toolResult {
			result, hasResult = p.events[i].text, true
		}
	}
	visible := p.visibleTools()
	switch {
	case hasResult:
		pl.text = "tool said: " + result
	case q.has("load"):
		pl.load(p, visible, q.byName["load"], searched)
	case text == "use-tool" && len(visible) > 0:
		pl.call = &toolCall{name: visible[0].name, args: argsFor(visible[0])}
	default:
		pl.text = "fake: " + text
	}
	if q.has("reasoning") {
		pl.reasoning = "thinking about: " + text
	}
	pl.output = len(pl.text)/4 + len(pl.reasoning)/4 + 1
	if pl.call != nil {
		pl.output += len(pl.call.args)/4 + 1
	}
	return pl
}

// load plays the load directive: call the tool when it is visible, search for
// it when it is not, and say so when a search did not load it.
func (pl *plan) load(p *parsed, visible []toolDef, id string, searched bool) {
	name := wireName(id)
	for _, t := range visible {
		if t.name == name {
			pl.call = &toolCall{name: name, args: argsFor(t)}
			return
		}
	}
	switch {
	case searched:
		pl.text = "fake: " + name + " is not loaded"
	case p.searchName == "":
		pl.text = "fake: no search tool to load " + name
	default:
		args, _ := json.Marshal(map[string][]string{"names": {id}})
		pl.call = &toolCall{name: p.searchName, args: string(args), search: p.nativeSearch}
	}
}

func argsFor(t toolDef) string {
	if props, ok := t.schema["properties"].(map[string]any); ok {
		var names []string
		for name, v := range props {
			if m, ok := v.(map[string]any); ok && m["type"] == "string" {
				names = append(names, name)
			}
		}
		if len(names) > 0 {
			first := names[0]
			for _, n := range names {
				if n < first {
					first = n
				}
			}
			b, _ := json.Marshal(map[string]string{first: "from-tool"})
			return string(b)
		}
	}
	return "{}"
}

// chunks splits s into pieces of about size bytes, at least two when s has
// two or more bytes, so a stream can be cut between them.
func chunks(s string, size int) []string {
	if s == "" {
		return nil
	}
	size = max(1, min(size, (len(s)+1)/2))
	var out []string
	for len(s) > size {
		cut := size
		for cut < len(s) && s[cut]&0xC0 == 0x80 { // keep a UTF-8 rune whole
			cut++
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	return append(out, s)
}

func shortHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}
