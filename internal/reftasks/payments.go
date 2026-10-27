package reftasks

import (
	"context"
	"fmt"
	"strings"

	"github.com/jinhuang712/gotato/testkit"

	"github.com/jinhuang712/gotato/gotato"
)

// RecapCheck is the payments example of GOALS.md G-E01: an operations
// assistant that reads payments, orders and logs works through a conversation,
// and is then asked questions whose answers appeared only in Tool Results.
// A team runs it on demand against its own gateway (internal/workload
// RunRecapCheck, or gotato-workload -recap-check -live); with Model set it
// plays on a fake model, which is how its scoring is tested.
type RecapCheck struct {
	Name        string
	Instruction string
	// Tools are the Agent's Tools. They keep no state.
	Tools []gotato.Tool
	// Prompts are the conversation, one Run each, on one Session. The last
	// is small talk with no Tool call, so the compaction's default tail (the
	// latest Run) holds none of the facts.
	Prompts []string
	// Questions are asked after the conversation, before and after the
	// compaction, each in a Run that is not saved.
	Questions []recapQuestion
	// Model plays the check. Nil in a live run, which brings its own.
	Model gotato.Model
}

// recapQuestion is one question and the fact that answers it.
type recapQuestion struct {
	// Ask is the question, as the user types it.
	Ask string
	// Want is the text a correct answer contains, compared without case.
	// The fact appears in a Tool Result, and in no user Message.
	Want string
}

// paymentsInstruction is the Agent's instruction in the payments check.
const paymentsInstruction = "You are an operations assistant for payments. Use the tools to find out what happened. " +
	"Answer in one or two short sentences, and do not repeat the details a tool returned unless you are asked for them."

// QuestionPrefix starts every question the check asks, so a fake model tells a
// question from the conversation. It also tells the model to answer from the
// conversation, since a tool could be asked again.
const QuestionPrefix = "Question (answer from this conversation only; the tools are switched off; say \"unknown\" if you cannot tell): "

// PaymentsRecapCheck returns the payments check, with the fake model that
// writes a faithful Recap. PaymentsRecapCheckWith picks the Recap it writes.
func PaymentsRecapCheck() RecapCheck { return PaymentsRecapCheckWith(true) }

// PaymentsRecapCheckWith returns the payments check with a fake model whose
// Recap keeps the facts (faithful) or drops them.
func PaymentsRecapCheckWith(faithful bool) RecapCheck {
	return RecapCheck{
		Name:        "payments",
		Instruction: paymentsInstruction,
		Tools:       paymentsTools(),
		Prompts: []string{
			"Some charges in last night's batch B-20261018-07 failed. Why? Look at the batch, the logs of each failed order, and the orders.",
			"Retry O-88502 with payments.retry, and leave the other two.",
			"Thanks, that is all for now.",
		},
		Questions: []recapQuestion{
			{Ask: "What is the processor reference of the failed charge of order O-88347?", Want: "ref_Kq3910"},
			{Ask: "What charge ID did the retry of order O-88502 create?", Want: "C-5521"},
			{Ask: "How many charges did batch B-20261018-07 hold in all?", Want: "412"},
			{Ask: "At what time, to the second, did the charge of order O-88502 time out?", Want: "23:41:07"},
			{Ask: "What was the amount of order O-88213, in cents?", Want: "12900"},
		},
		Model: paymentsModel{faithful: faithful},
	}
}

// failedCharges are the three charges of the batch that failed.
var failedCharges = map[string]string{
	"O-88213": `{"order":"O-88213","code":"card_expired","at":"2026-10-18T23:12:40+08:00","processor_ref":"ref_Zt0042"}`,
	"O-88347": `{"order":"O-88347","code":"insufficient_funds","at":"2026-10-18T23:30:02+08:00","processor_ref":"ref_Kq3910"}`,
	"O-88502": `{"order":"O-88502","code":"gateway_timeout","at":"2026-10-18T23:41:07+08:00","processor_ref":"ref_Hv7755"}`,
}

func paymentsTools() []gotato.Tool {
	batch, err := gotato.NewFuncTool("payments.get_batch", "Reads a payment batch.", func(_ context.Context, in struct {
		BatchID string `json:"batch_id" description:"the batch ID"`
	}) (string, error) {
		return fmt.Sprintf(`{"batch_id":%q,"charges":412,"succeeded":409,"failed":["O-88213","O-88347","O-88502"]}`, in.BatchID), nil
	})
	must(err)
	logs, err := gotato.NewFuncTool("logs.query", "Reads the payment gateway's log lines of an order.", func(_ context.Context, in struct {
		Service string `json:"service,omitempty" description:"the service whose logs to read"`
		Order   string `json:"order" description:"the order ID"`
	}) (string, error) {
		if line, ok := failedCharges[in.Order]; ok {
			return line, nil
		}
		return "", fmt.Errorf("no log lines for order %s", in.Order)
	})
	must(err)
	orders, err := gotato.NewFuncTool("orders.search", "Finds orders by ID.", func(_ context.Context, in struct {
		IDs []string `json:"ids" description:"order IDs"`
	}) (string, error) {
		amounts := map[string]int{"O-88213": 12900, "O-88347": 4550, "O-88502": 30075}
		var parts []string
		for _, id := range in.IDs {
			if cents, ok := amounts[id]; ok {
				parts = append(parts, fmt.Sprintf(`{"order":%q,"amount_cents":%d}`, id, cents))
			}
		}
		return "[" + strings.Join(parts, ",") + "]", nil
	})
	must(err)
	retry, err := gotato.NewFuncTool("payments.retry", "Retries the failed charge of an order.", func(_ context.Context, in struct {
		Order string `json:"order" description:"the order ID"`
	}) (string, error) {
		return fmt.Sprintf(`{"order":%q,"status":"succeeded","charge_id":"C-5521"}`, in.Order), nil
	})
	must(err)
	return []gotato.Tool{
		gotato.ToolWithEffect(batch, gotato.ToolEffectRead), gotato.ToolWithEffect(logs, gotato.ToolEffectRead),
		gotato.ToolWithEffect(orders, gotato.ToolEffectRead), gotato.ToolWithEffect(retry, gotato.ToolEffectWriteExternal),
	}
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

// paymentsModel plays the payments check without a provider: it calls the
// tools the prompts ask for, answers a question from whatever the request still
// holds, and writes a Recap that keeps the Tool Results (faithful) or does not.
type paymentsModel struct{ faithful bool }

// Stream implements gotato.Model.
func (m paymentsModel) Stream(_ context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	messages := request.Messages
	last := len(messages) - 1
	if last < 0 {
		return playScript(testkit.Text("nothing to do")), nil
	}
	ask := gotato.TextOf(messages[last])
	switch {
	case messages[last].Role == gotato.RoleUser && strings.HasPrefix(ask, "Write a recap"):
		return playScript(testkit.Text(m.recap(messages))), nil
	case messages[last].Role == gotato.RoleUser && strings.HasPrefix(ask, QuestionPrefix):
		return playScript(testkit.Text(answerFrom(strings.TrimPrefix(ask, QuestionPrefix), requestText(messages)))), nil
	case messages[last].Role == gotato.RoleToolResult:
		return playScript(testkit.Text("Done.")), nil
	case strings.Contains(ask, "Why?"):
		return playScript(testkit.ToolCalls(
			gotato.ToolCall{ID: "c1", ToolID: "payments.get_batch", Arguments: []byte(`{"batch_id":"B-20261018-07"}`)},
			gotato.ToolCall{ID: "c2", ToolID: "logs.query", Arguments: []byte(`{"order":"O-88213"}`)},
			gotato.ToolCall{ID: "c3", ToolID: "logs.query", Arguments: []byte(`{"order":"O-88347"}`)},
			gotato.ToolCall{ID: "c4", ToolID: "logs.query", Arguments: []byte(`{"order":"O-88502"}`)},
			gotato.ToolCall{ID: "c5", ToolID: "orders.search", Arguments: []byte(`{"ids":["O-88213","O-88347","O-88502"]}`)},
		)), nil
	case strings.Contains(ask, "Retry"):
		return playScript(testkit.ToolCalls(gotato.ToolCall{ID: "c6", ToolID: "payments.retry", Arguments: []byte(`{"order":"O-88502"}`)})), nil
	}
	return playScript(testkit.Text("You are welcome.")), nil
}

// recap writes the Recap, from the Tool Results in the request when faithful.
func (m paymentsModel) recap(messages []gotato.Message) string {
	var b strings.Builder
	b.WriteString("## What happened\n- Found why 3 charges of batch B-20261018-07 failed; retried O-88502 at the user's request.\n")
	if m.faithful {
		b.WriteString("## Key facts\n")
		for _, result := range testkit.ToolResults(gotato.ModelRequest{Messages: messages}) {
			b.WriteString("- ")
			b.WriteString(result)
			b.WriteString("\n")
		}
	}
	b.WriteString("## Open items\n- O-88213 and O-88347 were left alone.\n")
	return b.String()
}

// requestText is every text a request holds: Messages and Tool Results.
func requestText(messages []gotato.Message) string {
	var b strings.Builder
	for _, message := range messages {
		b.WriteString(gotato.TextOf(message))
		b.WriteString("\n")
		if message.ToolResult != nil {
			for _, part := range message.ToolResult.Content {
				b.WriteString(part.Text)
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

// paymentsFacts maps a word of a question to the fact that answers it, and the
// line of the log or tool result where a request holds it.
var paymentsFacts = []struct{ keyword, fact string }{
	{"processor reference", "ref_Kq3910"},
	{"charge ID", "C-5521"},
	{"How many charges", "412"},
	{"time out", "23:41:07"},
	{"amount of order", "12900"},
}

// answerFrom answers a question with its fact when the request still holds it.
func answerFrom(question, held string) string {
	for _, f := range paymentsFacts {
		if strings.Contains(question, f.keyword) {
			if strings.Contains(held, f.fact) {
				return "It is " + f.fact + "."
			}
			return "unknown"
		}
	}
	return "unknown"
}
