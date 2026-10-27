package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// What the fake Lark's driver API answers (internal/fakelark).
type sentDelivery struct {
	Endpoint string `json:"endpoint"`
	Status   int    `json:"status"`
	Error    string `json:"error"`
}

type sent struct {
	EventID    string         `json:"event_id"`
	MessageID  string         `json:"message_id"`
	Chat       string         `json:"chat"`
	Deliveries []sentDelivery `json:"deliveries"`
}

func (s sent) acked() bool {
	for _, d := range s.Deliveries {
		if d.Status >= 200 && d.Status < 300 {
			return true
		}
	}
	return false
}

type card struct {
	Time      time.Time `json:"time"`
	MessageID string    `json:"message_id"`
	Chat      string    `json:"chat"`
	ReplyTo   string    `json:"reply_to"`
	Text      string    `json:"text"`
	Session   string    `json:"session"`  // the footer's short ID: the last 8 characters
	Instance  string    `json:"instance"` // the pod name
}

func (e *env) larkURL() string { return e.svc("fakelark", 9000) }

// send fabricates one user message: a new message, or a reply inside a thread when replyTo is set.
func (e *env) send(ctx context.Context, chat, text, replyTo string) (sent, error) {
	var out struct {
		Sent []sent `json:"sent"`
	}
	err := e.do(ctx, "POST", e.larkURL()+"/send", map[string]any{"chat": chat, "text": text, "reply_to": replyTo}, &out)
	if err != nil {
		return sent{}, fmt.Errorf("send: %w", err)
	}
	if len(out.Sent) != 1 {
		return sent{}, fmt.Errorf("send: %d results", len(out.Sent))
	}
	return out.Sent[0], nil
}

func (e *env) cards(ctx context.Context, chat string) ([]card, error) {
	var out struct {
		Cards []card `json:"cards"`
	}
	err := e.do(ctx, "GET", e.larkURL()+"/cards?chat="+chat, nil, &out)
	return out.Cards, err
}

func sessionOf(messageID string) string { return "lark-" + messageID }

// --- a Session's Record, read from any ready bot pod over the shared store ---

type recordEvent struct {
	Kind     string    `json:"kind"`
	Number   int       `json:"number"`
	Time     time.Time `json:"time"`
	Instance string    `json:"instance"`
	Epoch    int       `json:"epoch"`
	Why      string    `json:"why"`
	From     string    `json:"from"`
	Status   string    `json:"status"`
	Source   string    `json:"source"`
	Text     string    `json:"text"`
	CallID   string    `json:"call_id"` // tool_started, tool_ended, tool_result_spilled
	ToolID   string    `json:"tool_id"` // those, tool_loaded and tool_unloaded (the qualified ID)
	Bytes    int       `json:"bytes"`   // tool_result_spilled: the size of the full result
	Stage    string    `json:"stage"`   // extension_failed: such as toolset:crm
	Code     string    `json:"code"`
	RunID    string    `json:"run_id"`
}

type recordPart struct {
	Text     string            `json:"text"`
	Metadata map[string]string `json:"metadata"` // a Segment's tag text carries gotato.tag (segment, tool_calls, recap)
}

// recordMessage is a Transcript message: a user or assistant text, an assistant's tool calls, or a tool result.
type recordMessage struct {
	RunID     string       `json:"run_id"`
	Role      string       `json:"role"`
	Parts     []recordPart `json:"parts"`
	ToolCalls []struct {
		ID     string `json:"id"`
		ToolID string `json:"tool_id"`
	} `json:"tool_calls"`
	ToolResult *struct {
		CallID    string          `json:"call_id"`
		Status    string          `json:"status"`
		SafeError string          `json:"safe_error"`
		Spilled   json.RawMessage `json:"spilled"`
	} `json:"tool_result"`
}

type record struct {
	ID       string          `json:"id"`
	Messages []recordMessage `json:"messages"`
	Runs     []struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	} `json:"runs"`
	Events []recordEvent `json:"events"`
	// Segments are the closed Segments the Record holds; Compactions say what each compaction did; LoadedTools are
	// the catalog tools loaded now.
	Segments    []recordSegment    `json:"segments"`
	Compactions []recordCompaction `json:"compactions"`
	LoadedTools []string           `json:"loaded_tools"`
}

type recordSegment struct {
	Number   int             `json:"number"`
	Recap    string          `json:"recap"`
	Messages []recordMessage `json:"messages"`
}

type recordCompaction struct {
	ReplacedMessages int    `json:"replaced_messages"`
	Recap            string `json:"recap"` // the writer's name: truncate, model, none
	Segment          int    `json:"segment"`
}

func (r record) text(i int) string {
	var b strings.Builder
	for _, p := range r.Messages[i].Parts {
		if p.Metadata["gotato.tag"] == "recap" {
			continue // a Recap quotes the messages it covers; it is not one of them
		}
		b.WriteString(p.Text)
	}
	return b.String()
}

// count is how many user messages carry the tag.
func (r record) count(tag string) int {
	n := 0
	for i, m := range r.Messages {
		if m.Role == "user" && strings.Contains(r.text(i), tag) {
			n++
		}
	}
	return n
}

func (r record) assistantReplies() int {
	n := 0
	for _, m := range r.Messages {
		if m.Role == "assistant" {
			n++
		}
	}
	return n
}

// unanswered says the Transcript ends on a user message: it got in, and no reply followed.
func (r record) unanswered() bool {
	return len(r.Messages) > 0 && r.Messages[len(r.Messages)-1].Role == "user"
}

// inversions counts the adjacent pairs of tags, sent one after the other, that the Transcript holds in the
// other order (a tag the Transcript lacks is skipped).
func (r record) inversions(tags []string) int {
	idx := func(tag string) int {
		for i, m := range r.Messages {
			if m.Role == "user" && strings.Contains(r.text(i), tag) {
				return i
			}
		}
		return -1
	}
	n, prev := 0, -1
	for _, tag := range tags {
		i := idx(tag)
		if i < 0 {
			continue
		}
		if prev >= 0 && i < prev {
			n++
		}
		prev = i
	}
	return n
}

func (e *env) record(ctx context.Context, id string) (record, error) {
	var r record
	err := e.do(ctx, "GET", e.svc("larkbot", 8080)+"/v1/sessions/"+id, nil, &r)
	return r, err
}

// recordFaults checks a Record's own invariants: event numbers are 1..N with no gap, and each claim accounts
// for the one before it (the new holder says it took over from the holder, and the epoch does not go back).
// Each finding is a GOTATO-FAULT.
func recordFaults(r record) []string {
	var out []string
	holder, epoch := "", 0
	for i, ev := range r.Events {
		if ev.Number != i+1 {
			out = append(out, fmt.Sprintf("position gap: event %d (%s) has number %d", i+1, ev.Kind, ev.Number))
			break
		}
		switch ev.Kind {
		case "session_claimed":
			if holder != "" && ev.From != holder && ev.Instance != holder {
				out = append(out, fmt.Sprintf("two claims overlap at %d: %s claims while %s holds it (from=%q)", ev.Number, ev.Instance, holder, ev.From))
			}
			if ev.Epoch < epoch {
				out = append(out, fmt.Sprintf("epoch went back at %d: %d after %d", ev.Number, ev.Epoch, epoch))
			}
			holder, epoch = ev.Instance, ev.Epoch
		case "session_released":
			if ev.Instance != holder {
				out = append(out, fmt.Sprintf("released by %s at %d, held by %q", ev.Instance, ev.Number, holder))
			}
			holder = ""
		}
	}
	return out
}
