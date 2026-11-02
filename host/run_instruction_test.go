package host_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/testkit"
)

// An Agent's instruction comes from its definition alone. A request that sets
// {"gotato.instruction": ...} is refused before its Run starts, whether it
// creates a Session or updates one, saved or not; another instruction is another
// Agent, chosen by RunRequest.Agent.
func TestAnInstructionInMetadataIsRefusedAndAnotherAgentIsTheWay(t *testing.T) {
	model := testkit.NewFakeModel(testkit.Text("1"), testkit.Text("2"), testkit.Text("3"))
	runner, store := newRunner(t,
		profile.AgentProfile{Name: "bot", Model: model, Instruction: "agent instruction"},
		profile.AgentProfile{Name: "french", Model: model, Instruction: "answer in French"},
	)
	ctx := context.Background()
	id := plant(t, store, record.WithMetadata(map[string]string{metaAgentKey: "bot"}))
	before := storedMetadata(t, store, id)

	for _, request := range []host.RunRequest{
		{SessionID: id, Prompt: "hi", Update: map[string]string{metaInstructionKey: "answer in French"}},
		{SessionID: id, Prompt: "hi", Update: map[string]string{metaInstructionKey: "answer in French"}, SkipSave: true},
		{SessionID: id, Prompt: "hi", Update: map[string]string{metaInstructionKey: ""}},
		{Prompt: "hi", Metadata: map[string]string{metaInstructionKey: "answer in French"}},
	} {
		if _, err := runner.Run(ctx, request); !errors.Is(err, host.ErrReservedKey) || !gotato.NotRun(err) {
			t.Fatalf("Run(%+v) = %v, want ErrReservedKey, not run", request, err)
		}
	}
	if got := model.Requests(); len(got) != 0 {
		t.Fatalf("a refused request reached the Model %d times", len(got))
	}
	if after := storedMetadata(t, store, id); !equalMetadata(before, after) {
		t.Fatalf("a refused request changed the Session's metadata: %v -> %v", before, after)
	}
	if list, err := listAll(ctx, store); err != nil || len(list) != 1 {
		t.Fatalf("a refused request left %d Sessions (err=%v), want only the planted one", len(list), err)
	}

	// Another instruction is another Agent.
	if _, err := runner.Run(ctx, host.RunRequest{SessionID: id, Agent: "french", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	request, _ := model.LastRequest()
	if request.SystemInstructions != "answer in French" {
		t.Fatalf("instruction = %q, want the Agent's", request.SystemInstructions)
	}
}
