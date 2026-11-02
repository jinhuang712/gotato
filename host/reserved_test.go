package host_test

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/host"
	"github.com/jinhuang712/gotato/profile"
	"github.com/jinhuang712/gotato/store"
	"github.com/jinhuang712/gotato/testkit"
)

// storedMetadata is the metadata of a stored Session.
func storedMetadata(t *testing.T, st store.Store, id string) map[string]string {
	t.Helper()
	rec, err := store.View(context.Background(), st.Records, id)
	if err != nil {
		t.Fatal(err)
	}
	return rec.Metadata()
}

func equalMetadata(a, b map[string]string) bool { return maps.Equal(a, b) }

// every key the Runner owns, and one it may own later.
var reservedKeys = []string{
	metaAgentKey,
	metaInstructionKey,
	metaCeilingKey,
	metaCacheKeyKey,
	metaToolKeyPrefix + "files.read",
	host.ReservedPrefix + "model",
	host.ReservedPrefix + "later_setting",
}

// requireRefused checks that err is the refusal of a reserved key: ErrReservedKey,
// invalid_argument, not run, and naming the key.
func requireRefused(t *testing.T, what, key string, err error) {
	t.Helper()
	if !errors.Is(err, host.ErrReservedKey) || !gotato.IsCode(err, gotato.CodeInvalidArgument) || !gotato.NotRun(err) {
		t.Fatalf("%s with %q = %v, want ErrReservedKey (invalid_argument, not run)", what, key, err)
	}
	if got := err.Error(); !strings.Contains(got, key) {
		t.Fatalf("%s: %q does not name the key %q", what, got, key)
	}
}

// A key under "gotato." is the Runner's. CreateSession refuses it, for every key,
// with ErrReservedKey, and writes nothing.
func TestAReservedKeyIsRefusedByCreateSession(t *testing.T) {
	runner, store := newRunner(t)
	ctx := context.Background()
	for _, key := range reservedKeys {
		_, err := runner.CreateSession(ctx, host.CreateSessionRequest{Metadata: map[string]string{"tenant": "a", key: "x"}})
		requireRefused(t, "CreateSession", key, err)
		_, err = runner.CreateSession(ctx, host.CreateSessionRequest{ID: "chosen-id", Metadata: map[string]string{key: "x"}})
		requireRefused(t, "CreateSession with an ID", key, err)
	}
	if list, err := listAll(ctx, store); err != nil || len(list) != 0 {
		t.Fatalf("a refused create left %d Sessions (err=%v)", len(list), err)
	}
	// An ordinary key is still the caller's.
	created, err := runner.CreateSession(ctx, host.CreateSessionRequest{Metadata: map[string]string{"tenant": "a"}})
	if err != nil || created.Metadata["tenant"] != "a" {
		t.Fatalf("CreateSession with an ordinary key = %v, %v", created, err)
	}
}

// RunRequest.Metadata, which makes a new Session's metadata, and RunRequest.Update,
// which changes an existing one, refuse every reserved key too: before the Run is
// admitted, so nothing runs and nothing is written, saved or not.
func TestAReservedKeyIsRefusedByRunMetadataAndUpdate(t *testing.T) {
	model := testkit.NewFakeModel(testkit.Text("ok"))
	runner, store := newRunner(t, profile.AgentProfile{Name: "echo", Model: model})
	ctx := context.Background()
	id := plant(t, store, record.WithMetadata(map[string]string{metaAgentKey: "echo", "tenant": "a"}))
	before := storedMetadata(t, store, id)
	for _, key := range reservedKeys {
		for _, skip := range []bool{false, true} {
			_, err := runner.Run(ctx, host.RunRequest{Prompt: "hi", Metadata: map[string]string{key: "x"}, SkipSave: skip})
			requireRefused(t, "Run.Metadata", key, err)
			_, err = runner.Run(ctx, host.RunRequest{SessionID: id, Prompt: "hi", Update: map[string]string{"tenant": "b", key: "x"}, SkipSave: skip})
			requireRefused(t, "Run.Update", key, err)
			_, err = runner.StreamRun(ctx, host.RunRequest{SessionID: id, Prompt: "hi", Update: map[string]string{key: ""}, SkipSave: skip}, func(gotato.Event) error { return nil })
			requireRefused(t, "StreamRun.Update", key, err)
		}
	}
	if got := model.Requests(); len(got) != 0 {
		t.Fatalf("a refused request reached the Model %d times", len(got))
	}
	if after := storedMetadata(t, store, id); !equalMetadata(before, after) {
		t.Fatalf("a refused Update changed the Session: %v -> %v", before, after)
	}
	if list, err := listAll(ctx, store); err != nil || len(list) != 1 {
		t.Fatalf("refused requests left %d Sessions (err=%v), want the planted one", len(list), err)
	}
	if runner.ActiveRuns() != 0 || runner.ActiveSessions() != 0 {
		t.Fatalf("a refused request is still admitted: runs=%d sessions=%d", runner.ActiveRuns(), runner.ActiveSessions())
	}
	// An ordinary key still updates.
	if _, err := runner.Run(ctx, host.RunRequest{SessionID: id, Prompt: "hi", Update: map[string]string{"tenant": "b"}}); err != nil {
		t.Fatal(err)
	}
	if got := storedMetadata(t, store, id)["tenant"]; got != "b" {
		t.Fatalf("tenant = %q, want the Update's", got)
	}
}

// The Agent of a Session changes only by RunRequest.Agent: an Update that names
// another one is refused, and the request's Agent is the typed way.
func TestTheAgentChangesOnlyByTheRequestsAgent(t *testing.T) {
	runner, store := newRunner(t,
		profile.AgentProfile{Name: "echo", Model: testkit.EchoModel{}},
		profile.AgentProfile{Name: "other", Model: testkit.EchoModel{}},
	)
	ctx := context.Background()
	id := plant(t, store, record.WithMetadata(map[string]string{metaAgentKey: "echo"}))
	_, err := runner.Run(ctx, host.RunRequest{SessionID: id, Prompt: "hi", Update: map[string]string{metaAgentKey: "other"}})
	requireRefused(t, "Run.Update", metaAgentKey, err)
	if got := storedMetadata(t, store, id)[metaAgentKey]; got != "echo" {
		t.Fatalf("agent = %q after a refused Update", got)
	}
	if _, err := runner.Run(ctx, host.RunRequest{SessionID: id, Agent: "other", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	if got := storedMetadata(t, store, id)[metaAgentKey]; got != "other" {
		t.Fatalf("agent = %q after RunRequest.Agent, want other", got)
	}
}

// A tool is switched only by SetToolActive, and the cache key only by Fork: the
// keys that hold them are refused, and the typed ways write them.
func TestAToolSwitchAndTheCacheKeyHaveOneWayEach(t *testing.T) {
	runner, store := newRunner(t, profile.AgentProfile{Name: "tools", Model: testkit.EchoModel{}, Tools: []gotato.Tool{testkit.NewFakeTool("own", "x")}})
	ctx := context.Background()
	created, err := runner.CreateSession(ctx, host.CreateSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.SetToolActive(ctx, created.ID, "own", false); err != nil {
		t.Fatal(err)
	}
	if got := storedMetadata(t, store, created.ID)[metaToolKeyPrefix+"own"]; got != "inactive" {
		t.Fatalf("SetToolActive wrote %q", got)
	}
	child, err := runner.Fork(ctx, created.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := storedMetadata(t, store, child.ID)[metaCacheKeyKey]; got != host.CacheKey(created.ID) {
		t.Fatalf("Fork wrote cache key %q, want the parent's", got)
	}
}
