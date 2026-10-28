package reftasks_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/reftasks"
)

func TestSupportCaseIsAnAgenticLoop(t *testing.T) {
	s, answers := play(t, reftasks.SupportCaseTask())
	var steps []string
	for _, message := range s.Messages() {
		for _, call := range message.ToolCalls {
			steps = append(steps, call.ToolID)
		}
	}
	want := strings.Join([]string{reftasks.CustomerOrdersID, reftasks.OrderDetailsID, reftasks.PolicySearchID, reftasks.OrderRefundID}, ",")
	if got := strings.Join(steps, ","); got != want {
		t.Fatalf("steps = %s, want %s", got, want)
	}
	if !strings.Contains(answers[0], reftasks.SupportRefundID) {
		t.Fatalf("answer = %q", answers[0])
	}
	if err := reftasks.SupportCaseTask().Check([]string{"I refunded the teapot."}); err == nil {
		t.Fatal("an answer without the refund ID must fail the check")
	}
}

func TestSupportRefundRefusesTheExpiredOrder(t *testing.T) {
	var refund gotato.Tool
	for _, tool := range reftasks.SupportTools() {
		if tool.Spec().ID == reftasks.OrderRefundID {
			refund = tool
		}
	}
	if refund.Spec().Effect != gotato.ToolEffectWriteExternal {
		t.Fatalf("orders.refund effect = %s", refund.Spec().Effect)
	}
	result, err := refund.Execute(context.Background(), gotato.ToolUse{Arguments: []byte(`{"order_id":"A-5208","sku":"CT-200","reason":"arrived damaged"}`)}, nil)
	if err == nil && result.Status == gotato.ToolResultStatusOK {
		t.Fatalf("the teapot of the order delivered 58 days ago was refunded: %+v", result)
	}
}
