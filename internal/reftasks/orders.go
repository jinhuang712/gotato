package reftasks

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jinhuang712/gotato/gotato"
)

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
					return nil, publicError(fmt.Sprintf("order %s not found", in.ID))
				}
			}
			return map[string]string{"id": in.ID, "status": status}, nil
		})
	if err != nil {
		panic(err)
	}
	return gotato.ToolWithEffect(tool, gotato.ToolEffectRead)
}

// historyOrder reports whether id is one of the orders a prepared history
// asks about: 1000 and up.
func historyOrder(id string) bool {
	n, err := strconv.Atoi(id)
	return err == nil && n >= 1000
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
			answer.StopReason = gotato.StopReasonEndTurn
			messages = append(messages, question, answer)
			continue
		}
		callID := gotato.ToolCallID("history-call-" + id)
		call := gotato.Message{Role: gotato.RoleAssistant, StopReason: gotato.StopReasonToolCalls, ToolCalls: []gotato.ToolCall{
			{ID: callID, ToolID: ordersLookupID, Arguments: []byte(`{"id":"` + id + `"}`)},
		}}
		content := []gotato.ContentPart{{Kind: gotato.ContentKindJSON, Text: `{"id":"` + id + `","status":"delivered"}`}}
		result := gotato.Message{Role: gotato.RoleToolResult, Parts: content, ToolResult: &gotato.ToolResult{
			CallID: callID, Status: gotato.ToolResultStatusOK, Content: content, Executed: true,
		}}
		answer := gotato.AssistantMessage("Order " + id + " was delivered. Let me know if anything is missing from the package.")
		answer.StopReason = gotato.StopReasonEndTurn
		messages = append(messages, question, call, result, answer)
	}
	for i := range messages {
		messages[i].ID = gotato.MessageID("history-" + strconv.Itoa(i+1))
	}
	return messages
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
