package testkit_test

import (
	"context"
	"strings"
	"testing"

	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// play runs a task on a plain live Session and returns its Record and each Run's
// final text.
func play(t *testing.T, task testkit.Task) (*session.Record, []string) {
	t.Helper()
	s := session.New()
	for _, message := range task.History {
		if err := s.Append(message); err != nil {
			t.Fatal(err)
		}
	}
	options := []gotato.Option{
		gotato.WithModel(task.Model),
		gotato.WithInstruction(task.Instruction),
		gotato.WithTranscript(s),
	}
	for _, tool := range task.Tools {
		options = append(options, gotato.WithTool(tool))
	}
	options = append(options, task.Options...)
	if task.Limits != nil {
		options = append(options, gotato.WithLimits(*task.Limits))
	}
	agent, err := gotato.NewSession(options...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = agent.Close(context.Background()) }()
	var answers []string
	for _, prompt := range task.Prompts {
		result, err := agent.Prompt(context.Background(), gotato.UserMessage(prompt))
		if err != nil {
			t.Fatalf("%s: %q: %v", task.Name, prompt, err)
		}
		answers = append(answers, gotato.TextOf(*result.FinalMessage))
	}
	return s, answers
}

func TestReferenceTasksPassTheirChecks(t *testing.T) {
	tasks := testkit.ReferenceTasks()
	names := make([]string, 0, len(tasks))
	for _, task := range tasks {
		names = append(names, task.Name)
		_, answers := play(t, task)
		if err := task.Check(answers); err != nil {
			t.Errorf("%s: %v (answers %q)", task.Name, err, answers)
		}
	}
	if got := strings.Join(names, ","); got != "lookup,conversation,tool_error,support_case,tool_search,history_10,history_100,history_1000" {
		t.Fatalf("tasks = %s", got)
	}
}

func TestSupportCaseIsAnAgenticLoop(t *testing.T) {
	s, answers := play(t, testkit.SupportCaseTask())
	var steps []string
	for _, message := range s.Messages() {
		for _, call := range message.ToolCalls {
			steps = append(steps, call.ToolID)
		}
	}
	want := strings.Join([]string{testkit.CustomerOrdersID, testkit.OrderDetailsID, testkit.PolicySearchID, testkit.OrderRefundID}, ",")
	if got := strings.Join(steps, ","); got != want {
		t.Fatalf("steps = %s, want %s", got, want)
	}
	if !strings.Contains(answers[0], testkit.SupportRefundID) {
		t.Fatalf("answer = %q", answers[0])
	}
	if err := testkit.SupportCaseTask().Check([]string{"I refunded the teapot."}); err == nil {
		t.Fatal("an answer without the refund ID must fail the check")
	}
}

func TestSupportRefundRefusesTheExpiredOrder(t *testing.T) {
	var refund gotato.Tool
	for _, tool := range testkit.SupportTools() {
		if tool.Spec().ID == testkit.OrderRefundID {
			refund = tool
		}
	}
	if refund.Spec().Effect != gotato.EffectWriteExternal {
		t.Fatalf("orders.refund effect = %s", refund.Spec().Effect)
	}
	result, err := refund.Execute(context.Background(), gotato.ToolUse{ArgumentsJSON: []byte(`{"order_id":"A-5208","sku":"CT-200","reason":"arrived damaged"}`)}, nil)
	if err == nil && result.Status == gotato.ToolResultOK {
		t.Fatalf("the teapot of the order delivered 58 days ago was refunded: %+v", result)
	}
}

func TestToolErrorTaskRecoversFromBothKindsOfFailure(t *testing.T) {
	s, answers := play(t, testkit.ToolErrorTask())
	var statuses []string
	for _, message := range s.Messages() {
		if result := message.ToolResult; result != nil {
			executed := "refused"
			if result.Executed {
				executed = "ran"
			}
			statuses = append(statuses, string(result.Status)+"/"+executed)
		}
	}
	if got := strings.Join(statuses, ","); got != "failed/refused,failed/ran,ok/ran" {
		t.Fatalf("tool results = %s", got)
	}
	if answers[0] != "Order 42 is shipped." {
		t.Fatalf("answer = %q", answers[0])
	}
}

func TestOrderHistoryHasTheRequestedSize(t *testing.T) {
	for _, size := range []int{2, 10, 100, 1000} {
		history := testkit.OrderHistory(size)
		if len(history) != size {
			t.Fatalf("OrderHistory(%d) has %d messages", size, len(history))
		}
		if last := history[len(history)-1]; last.Role != gotato.RoleAssistant || len(last.ToolCalls) != 0 {
			t.Fatalf("OrderHistory(%d) ends with %+v, want an answer", size, last)
		}
	}
}

func TestConversationCheckFailsOnAWrongAnswer(t *testing.T) {
	task := testkit.ConversationTask()
	answers := make([]string, 10)
	answers[9] = "You asked about orders 42 and 17."
	if err := task.Check(answers); err == nil {
		t.Fatal("a last answer without order 8 must fail the check")
	}
}
