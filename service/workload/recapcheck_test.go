package workload_test

import (
	"context"
	"strings"
	"testing"

	gotato "github.com/jinhuang712/gotato/gotato-core"
	"github.com/jinhuang712/gotato/service/workload"
	"github.com/jinhuang712/gotato/testkit"
)

func TestTheRecapCheckScoresAFaithfulRecapAtOneHundredPercent(t *testing.T) {
	report, err := workload.RunRecapCheck(context.Background(), workload.RecapCheckConfig{}, testkit.PaymentsRecapCheck())
	if err != nil {
		t.Fatal(err)
	}
	if report.Questions != 5 || report.Before.Correct != 5 || report.After.Correct != 5 || report.After.Share != 1 || report.Before.Share != 1 {
		t.Fatalf("report = %+v", report)
	}
	if !report.Replaced || !report.RecapWritten || !strings.HasPrefix(report.Recap, "## What happened") || !strings.Contains(report.Recap, "C-5521") {
		t.Fatalf("compaction: %+v", report)
	}
	if report.Model != "fake" || report.TokensAfter >= report.TokensBefore {
		t.Errorf("model = %q, tokens %d -> %d", report.Model, report.TokensBefore, report.TokensAfter)
	}
}

func TestTheRecapCheckSeesAFactTheRecapLeftOut(t *testing.T) {
	report, err := workload.RunRecapCheck(context.Background(), workload.RecapCheckConfig{}, testkit.PaymentsRecapCheckWith(false))
	if err != nil {
		t.Fatal(err)
	}
	if report.Before.Correct != 5 || report.After.Correct != 0 || !report.RecapWritten {
		t.Fatalf("report = %+v", report)
	}
	for _, a := range report.Answers {
		if a.AfterCorrect || !strings.Contains(a.After, "unknown") {
			t.Errorf("answer after = %+v", a)
		}
	}
}

// badRecapModel plays the check, but answers the Recap's ask in free text.
type badRecapModel struct{ gotato.Model }

func (m badRecapModel) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	if n := len(request.Messages); n > 0 && strings.HasPrefix(gotato.TextOf(request.Messages[n-1]), "Write a recap") {
		return testkit.NewFakeModel(testkit.Text("Sure! The user had a problem with payments.")).Stream(ctx, request)
	}
	return m.Model.Stream(ctx, request)
}

func TestTheRecapCheckMeasuresNothingWhenTheModelWritesNoRecap(t *testing.T) {
	check := testkit.PaymentsRecapCheck()
	check.Model = badRecapModel{check.Model}
	report, err := workload.RunRecapCheck(context.Background(), workload.RecapCheckConfig{}, check)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Replaced || report.RecapWritten || report.Recap != "" || report.After.Correct != 0 {
		t.Fatalf("report = %+v", report)
	}
}

func TestTheRecapChecksQuestionsAreAnsweredOnlyFromToolResults(t *testing.T) {
	check := testkit.PaymentsRecapCheck()
	for _, prompt := range check.Prompts {
		for _, q := range check.Questions {
			if strings.Contains(prompt, q.Want) {
				t.Errorf("the prompt %q holds the fact %q, so a question about it could be answered without a Tool Result", prompt, q.Want)
			}
		}
	}
}
