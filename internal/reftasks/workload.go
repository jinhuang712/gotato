package reftasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jinhuang712/gotato/testkit"

	"github.com/jinhuang712/gotato/gotato"
)

// Task is one task of the reference workload that GOALS.md measures: the
// history a Session starts with, the prompts sent to it, the Tools it offers,
// and a fake Model that plays it the same way every time. Each prompt is one
// Run on the same Session. Package internal/workload drives the tasks through
// a Runner, with the fake Model in CI or a real one in the live run.
type Task struct {
	// Name identifies the task in reports.
	Name string
	// Instruction is the Agent's instruction.
	Instruction string
	// History is committed to the Session before the first prompt.
	History []gotato.Message
	// Prompts are sent in order, each as one Run on the same Session.
	Prompts []string
	// Tools are the Tools the Agent offers. They keep no state.
	Tools []gotato.Tool
	// Options are added to the Agent after its Tools, such as the ToolSets of
	// the tool_search task. They keep no state.
	Options []gotato.Option
	// Model plays the task. It keeps no state and no copy of the requests,
	// so one value serves concurrent plays.
	Model gotato.Model
	// Limits replaces gotato.DefaultLimits when it is not nil.
	Limits *gotato.CoreLimits
	// Window is the window, in tokens, of the task's fake Model, 0 for a
	// task that is not about the window. The driver gives the Model that
	// window, so the Runner takes its default ceiling from it, counts every
	// request against it, and refuses one over it as the provider would
	// (workload.WindowReport).
	Window int
	// Recap turns the Agent's model Recap on for the task (G-B16).
	Recap bool
	// Check reports whether a play reached the task's goal, from the final
	// text of each Run in order. It holds for any Model that does the task,
	// so the live run uses it too.
	Check func(answers []string) error
}

// workloadInstruction is the instruction every reference task uses.
const workloadInstruction = "You answer questions about orders. Use orders.lookup to find an order's status. " +
	"If a lookup fails, say so, or follow the user's fallback. Answer in one short sentence."

// ordersLookupID is the Tool every reference task offers.
const ordersLookupID = "orders.lookup"

// orders are the orders OrdersLookup knows. Order 7 does not exist.
var orders = map[string]string{"42": "shipped", "17": "delivered", "8": "processing"}

// OrdersLookup is the reference workload's Tool: it finds an order by its ID
// and fails for an order it does not know, so a task can recover from a tool
// error. Its Effect is read.
func OrdersLookup() gotato.Tool {
	tool, err := gotato.NewFuncTool(ordersLookupID, "Finds an order by its ID and returns its status.",
		func(_ context.Context, in struct {
			ID string `json:"id" description:"the order ID, such as 42"`
		}) (map[string]string, error) {
			status, ok := orders[in.ID]
			if !ok {
				if historyOrder(in.ID) {
					status = "delivered"
				} else {
					return nil, fmt.Errorf("order %s not found", in.ID)
				}
			}
			return map[string]string{"id": in.ID, "status": status}, nil
		})
	if err != nil {
		panic(err)
	}
	return gotato.ToolWithEffect(tool, gotato.EffectRead)
}

// historyOrder reports whether id is one of the orders a prepared history
// asks about: 1000 and up.
func historyOrder(id string) bool {
	n, err := strconv.Atoi(id)
	return err == nil && n >= 1000
}

// ReferenceTasks returns the reference workload: a short lookup, a 10-Turn
// conversation, a tool error and its recovery, a support case the model works
// through in several Turns, a Session whose Tools load and unload, and a lookup in Sessions that already hold 10,
// 100 and 1,000 messages.
func ReferenceTasks() []Task {
	tasks := []Task{
		LookupTask(),
		ConversationTask(),
		ToolErrorTask(),
		SupportCaseTask(),
		ToolSearchTask(),
	}
	for _, size := range []int{10, 100, 1000} {
		tasks = append(tasks, HistoryTask(size))
	}
	return tasks
}

// LookupTask asks for one order: the model looks it up and answers.
func LookupTask() Task {
	return Task{
		Name:        "lookup",
		Instruction: workloadInstruction,
		Prompts:     []string{"Where is order 42?"},
		Tools:       []gotato.Tool{OrdersLookup()},
		Model:       ordersModel{},
		Check:       answerMentions("shipped"),
	}
}

// ConversationTask is ten prompts on one Session, with three lookups, and a
// last question the model can answer only from the history.
func ConversationTask() Task {
	return Task{
		Name:        "conversation",
		Instruction: workloadInstruction,
		Prompts: []string{
			"Hi, I have a few questions about my orders.",
			"Where is order 42?",
			"Thanks. And order 17?",
			"Is that one already with me?",
			"Good. Please also check order 8.",
			"When do you think it will ship?",
			"I see. Can you keep that in mind?",
			"One more thing: how do I return an order?",
			"Thank you, that helps.",
			"Before I go: which order numbers did I ask about?",
		},
		Tools: []gotato.Tool{OrdersLookup()},
		Model: ordersModel{},
		Check: func(answers []string) error {
			if len(answers) != 10 {
				return fmt.Errorf("got %d answers, want 10", len(answers))
			}
			last := answers[len(answers)-1]
			for _, id := range []string{"42", "17", "8"} {
				if !containsWord(last, id) {
					return fmt.Errorf("the last answer does not name order %s: %q", id, last)
				}
			}
			return nil
		},
	}
}

// ToolErrorTask asks for an order that does not exist, with a fallback. The
// fake model also gets its first call wrong, so the task covers both kinds of
// failed tool call: arguments the schema check refuses, and a tool that
// fails. A play passes when the model recovers and answers from the fallback.
func ToolErrorTask() Task {
	return Task{
		Name:        "tool_error",
		Instruction: workloadInstruction,
		Prompts:     []string{"Look up order 7. If it does not exist, look up order 42 instead and tell me its status."},
		Tools:       []gotato.Tool{OrdersLookup()},
		Model:       ordersModel{wrongFirstCall: true},
		Check:       answerMentions("shipped"),
	}
}

// HistoryTask is a lookup in a Session that already holds size messages, for
// the cold-load and per-Turn measurements at that size. Its MaxMessages
// leaves the default room on top of the history, so a larger size never meets
// the default limit.
func HistoryTask(size int) Task {
	limits := gotato.DefaultLimits()
	limits.MaxMessages += uint32(size)
	return Task{
		Name:        "history_" + strconv.Itoa(size),
		Instruction: workloadInstruction,
		History:     OrderHistory(size),
		Prompts:     []string{"Where is order 42?"},
		Tools:       []gotato.Tool{OrdersLookup()},
		Model:       ordersModel{},
		Limits:      &limits,
		Check:       answerMentions("shipped"),
	}
}

// OrderHistory returns size committed Messages of earlier lookups: a
// question, the tool call, its result and the answer, repeated, ending with
// an answer. An odd size is rounded down.
func OrderHistory(size int) []gotato.Message {
	messages := make([]gotato.Message, 0, size)
	for n := 0; len(messages)+2 <= size; n++ {
		id := strconv.Itoa(1000 + n)
		question := gotato.UserMessage("Where is order " + id + "? I ordered it a while ago and would like an update.")
		if len(messages)+4 > size {
			answer := gotato.AssistantMessage("I can look up order " + id + " for you as soon as you confirm the number.")
			answer.StopReason = gotato.StopEndTurn
			messages = append(messages, question, answer)
			continue
		}
		callID := gotato.ToolCallID("history-call-" + id)
		call := gotato.Message{Role: gotato.RoleAssistant, StopReason: gotato.StopToolCalls, ToolCalls: []gotato.ToolCall{
			{ID: callID, ToolID: ordersLookupID, Arguments: []byte(`{"id":"` + id + `"}`)},
		}}
		content := []gotato.ContentPart{{Kind: gotato.ContentJSON, Text: `{"id":"` + id + `","status":"delivered"}`}}
		result := gotato.Message{Role: gotato.RoleToolResult, Parts: content, ToolResult: &gotato.ToolResult{
			CallID: callID, Status: gotato.ToolResultOK, Content: content, Executed: true,
		}}
		answer := gotato.AssistantMessage("Order " + id + " was delivered. Let me know if anything is missing from the package.")
		answer.StopReason = gotato.StopEndTurn
		messages = append(messages, question, call, result, answer)
	}
	for i := range messages {
		messages[i].ID = gotato.MessageID("history-" + strconv.Itoa(i+1))
	}
	return messages
}

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
	return &stream{events: m.play(request)}, nil
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
	if last.Status == gotato.ToolResultOK {
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

// lastPrompt returns the text of the last user Message and the tool results
// that follow it.
func lastPrompt(messages []gotato.Message) (string, []gotato.ToolResult) {
	var results []gotato.ToolResult
	for i := len(messages) - 1; i >= 0; i-- {
		switch messages[i].Role {
		case gotato.RoleToolResult:
			if messages[i].ToolResult != nil {
				results = append([]gotato.ToolResult{*messages[i].ToolResult}, results...)
			}
		case gotato.RoleUser:
			return promptText(messages[i]), results
		}
	}
	return "", results
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
	return strings.HasPrefix(result.SafeError, string(gotato.ErrToolArgumentFailure)) ||
		strings.HasPrefix(result.SafeError, string(gotato.ErrToolResolutionFailure))
}

func lookupCall(n int, arguments string) testkit.Script {
	return testkit.ToolCalls(gotato.ToolCall{ID: gotato.ToolCallID("call-" + strconv.Itoa(n)), ToolID: ordersLookupID, Arguments: []byte(arguments)})
}

func resultText(result gotato.ToolResult) string {
	var b strings.Builder
	for _, part := range result.Content {
		b.WriteString(part.Text)
	}
	return b.String()
}

func answerMentions(word string) func([]string) error {
	return func(answers []string) error {
		if len(answers) == 0 {
			return errors.New("no answer")
		}
		if last := answers[len(answers)-1]; !strings.Contains(strings.ToLower(last), word) {
			return fmt.Errorf("the answer does not say %q: %q", word, last)
		}
		return nil
	}
}

func containsWord(text, word string) bool {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(word) + `\b`).MatchString(text)
}
