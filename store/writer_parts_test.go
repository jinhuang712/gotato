package store_test

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/store"
	"github.com/jinhuang712/gotato/testkit"
)

// stallModel streams three 3000-byte deltas and then waits, as a model does
// when the process that called it stops.
type stallModel struct{ sent chan struct{} }

type stallStream struct {
	n    int
	sent chan struct{}
}

func (m stallModel) Stream(context.Context, gotato.ModelRequest) (gotato.ModelStream, error) {
	return &stallStream{sent: m.sent}, nil
}

// Config states nothing: no window, no retention, no sampling.
func (m stallModel) Config() gotato.ModelConfig { return gotato.ModelConfig{} }

func (s *stallStream) Recv(ctx context.Context) (gotato.ModelEvent, error) {
	s.n++
	if s.n <= 3 {
		return gotato.ModelEvent{Kind: gotato.ModelEventKindTextDelta, Text: strings.Repeat("x", 3000)}, nil
	}
	if s.n == 4 {
		close(s.sent)
	}
	<-ctx.Done()
	return gotato.ModelEvent{}, ctx.Err()
}

func (s *stallStream) Close() error { return nil }

func cutOffTexts(messages []gotato.Message) []string {
	var out []string
	for _, message := range messages {
		if message.StopReason == gotato.StopReasonCutOff {
			out = append(out, gotato.TextOf(message))
		}
	}
	return out
}

// A Writer that stops in the middle of a streaming reply loses at most the last
// part: the Instance that takes the Session over rebuilds the Cut-off reply from
// the parts kept, writes it as a fact, and every later load has it too.
func TestTakeoverRebuildsTheCutOffReplyFromItsParts(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	rec := record.New()
	if _, err := store.CreateWriter(ctx, st, rec, "a", store.WithIdle()); err != nil {
		t.Fatal(err)
	}
	writer, err := store.OpenWriter(ctx, st, rec.ID(), "a")
	if err != nil {
		t.Fatal(err)
	}
	sent := make(chan struct{})
	live, err := gotato.NewSession(context.Background(), gotato.WithModel(stallModel{sent: sent}), gotato.WithTranscript(writer))
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = live.Prompt(runCtx, gotato.UserMessage("hi"))
	}()
	<-sent
	waitStored(t, st, rec.ID(), gotato.EventKindReplyPartKept, 1)

	// While the Run is in flight a load holds its run_started and the parts
	// of the reply, as facts.
	inFlight, err := st.Load(ctx, rec.ID())
	if err != nil {
		t.Fatal(err)
	}
	var kinds []gotato.EventKind
	for _, entry := range inFlight.Entries {
		kinds = append(kinds, entry.Kind)
	}
	if !slices.Contains(kinds, gotato.EventKindRunStarted) || !slices.Contains(kinds, gotato.EventKindReplyPartKept) || !heldOf(t, inFlight) {
		t.Fatalf("a load of a Record with a Run in flight holds %v (held %v), want run_started and reply_part_kept, and the Record held", kinds, heldOf(t, inFlight))
	}

	// The Instance that held the Session stopped: another one takes it over,
	// with one Load and no Log: the Load holds the parts.
	counting := &countingRecords{Records: st}
	opened, err := store.OpenWriter(ctx, counting, rec.ID(), "b")
	if err != nil {
		t.Fatal(err)
	}
	if loads, logs := counting.counts(); loads != 1 || logs != 0 {
		t.Fatalf("OpenWriter made %d Loads and %d Logs, want 1 and 0", loads, logs)
	}
	cancel()
	<-done
	_ = live.Close(ctx)
	want := strings.Repeat("x", 6000) // the two deltas of the last part; the third was never written
	if got := cutOffTexts(opened.Record().Messages()); len(got) != 1 || got[0] != want {
		t.Fatalf("OpenWriter's Messages hold %d Cut-off replies %v, want one of %d bytes", len(got), len(got), len(want))
	}
	if err := opened.Release(ctx); err != nil {
		t.Fatal(err)
	}
	for name, load := range map[string]func() (*record.Record, error){
		"Open": func() (*record.Record, error) { return store.Open(ctx, st, rec.ID()) },
		"View": func() (*record.Record, error) { return store.View(ctx, st, rec.ID()) },
	} {
		loaded, err := load()
		if err != nil {
			t.Fatal(err)
		}
		if got := cutOffTexts(loaded.Messages()); len(got) != 1 || len(got[0]) != len(want) {
			t.Fatalf("%s holds %d Cut-off replies, want one of %d bytes", name, len(got), len(want))
		}
	}
	events, err := store.StoredEvents(ctx, st, rec.ID(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	cut := 0
	for _, event := range events {
		if _, ok := event.(gotato.ReplyCutOff); ok {
			cut++
		}
	}
	if cut != 1 {
		t.Fatalf("the Record holds %d reply_cut_off Events, want 1", cut)
	}
}

// A Record that is not stored keeps no parts of a reply: they exist so that a
// crash loses little, and nothing is stored.
func TestUnstoredRecordKeepsNoReplyParts(t *testing.T) {
	ctx := context.Background()
	rec := record.New()
	sent := make(chan struct{})
	live, err := gotato.NewSession(context.Background(), gotato.WithModel(stallModel{sent: sent}), gotato.WithTranscript(rec))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.Close(ctx) }()
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = live.Prompt(runCtx, gotato.UserMessage("hi"))
	}()
	<-sent
	cancel()
	<-done
	for _, event := range rec.Events() {
		if event.Kind() == gotato.EventKindReplyPartKept {
			t.Fatalf("an unstored Record holds %s", event.Kind())
		}
	}
}

// countStored counts the stored Events of a kind in a Session's Record.
func countStored(t *testing.T, st store.Records, id string, kind gotato.EventKind) int {
	t.Helper()
	stored, err := store.StoredEvents(context.Background(), st, id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range stored {
		if event.Kind() == kind {
			count++
		}
	}
	return count
}

// waitStored waits until the stored Events of a Session hold at least n of a
// kind. The Session stages its writes, so a test that acts on what the Record
// holds waits for it instead of guessing a delay.
func waitStored(t *testing.T, st store.Records, id string, kind gotato.EventKind, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		count := countStored(t, st, id, kind)
		if count >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the Record of %s never held %d %s Events (has %d)", id, n, kind, count)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestViewRebuildsACutOffReplyFromItsParts(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	writer, err := store.CreateWriter(ctx, st, record.New(), "instance-a")
	if err != nil {
		t.Fatal(err)
	}
	commit(t, writer, gotato.UserMessage("question"))
	for _, text := range []string{"half an ", "answer"} {
		if _, err := writer.Write(ctx, []gotato.Event{gotato.ReplyPartKept{Header: header(), MessageID: "reply-1", Text: text}}); err != nil {
			t.Fatal(err)
		}
	}
	// The instance stops mid-stream; another one takes the Session over.
	next, err := store.OpenWriter(ctx, st, writer.ID(), "instance-b")
	if err != nil {
		t.Fatal(err)
	}
	commit(t, next, gotato.UserMessage("again"))
	view, err := store.View(ctx, st, writer.ID())
	if err != nil {
		t.Fatal(err)
	}
	messages := view.Messages()
	if len(messages) != 3 || messages[1].StopReason != gotato.StopReasonCutOff || gotato.TextOf(messages[1]) != "half an answer" || gotato.TextOf(messages[2]) != "again" {
		t.Fatalf("view = %+v, want the cut-off reply between the two prompts", messages)
	}
	// The parts are facts: the takeover read them and wrote the Cut-off
	// reply, so a load holds what the whole log holds.
	facts, err := store.Open(ctx, st, writer.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(facts.Messages(), messages) {
		t.Fatalf("facts = %+v, want what the whole log holds, %+v", facts.Messages(), messages)
	}
}

type blockModel struct{ started chan struct{} }

func (m blockModel) Stream(ctx context.Context, _ gotato.ModelRequest) (gotato.ModelStream, error) {
	close(m.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

// Config states nothing: no window, no retention, no sampling.
func (m blockModel) Config() gotato.ModelConfig { return gotato.ModelConfig{} }

// A reply that a limit refuses leaves parts of its text and no Message. When a
// later Run crashes, the load that reads the log for that Run rebuilds only the
// parts of the Run that never ended: the refused reply is not brought back into
// the middle of the history, and every kind of load agrees.
func TestRefusedReplyPartsDoNotReappearAfterACrash(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	rec := record.New()
	if _, err := store.CreateWriter(ctx, st, rec, "a", store.WithIdle()); err != nil {
		t.Fatal(err)
	}
	writer, err := store.OpenWriter(ctx, st, rec.ID(), "a")
	if err != nil {
		t.Fatal(err)
	}
	limits := gotato.DefaultLimits()
	limits.MaxMessageBytes = 2000
	big := testkit.NewFakeModel(testkit.Text(strings.Repeat("y", 5000)))
	first, err := gotato.NewSession(context.Background(), gotato.WithModel(big), gotato.WithTranscript(writer), gotato.WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	if result, _ := first.Prompt(ctx, gotato.UserMessage("p1")); result.Status != gotato.RunStatusFailed {
		t.Fatalf("the first Run is %s, want failed on the Message limit", result.Status)
	}
	_ = first.Close(ctx)
	if err := writer.Release(ctx); err != nil {
		t.Fatal(err)
	}

	second, err := store.OpenWriter(ctx, st, rec.ID(), "a")
	if err != nil {
		t.Fatal(err)
	}
	turnsBefore := countStored(t, st, rec.ID(), gotato.EventKindTurnStarted)
	started := make(chan struct{})
	live, err := gotato.NewSession(context.Background(), gotato.WithModel(blockModel{started}), gotato.WithTranscript(second))
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = live.Prompt(runCtx, gotato.UserMessage("p2"))
	}()
	<-started
	waitStored(t, st, rec.ID(), gotato.EventKindTurnStarted, turnsBefore+1)

	taken, err := store.OpenWriter(ctx, st, rec.ID(), "b") // the crash: B takes over
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done
	_ = live.Close(ctx)
	if err := taken.Release(ctx); err != nil {
		t.Fatal(err)
	}
	for name, messages := range map[string][]gotato.Message{"OpenWriter": taken.Record().Messages()} {
		if got := cutOffTexts(messages); len(got) != 0 {
			t.Fatalf("%s brought back %d refused replies: %v", name, len(got), messages)
		}
	}
	open, err := store.Open(ctx, st, rec.ID())
	if err != nil {
		t.Fatal(err)
	}
	view, err := store.View(ctx, st, rec.ID())
	if err != nil {
		t.Fatal(err)
	}
	if a, b, c := len(taken.Record().Messages()), len(open.Messages()), len(view.Messages()); a != b || b != c || a != 2 {
		t.Fatalf("OpenWriter has %d Messages, Open %d, View %d; want 2 each", a, b, c)
	}
}
