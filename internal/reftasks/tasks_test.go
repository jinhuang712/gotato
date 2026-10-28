package reftasks_test

import (
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/internal/reftasks"
)

func TestReferenceTasksPassTheirChecks(t *testing.T) {
	tasks := reftasks.ReferenceTasks()
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
