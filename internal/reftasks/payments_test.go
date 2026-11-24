package reftasks_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/reftasks"
	"github.com/jinhuang712/gotato/record"
)

// ask plays the payments check's fake model on a Session that holds one user
// Message with held, and returns its answer to each question of the check.
func ask(t *testing.T, held string) map[string]string {
	t.Helper()
	check := reftasks.PaymentsRecapCheck()
	s := record.New()
	for _, message := range []gotato.Message{gotato.UserMessage(held), gotato.AssistantMessage("Noted.")} {
		if err := s.Append(message); err != nil {
			t.Fatal(err)
		}
	}
	session, err := gotato.NewSession(context.Background(), gotato.WithModel(check.Model), gotato.WithTranscript(s))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	answers := map[string]string{}
	for _, want := range []string{"ref_Kq3910", "C-5521", "412", "23:41:07", "12900"} {
		question := questionFor(t, want)
		result, err := session.Prompt(context.Background(), gotato.UserMessage(reftasks.QuestionPrefix+question))
		if err != nil {
			t.Fatalf("%q: %v", question, err)
		}
		answers[want] = gotato.TextOf(*result.FinalMessage)
	}
	return answers
}

// questionFor is the question of the payments check that the fact answers.
func questionFor(t *testing.T, fact string) string {
	t.Helper()
	questions := map[string]string{
		"ref_Kq3910": "What is the processor reference of the failed charge of order O-88347?",
		"C-5521":     "What charge ID did the retry of order O-88502 create?",
		"412":        "How many charges did batch B-20261018-07 hold in all?",
		"23:41:07":   "At what time, to the second, did the charge of order O-88502 time out?",
		"12900":      "What was the amount of order O-88213, in cents?",
	}
	question, ok := questions[fact]
	if !ok {
		t.Fatalf("no question for the fact %q", fact)
	}
	return question
}

// The fake model answers a question only from a Tool Result's own field. The
// text of a fact also turns up by chance where a compaction leaves no Tool
// Result: in the ID of a Message that a Segment refers to, in a time, and
// inside a longer word.
func TestThePaymentsModelDoesNotAnswerFromAFactsDigitsElsewhere(t *testing.T) {
	held := strings.Join([]string{
		"<segment n=\"1\">",
		"ref msg:message-61c3a8e38412",
		"ref msg:message-c-5521a12900",
		"Segment 1, 2026-11-24T23:41:07+08:00 to 2026-11-24T23:41:07+08:00",
		"ref_Kq39100 was not the reference",
		"</segment>",
	}, "\n")
	for fact, answer := range ask(t, held) {
		if !strings.Contains(answer, "unknown") {
			t.Errorf("the fact %q is in no Tool Result, and the model answered %q", fact, answer)
		}
	}
}

// The fake model answers from the fields the Tool Results of the check hold,
// which is also what its faithful Recap keeps of them.
func TestThePaymentsModelAnswersFromAToolResultsField(t *testing.T) {
	held := strings.Join([]string{
		`- {"batch_id":"B-20261018-07","charges":412,"succeeded":409,"failed":["O-88213","O-88347","O-88502"]}`,
		`- {"order":"O-88347","code":"insufficient_funds","at":"2026-10-18T23:30:02+08:00","processor_ref":"ref_Kq3910"}`,
		`- {"order":"O-88502","code":"gateway_timeout","at":"2026-10-18T23:41:07+08:00","processor_ref":"ref_Hv7755"}`,
		`- [{"order":"O-88213","amount_cents":12900}]`,
		`- {"order":"O-88502","status":"succeeded","charge_id":"C-5521"}`,
	}, "\n")
	for fact, answer := range ask(t, held) {
		if !strings.Contains(answer, fact) {
			t.Errorf("the fact %q is in a Tool Result's field, and the model answered %q", fact, answer)
		}
	}
}
