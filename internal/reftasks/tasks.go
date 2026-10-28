package reftasks

import (
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
	Options []gotato.SessionOption
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
