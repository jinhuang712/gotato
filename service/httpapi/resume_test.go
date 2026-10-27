package httpapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jinhuang712/gotato/gotato"
	"github.com/jinhuang712/gotato/internal/reftasks"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// frame is one server-sent event, read as it arrives.
type frame struct {
	id, event, retry, data string
	comment                bool
}

// frames reads server-sent events from a body, one at a time.
type frames struct{ scanner *bufio.Scanner }

func newFrames(body io.Reader) *frames {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1<<20), 1<<24)
	return &frames{scanner: scanner}
}

// next returns the next frame, or an error when the stream ends.
func (f *frames) next() (frame, error) {
	var out frame
	seen := false
	for f.scanner.Scan() {
		line := f.scanner.Text()
		if line == "" {
			if seen {
				return out, nil
			}
			continue
		}
		seen = true
		if strings.HasPrefix(line, ":") {
			out.comment = true
			continue
		}
		field, value, _ := strings.Cut(line, ": ")
		switch field {
		case "id":
			out.id = value
		case "event":
			out.event = value
		case "retry":
			out.retry = value
		case "data":
			out.data = value
		}
	}
	if err := f.scanner.Err(); err != nil {
		return frame{}, err
	}
	return frame{}, io.EOF
}

// openSessionStream opens a Session's stream with a cancel, extra headers and
// a query string.
func openSession(t *testing.T, server *httptest.Server, id, query string, headers map[string]string) (*http.Response, *frames, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/sessions/"+id+"/stream"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = resp.Body.Close() })
	return resp, newFrames(resp.Body), cancel
}

// startOf reads the stream frame that opens a Session's stream.
func startOf(t *testing.T, f *frames) (start struct {
	SessionID   string `json:"session_id"`
	RecordEpoch string `json:"record_epoch"`
	After       uint64 `json:"after"`
	Head        uint64 `json:"head"`
}, first frame) {
	t.Helper()
	first, err := f.next()
	if err != nil || first.event != "stream" || first.retry == "" {
		t.Fatalf("first frame = %+v, %v; want a stream frame that sets the retry wait", first, err)
	}
	if err := json.Unmarshal([]byte(first.data), &start); err != nil {
		t.Fatal(err)
	}
	return start, first
}

// storedFrame is a stored Event as a stream carries it: the id is its number.
type storedFrame struct {
	number uint64
	kind   string
	data   map[string]any
}

// readStored reads frames until count stored Events came, checks that each has
// its number as the SSE id, and returns them and the deltas seen on the way.
func readStored(t *testing.T, f *frames, count int) (stored []storedFrame, deltas []map[string]any) {
	t.Helper()
	for len(stored) < count {
		fr, err := f.next()
		if err != nil {
			t.Fatalf("after %d of %d stored Events: %v", len(stored), count, err)
		}
		if fr.comment || fr.event == "" {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(fr.data), &data); err != nil {
			t.Fatalf("frame %+v: %v", fr, err)
		}
		if gotato.EventKind(fr.event).Class() == gotato.EventClassStored {
			number, _ := data["number"].(float64)
			if fr.id != strconv.FormatUint(uint64(number), 10) {
				t.Fatalf("a stored Event numbered %v has id %q", number, fr.id)
			}
			stored = append(stored, storedFrame{number: uint64(number), kind: fr.event, data: data})
			continue
		}
		if fr.id != "" {
			t.Fatalf("a %s frame has id %q: only stored Events do", fr.event, fr.id)
		}
		deltas = append(deltas, data)
	}
	return stored, deltas
}

func runOnce(t *testing.T, runner *service.Runner) service.RunResponse {
	t.Helper()
	result, err := runner.Run(context.Background(), service.RunRequest{Prompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// A Session's stream goes on after the number in Last-Event-ID, as a browser's
// EventSource sends it by itself, or in the after parameter, which wins. Each
// stored Event has its number as its id, and the first frame tells the epoch.
func TestSessionStreamResumesAfterLastEventID(t *testing.T) {
	server, runner := newServer(t)
	first := runOnce(t, runner)
	id := first.SessionID
	record, err := runner.Record(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	head := int(record.Head())

	resp, f, _ := openSession(t, server, id, "", map[string]string{"Last-Event-ID": "3"})
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d, type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	start, _ := startOf(t, f)
	if start.SessionID != id || start.RecordEpoch == "" || start.RecordEpoch != first.RecordEpoch || start.After != 3 || start.Head != uint64(head) {
		t.Fatalf("stream frame = %+v; want session %s, epoch %q, after 3, head %d", start, id, first.RecordEpoch, head)
	}
	if resp.Header.Get("Record-Epoch") != start.RecordEpoch {
		t.Fatalf("Record-Epoch header %q, frame %q", resp.Header.Get("Record-Epoch"), start.RecordEpoch)
	}
	stored, _ := readStored(t, f, head-3)
	for i, event := range stored {
		if event.number != uint64(4+i) {
			t.Fatalf("stored Events = %v at %d, want consecutive from 4", event.number, i)
		}
	}

	// The after parameter wins over Last-Event-ID.
	_, f2, _ := openSession(t, server, id, "?after="+strconv.Itoa(head-1), map[string]string{"Last-Event-ID": "1"})
	startOf(t, f2)
	stored, _ = readStored(t, f2, 1)
	if stored[0].number != uint64(head) {
		t.Fatalf("after=%d resumed at %d", head-1, stored[0].number)
	}

	// The same stream carries the Events of a later Run, live, from head+1.
	go func() { _, _ = runner.Run(context.Background(), service.RunRequest{SessionID: id, Prompt: "two"}) }()
	live, _ := readStored(t, f, 3)
	if live[0].number != uint64(head)+1 {
		t.Fatalf("the live part starts at %d, want %d", live[0].number, head+1)
	}
}

// A position that cannot be resumed is refused before the stream starts, with
// the Record's current epoch, so the client starts over.
func TestSessionStreamRefusesWhatItCannotResume(t *testing.T) {
	server, runner := newServer(t)
	first := runOnce(t, runner)
	id := first.SessionID
	record, _ := runner.Record(context.Background(), id)

	cases := []struct {
		name, id, query string
		headers         map[string]string
		status          int
		code            string
	}{
		{"another epoch", id, "?after=2&record_epoch=other", nil, http.StatusConflict, "cannot_resume"},
		{"another epoch in a header", id, "?after=2", map[string]string{"Record-Epoch": "other"}, http.StatusConflict, "cannot_resume"},
		{"past the head", id, "?after=" + strconv.FormatUint(record.Head()+1, 10), nil, http.StatusConflict, "cannot_resume"},
		{"unknown Session", "missing", "", nil, http.StatusNotFound, "not_found"},
		{"not a number", id, "?after=x", nil, http.StatusBadRequest, ""},
		{"not a number in the header", id, "", map[string]string{"Last-Event-ID": "-1"}, http.StatusBadRequest, ""},
		{"header for another Session", id, "", map[string]string{"session-id": "other"}, http.StatusBadRequest, ""},
	}
	for _, c := range cases {
		resp, _, _ := func() (*http.Response, *frames, context.CancelFunc) {
			req, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/sessions/"+c.id+"/stream"+c.query, nil)
			for k, v := range c.headers {
				req.Header.Set(k, v)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = resp.Body.Close() })
			return resp, nil, nil
		}()
		body, _ := io.ReadAll(resp.Body)
		var parsed struct {
			Code        string `json:"code"`
			RecordEpoch string `json:"record_epoch"`
		}
		_ = json.Unmarshal(body, &parsed)
		if resp.StatusCode != c.status || parsed.Code != c.code {
			t.Fatalf("%s: %d %s; want %d %q", c.name, resp.StatusCode, body, c.status, c.code)
		}
		if c.code == "cannot_resume" && parsed.RecordEpoch != first.RecordEpoch {
			t.Fatalf("%s: the refusal names epoch %q, want the Record's %q", c.name, parsed.RecordEpoch, first.RecordEpoch)
		}
	}

	// The Record created again under the same ID has another epoch: the old
	// one is refused, and no epoch starts from the first Event.
	if err := runner.DeleteSession(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.CreateSessionExclusive(context.Background(), "echo", id, nil); err != nil {
		t.Fatal(err)
	}
	resp, _, _ := openSessionStatus(t, server, id, "?after=1&record_epoch="+first.RecordEpoch)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("a Record created again: %d, want 409", resp.StatusCode)
	}
	resp, f, _ := openSession(t, server, id, "", nil)
	start, _ := startOf(t, f)
	if resp.StatusCode != http.StatusOK || start.RecordEpoch == first.RecordEpoch {
		t.Fatalf("the new Record: %d, epoch %q (the old one %q)", resp.StatusCode, start.RecordEpoch, first.RecordEpoch)
	}
}

func openSessionStatus(t *testing.T, server *httptest.Server, id, query string) (*http.Response, []byte, error) {
	t.Helper()
	resp, err := http.Get(server.URL + "/v1/sessions/" + id + "/stream" + query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	return resp, body, err
}

// A Run's stream carries each stored Event's number as the SSE id too, and the
// result carries the Record's epoch, so a client that loses the stream
// reconnects through the Session's stream after the last number it saw.
func TestRunStreamCarriesNumbersAndTheEpoch(t *testing.T) {
	server, _ := newServer(t)
	id := newSession(t, server, "echo")
	resp, err := http.Post(server.URL+"/v1/sessions/"+id+"/runs/stream", "application/json", strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	f := newFrames(resp.Body)
	var last uint64
	var result service.RunResponse
	var sessionID string
	for {
		fr, err := f.next()
		if err != nil {
			break
		}
		if fr.event == "result" {
			if err := json.Unmarshal([]byte(fr.data), &result); err != nil {
				t.Fatal(err)
			}
			continue
		}
		var data struct {
			Number      uint64 `json:"number"`
			SessionID   string `json:"session_id"`
			RecordEpoch string `json:"record_epoch"`
		}
		_ = json.Unmarshal([]byte(fr.data), &data)
		sessionID = data.SessionID
		if gotato.EventKind(fr.event).Class() == gotato.EventClassStored {
			if last == 0 {
				// The stream of a Run on an existing Session starts at the
				// Run's own first Event.
				last = data.Number - 1
			}
			if fr.id != strconv.FormatUint(data.Number, 10) || data.Number != last+1 {
				t.Fatalf("%s: id %q, number %d after %d", fr.event, fr.id, data.Number, last)
			}
			last = data.Number
		} else if fr.id != "" {
			t.Fatalf("%s carries id %q", fr.event, fr.id)
		}
	}
	if result.RecordEpoch == "" || sessionID != id || result.SessionID != id || last == 0 {
		t.Fatalf("result = %+v, session %q, last number %d", result, sessionID, last)
	}
	// Reconnect through the Session's stream after the last number seen: all
	// that is left is what the Run wrote after it.
	_, sf, _ := openSession(t, server, sessionID, "?after="+strconv.FormatUint(last, 10)+"&record_epoch="+result.RecordEpoch, nil)
	start, _ := startOf(t, sf)
	if start.RecordEpoch != result.RecordEpoch {
		t.Fatalf("epoch %q, the result's %q", start.RecordEpoch, result.RecordEpoch)
	}
}

// A drain ends a Session's stream once the Runs are saved, after the stored
// Events, with the advice to reconnect, jittered, and the reason.
func TestDrainEndsASessionStreamWithAReconnectFrame(t *testing.T) {
	server, runner := newServer(t)
	first := runOnce(t, runner)
	_, f, _ := openSession(t, server, first.SessionID, "", nil)
	start, _ := startOf(t, f)
	readStored(t, f, int(start.Head))
	if err := runner.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	fr, err := f.next()
	if err != nil || fr.event != "reconnect" || fr.retry == "" {
		t.Fatalf("after the drain: %+v, %v; want a reconnect frame", fr, err)
	}
	var hint service.Reconnect
	if err := json.Unmarshal([]byte(fr.data), &hint); err != nil || hint.Why != "draining" || strconv.FormatInt(hint.RetryAfterMS, 10) != fr.retry {
		t.Fatalf("reconnect = %+v (%v), retry %q", hint, err, fr.retry)
	}
	if _, err := f.next(); err != io.EOF {
		t.Fatalf("the stream went on after the reconnect frame: %v", err)
	}
}

// An idle stream sends a comment now and then, so a proxy keeps it open.
func TestSessionStreamSendsKeepaliveComments(t *testing.T) {
	runner, err := service.New(service.Config{Store: session.NewMemoryStore(), Agents: []service.Agent{{Name: "echo", Model: testkit.EchoModel{}}}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(runner, httpapi.WithKeepAlive(30*time.Millisecond)))
	t.Cleanup(server.Close)
	first := runOnce(t, runner)
	_, f, _ := openSession(t, server, first.SessionID, "", nil)
	start, _ := startOf(t, f)
	readStored(t, f, int(start.Head))
	fr, err := f.next()
	if err != nil || !fr.comment {
		t.Fatalf("an idle stream sent %+v, %v; want a comment", fr, err)
	}
}

// ---- the exit test ----------------------------------------------------------

// normalized is a stored Event as the stream carried it, as plain values.
func normalized(events []storedFrame) []string {
	out := make([]string, len(events))
	for i, e := range events {
		// A live Event and the same Event read from the Record write their
		// times in different zones: compare the instants.
		for key, value := range e.data {
			if text, ok := value.(string); ok && (key == "time" || key == "created_at") {
				if at, err := time.Parse(time.RFC3339Nano, text); err == nil {
					e.data[key] = at.UTC().Format(time.RFC3339Nano)
				}
			}
		}
		data, _ := json.Marshal(e.data)
		out[i] = fmt.Sprintf("%d %s %s", e.number, e.kind, data)
	}
	return out
}

// follower reads a Session's stream in the background and keeps its stored
// Events and deltas.
type follower struct {
	mu     sync.Mutex
	stored []storedFrame
	deltas []map[string]any
	done   chan struct{}
}

func follow(t *testing.T, f *frames) *follower {
	fl := &follower{done: make(chan struct{})}
	go func() {
		defer close(fl.done)
		for {
			fr, err := f.next()
			if err != nil {
				return
			}
			if fr.comment || fr.event == "" || fr.event == "stream" || fr.event == "reconnect" {
				continue
			}
			var data map[string]any
			if json.Unmarshal([]byte(fr.data), &data) != nil {
				continue
			}
			fl.mu.Lock()
			if gotato.EventKind(fr.event).Class() == gotato.EventClassStored {
				number, _ := data["number"].(float64)
				fl.stored = append(fl.stored, storedFrame{number: uint64(number), kind: fr.event, data: data})
			} else {
				fl.deltas = append(fl.deltas, data)
			}
			fl.mu.Unlock()
		}
	}()
	return fl
}

// reached waits until the follower holds a stored Event numbered at least n.
func (fl *follower) reached(t *testing.T, n uint64) []storedFrame {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		fl.mu.Lock()
		if len(fl.stored) > 0 && fl.stored[len(fl.stored)-1].number >= n {
			out := append([]storedFrame(nil), fl.stored...)
			fl.mu.Unlock()
			return out
		}
		fl.mu.Unlock()
		select {
		case <-fl.done:
			t.Fatalf("the stream ended before Event %d", n)
		case <-time.After(2 * time.Millisecond):
		}
	}
	fl.mu.Lock()
	var got []string
	for _, e := range fl.stored {
		got = append(got, fmt.Sprintf("%d:%s", e.number, e.kind))
	}
	fl.mu.Unlock()
	t.Fatalf("the stream never reached Event %d; it holds %v", n, got)
	return nil
}

// playTask plays a reference task's prompts on a new Session, one Run each,
// through the HTTP handler, and reports the Session's last number.
func playTask(t *testing.T, server *httptest.Server, runner *service.Runner, task reftasks.Task, id string) uint64 {
	t.Helper()
	for _, prompt := range task.Prompts {
		body := fmt.Sprintf(`{"prompt":%q}`, prompt)
		resp, err := http.Post(server.URL+"/v1/sessions/"+id+"/runs", "application/json", strings.NewReader(body))
		if err != nil {
			t.Error(err)
			return 0
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("run = %d", resp.StatusCode)
			return 0
		}
	}
	record, err := runner.Record(context.Background(), id)
	if err != nil {
		t.Error(err)
		return 0
	}
	return record.Head()
}

func taskRunner(t *testing.T, task reftasks.Task) (*httptest.Server, *service.Runner) {
	t.Helper()
	runner, err := service.New(service.Config{
		Store: session.NewMemoryStore(),
		Agents: []service.Agent{{
			Name: task.Name, Model: task.Model, Instruction: task.Instruction, Tools: task.Tools, Options: task.Options, Limits: task.Limits,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(runner))
	t.Cleanup(server.Close)
	return server, runner
}

// cutAndResume plays task once, with one reader that never lets go and one
// that is cut after its k-th stored Event and reconnects after k, with the
// epoch it was told, by Last-Event-ID or by the after parameter. It returns the
// Events of the uncut reader and of the cut one, the two parts joined.
func cutAndResume(t *testing.T, task reftasks.Task, k int, viaHeader bool) (uncut, resumed []storedFrame) {
	server, runner := taskRunner(t, task)
	created, err := runner.CreateSession(context.Background(), task.Name, nil, session.WithMessages(task.History...))
	if err != nil {
		t.Fatal(err)
	}
	id := created.ID()

	_, uf, _ := openSession(t, server, id, "", nil)
	startOf(t, uf)
	whole := follow(t, uf)

	_, cf, cancel := openSession(t, server, id, "", nil)
	start, _ := startOf(t, cf)
	if start.RecordEpoch == "" {
		t.Fatal("no epoch")
	}

	final := make(chan uint64, 1)
	go func() { final <- playTask(t, server, runner, task, id) }()

	firstPart, deltasBefore := readStored(t, cf, k)
	cancel() // the connection is cut here
	query, headers := "?record_epoch="+start.RecordEpoch+"&after="+strconv.Itoa(k), map[string]string(nil)
	if viaHeader {
		query, headers = "?record_epoch="+start.RecordEpoch, map[string]string{"Last-Event-ID": strconv.Itoa(k)}
	}
	resp, rf, _ := openSession(t, server, id, query, headers)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resume after %d: %d", k, resp.StatusCode)
	}
	again := follow(t, rf)

	last := <-final
	if last == 0 {
		t.FailNow()
	}
	uncut = whole.reached(t, last)
	var secondPart []storedFrame
	if uint64(k) < last {
		secondPart = again.reached(t, last)
	}

	// What the cut reader read before the cut, and after: no gap, no repeat.
	resumed = append(append([]storedFrame(nil), firstPart...), secondPart...)

	// Deltas sent before the cut are not sent again: those of the resumed
	// stream all follow a stored Event numbered at least k.
	again.mu.Lock()
	for _, d := range again.deltas {
		if number, _ := d["number"].(float64); uint64(number) < uint64(k) {
			t.Errorf("the resumed stream sent a delta that follows Event %v, before the cut at %d", number, k)
		}
	}
	again.mu.Unlock()
	_ = deltasBefore
	return uncut, resumed
}

// TestStreamCutAtEveryEventResumesEqualToAnUncutOne is the Phase 5 exit test,
// over HTTP: for each Event of each reference task, a Session's stream is cut
// after that Event and resumed after its number, and what the client read, the
// part before the cut and the part after, is the same as what a stream that
// was never cut read: the same stored Events, in the same order, numbered from
// 1 with no gap and no repeat.
func TestStreamCutAtEveryEventResumesEqualToAnUncutOne(t *testing.T) {
	for _, task := range reftasks.ReferenceTasks() {
		t.Run(task.Name, func(t *testing.T) {
			calibration, _ := cutAndResume(t, task, 1, true)
			total := len(calibration)
			if total < 6 {
				t.Fatalf("a play made only %d Events", total)
			}
			t.Logf("%d Events, a cut after each", total)
			for k := 1; k <= total; k++ {
				uncut, resumed := cutAndResume(t, task, k, k%2 == 0)
				for i, e := range uncut {
					if e.number != uint64(i+1) {
						t.Fatalf("cut after %d: the uncut stream has Event %d at place %d", k, e.number, i+1)
					}
				}
				if got, want := normalized(resumed), normalized(uncut); !reflect.DeepEqual(got, want) {
					t.Fatalf("cut after %d: the resumed stream differs from the uncut one (%d Events, uncut %d); first difference: %s", k, len(got), len(want), firstDifference(got, want))
				}
			}
		})
	}
}

func firstDifference(got, want []string) string {
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			return fmt.Sprintf("at place %d:\n got  %s\n want %s", i+1, got[i], want[i])
		}
	}
	return "one is a prefix of the other"
}
