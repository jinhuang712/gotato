package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"strings"

	gotato "github.com/jinhuang712/gotato/gotato-core"
)

// Pure card building: the Lark `content` JSON of an interactive message
// (card schema 1.0). A card shows the text, and a grey footer that says which
// Session, instance and model answered.

// maxReplyRunes keeps a card well under Lark's request size limit (30 KB).
const maxReplyRunes = 8000

const truncatedMarker = "\n\n... (truncated)"

// cardInfo says who answers; it fills the header and the footer.
type cardInfo struct {
	Session  string
	Instance string // display label, see instanceLabel
	Model    string
}

// headerTemplates are Lark header colours; an instance label picks one.
// Exactly eight, see templateFor.
var headerTemplates = []string{"blue", "green", "orange", "purple", "turquoise", "wathet", "carmine", "indigo"}

func templateFor(label string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(label))
	return headerTemplates[int(h.Sum32()%8)]
}

func (c cardInfo) footer() string {
	f := fmt.Sprintf("session %s · instance %s", shortID(c.Session), c.Instance)
	if c.Model != "" {
		f += " · " + c.Model
	}
	return f
}

// cardContent builds the card JSON of a text. Text longer than maxReplyRunes
// is cut with a marker. JSON escaping is json.Marshal's; the text is passed
// to Lark's markdown element as it is.
func cardContent(c cardInfo, text string) string {
	text = strings.TrimSpace(text)
	if r := []rune(text); len(r) > maxReplyRunes {
		text = string(r[:maxReplyRunes]) + truncatedMarker
	}
	if text == "" {
		text = "(the agent had nothing to say)"
	}
	title := c.Instance
	if c.Model != "" {
		title = c.Model + " · " + c.Instance
	}
	card := map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{
			"template": templateFor(c.Instance),
			"title":    map[string]string{"tag": "plain_text", "content": title},
		},
		"elements": []any{
			map[string]any{"tag": "markdown", "content": text},
			map[string]any{"tag": "note", "elements": []any{
				map[string]string{"tag": "plain_text", "content": c.footer()},
			}},
		},
	}
	b, _ := json.Marshal(card)
	return string(b)
}

// replyCard is the card of a settled Run: the final text when it completed,
// and a short notice otherwise.
func replyCard(c cardInfo, status gotato.RunStatus, finalText string) string {
	text := strings.TrimSpace(finalText)
	if status != gotato.RunCompleted && text == "" {
		text = fmt.Sprintf("The run did not complete (%s).", status)
	}
	return cardContent(c, text)
}

// instanceLabel picks the name shown in the card: the override, else
// $POD_NAME, else the host name, else the Runner's instance in short form.
func instanceLabel(override, runnerInstance string) string {
	if override != "" {
		return override
	}
	if p := os.Getenv("POD_NAME"); p != "" {
		return p
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	if len(runnerInstance) > 12 {
		return runnerInstance[:12]
	}
	return runnerInstance
}
