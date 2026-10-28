package reftasks

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/jinhuang712/gotato/testkit"

	"github.com/jinhuang712/gotato/gotato"
)

// ordersModel plays every reference task from the request alone. After a
// user prompt it looks up the first order the prompt names, or answers. After
// a tool result it answers from it, retries a call Gotato refused, or moves
// to the next order the prompt names when the lookup failed. With wrongFirstCall, its first call after a prompt has an
// argument the schema refuses.
type ordersModel struct {
	wrongFirstCall bool
}

var orderNumber = regexp.MustCompile(`(?i)\border (\d+)`)

// Stream implements gotato.Model.
func (m ordersModel) Stream(_ context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	return playScript(m.play(request)), nil
}

func (m ordersModel) play(request gotato.ModelRequest) testkit.Script {
	prompt, results := lastPrompt(request.Messages)
	ids := orderNumbers(prompt)
	// A call ID is unique in the Session: it counts the Messages before it.
	n := len(request.Messages) + 1
	if len(results) == 0 {
		switch {
		case len(ids) > 0 && m.wrongFirstCall:
			return lookupCall(n, `{"order":"`+ids[0]+`"}`)
		case len(ids) > 0:
			return lookupCall(n, `{"id":"`+ids[0]+`"}`)
		case strings.Contains(strings.ToLower(prompt), "which order numbers"):
			return testkit.Text("You asked about orders " + strings.Join(askedOrders(request.Messages), ", ") + ".")
		default:
			return testkit.Text("Noted.")
		}
	}
	last := results[len(results)-1]
	if last.Status == gotato.ToolResultStatusOK {
		var found map[string]string
		_ = json.Unmarshal([]byte(resultText(last)), &found)
		return testkit.Text("Order " + found["id"] + " is " + found["status"] + ".")
	}
	// A refused call is retried with the right argument; a failed lookup
	// moves to the next order the prompt names, if any.
	if refused(last) && len(ids) > 0 {
		return lookupCall(n, `{"id":"`+ids[0]+`"}`)
	}
	tried := 0
	for _, result := range results {
		if !refused(result) {
			tried++
		}
	}
	if tried < len(ids) {
		return lookupCall(n, `{"id":"`+ids[tried]+`"}`)
	}
	return testkit.Text("I could not find that order: " + last.SafeError)
}

func orderNumbers(text string) []string {
	var ids []string
	for _, match := range orderNumber.FindAllStringSubmatch(text, -1) {
		ids = append(ids, match[1])
	}
	return ids
}

// askedOrders lists, once each, the orders the user's prompts named, leaving
// out a prepared history's orders.
func askedOrders(messages []gotato.Message) []string {
	var ids []string
	seen := map[string]bool{}
	for _, message := range messages {
		if message.Role != gotato.RoleUser {
			continue
		}
		for _, id := range orderNumbers(promptText(message)) {
			if !seen[id] && !historyOrder(id) {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// refused reports whether Gotato refused a call before the tool ran. The
// model sees only what a provider receives, which leaves out
// ToolResult.Executed, so it reads the error code the refusal starts with.
func refused(result gotato.ToolResult) bool {
	return strings.HasPrefix(result.SafeError, string(gotato.CodeToolArgumentFailure)) ||
		strings.HasPrefix(result.SafeError, string(gotato.CodeToolResolutionFailure))
}

func lookupCall(n int, arguments string) testkit.Script {
	return testkit.ToolCalls(gotato.ToolCall{ID: gotato.ToolCallID("call-" + strconv.Itoa(n)), ToolID: ordersLookupID, Arguments: []byte(arguments)})
}
