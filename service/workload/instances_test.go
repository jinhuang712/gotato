package workload_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gotato "github.com/jinhuang712/gotato"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

// ---- the instances ----------------------------------------------------------

// killStore is an instance's view of the shared store. Once dead it fails
// every call, as a store does for a process that is gone.
type killStore struct {
	session.Store
	dead atomic.Bool
}

func (s *killStore) alive() error {
	if s.dead.Load() {
		return errCrashed
	}
	return nil
}

func (s *killStore) Create(ctx context.Context, id string, entries []session.Entry, summary session.Summary) error {
	if err := s.alive(); err != nil {
		return err
	}
	return s.Store.Create(ctx, id, entries, summary)
}

func (s *killStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.Summary) error {
	if err := s.alive(); err != nil {
		return err
	}
	return s.Store.Append(ctx, id, after, entries, summary)
}

func (s *killStore) Load(ctx context.Context, id string) (session.Loaded, error) {
	if err := s.alive(); err != nil {
		return session.Loaded{}, err
	}
	return s.Store.Load(ctx, id)
}

func (s *killStore) Log(ctx context.Context, id string, after uint64) ([]session.Entry, error) {
	if err := s.alive(); err != nil {
		return nil, err
	}
	return s.Store.Log(ctx, id, after)
}

func (s *killStore) Beat(ctx context.Context, row session.InstanceRow, ttl time.Duration) error {
	if err := s.alive(); err != nil {
		return err
	}
	return s.Store.Beat(ctx, row, ttl)
}

// instance is one Runner behind its own HTTP server.
type instance struct {
	name   string
	runner *service.Runner
	store  *killStore
	server *httptest.Server
	killed atomic.Bool
}

// cluster is several instances over one shared store, and a proxy in front of
// them that hashes on the session-id header.
type cluster struct {
	t         *testing.T
	backing   session.Store
	instances []*instance
	proxy     *hashProxy
	front     *httptest.Server
	random    *rand.Rand
	mu        sync.Mutex
}

var clusterLiveness = service.Liveness{TTL: 300 * time.Millisecond, Wait: 5 * time.Second}

// newCluster starts n instances over one new store, each serving every task as
// an Agent, and the hashing proxy in front of them.
func newCluster(t *testing.T, n int, tasks []testkit.Task, seed uint64, adjust ...func(i int, cfg *service.Config)) *cluster {
	t.Helper()
	c := &cluster{t: t, backing: session.NewMemoryStore(), random: rand.New(rand.NewPCG(seed, 99))}
	var agents []service.Agent
	for _, task := range tasks {
		agents = append(agents, service.Agent{Name: task.Name, Model: task.Model, Instruction: task.Instruction, Tools: task.Tools, Options: task.Options, Limits: task.Limits})
	}
	var backends []*backend
	for i := range n {
		in := &instance{name: fmt.Sprintf("instance-%d", i), store: &killStore{Store: c.backing}}
		cfg := service.Config{
			Store: in.store, Instance: in.name, Liveness: clusterLiveness, StreamPoll: 20 * time.Millisecond,
			Admission: service.Admission{Queue: service.WaitWhileBusy},
			Agents:    agents,
		}
		for _, change := range adjust {
			change(i, &cfg)
		}
		runner, err := service.New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		in.runner = runner
		// The configured name is a label; the store knows the Instance by
		// the name with its per-start suffix.
		in.name = runner.Instance()
		in.server = httptest.NewServer(httpapi.New(runner))
		t.Cleanup(func() {
			in.server.CloseClientConnections()
			in.server.Close()
			_ = runner.Close(context.Background())
		})
		c.instances = append(c.instances, in)
		u, _ := url.Parse(in.server.URL)
		backends = append(backends, &backend{name: in.name, host: u.Host})
	}
	c.proxy = newHashProxy(backends, seed)
	c.front = httptest.NewServer(&httputil.ReverseProxy{
		Director:      func(r *http.Request) { r.URL.Scheme, r.URL.Host = "http", "backend" },
		Transport:     c.proxy,
		FlushInterval: -1,
	})
	t.Cleanup(c.front.Close)
	return c
}

// live returns the instances that are not killed.
func (c *cluster) live() []*instance {
	var out []*instance
	for _, in := range c.instances {
		if !in.killed.Load() {
			out = append(out, in)
		}
	}
	return out
}

// anyInstance picks a live instance at random.
func (c *cluster) anyInstance() *instance {
	c.mu.Lock()
	defer c.mu.Unlock()
	live := c.live()
	return live[c.random.IntN(len(live))]
}

// coin decides, at random, between the proxy and a straight request.
func (c *cluster) coin() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.random.IntN(2) == 0
}

// kill makes an instance die: its store fails every call, and its server stops
// answering. The proxy learns it by the refused connection.
func (c *cluster) kill(in *instance) {
	in.killed.Store(true)
	in.store.dead.Store(true)
	in.server.CloseClientConnections()
	go in.server.Close()
}

// ---- the hashing proxy ------------------------------------------------------

type backend struct {
	name, host string
	down       atomic.Bool
}

type ringPoint struct {
	hash    uint64
	backend *backend
}

// hashProxy is the load balancer the documents describe: a consistent hash on
// the session-id header, one retry on another instance after a 503 marked not
// run, and an instance whose connection is refused leaves the ring. A request
// whose connection broke after it was sent is not retried: it may have run. A request
// with no header, one that creates a Session, goes to a random instance.
type hashProxy struct {
	backends []*backend
	ring     []ringPoint
	mu       sync.Mutex
	random   *rand.Rand
	retries  atomic.Int64
	hashed   atomic.Int64
}

func hashOf(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

func newHashProxy(backends []*backend, seed uint64) *hashProxy {
	p := &hashProxy{backends: backends, random: rand.New(rand.NewPCG(seed, 5))}
	for _, b := range backends {
		for v := range 64 {
			p.ring = append(p.ring, ringPoint{hash: hashOf(fmt.Sprintf("%s#%d", b.name, v)), backend: b})
		}
	}
	sort.Slice(p.ring, func(i, j int) bool { return p.ring[i].hash < p.ring[j].hash })
	return p
}

// candidates lists the live instances for a key, the Session's own first.
func (p *hashProxy) candidates(key string) []*backend {
	var out []*backend
	if key == "" {
		p.mu.Lock()
		order := p.random.Perm(len(p.backends))
		p.mu.Unlock()
		for _, i := range order {
			if !p.backends[i].down.Load() {
				out = append(out, p.backends[i])
			}
		}
		return out
	}
	start := sort.Search(len(p.ring), func(i int) bool { return p.ring[i].hash >= hashOf(key) })
	for i := range p.ring {
		b := p.ring[(start+i)%len(p.ring)].backend
		if !b.down.Load() && !slices.Contains(out, b) {
			out = append(out, b)
		}
	}
	return out
}

// owner returns the instance a key hashes to, for the test to check.
func (p *hashProxy) owner(key string) string {
	if c := p.candidates(key); len(c) > 0 {
		return c[0].name
	}
	return ""
}

// RoundTrip sends a request to the instance its session-id hashes to, and once
// more to the next one when the first is gone or refused it as not run.
func (p *hashProxy) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
	}
	key := strings.TrimSpace(req.Header.Get(httpapi.SessionIDHeader))
	if key != "" {
		p.hashed.Add(1)
	}
	var lastErr error
	tries := 0
	for _, b := range p.candidates(key) {
		if tries == 2 {
			break
		}
		tries++
		out := req.Clone(req.Context())
		out.URL.Scheme, out.URL.Host = "http", b.host
		out.Body = io.NopCloser(bytes.NewReader(body))
		out.ContentLength = int64(len(body))
		resp, err := http.DefaultTransport.RoundTrip(out)
		if err != nil {
			var opErr *net.OpError
			if req.Context().Err() != nil || !errors.As(err, &opErr) || opErr.Op != "dial" {
				// The connection broke after the request may have been
				// sent: it may have run, so it is not sent again.
				return nil, err
			}
			// Refused before anything was sent: the instance is gone.
			b.down.Store(true)
			lastErr = err
			p.retries.Add(1)
			continue
		}
		if resp.StatusCode == http.StatusServiceUnavailable && resp.Header.Get(httpapi.NotRunHeader) == "true" && tries == 1 {
			_ = resp.Body.Close()
			p.retries.Add(1)
			continue
		}
		return resp, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no instance can take the request")
	}
	return nil, lastErr
}

// ---- the client -------------------------------------------------------------

type reply struct {
	status int
	header http.Header
	body   []byte
}

// send makes one request, to the proxy or straight to an instance.
func send(t *testing.T, base, method, path string, body any, sessionID string) reply {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req, err := http.NewRequest(method, base+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		req.Header.Set(httpapi.SessionIDHeader, sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return reply{status: 0, body: []byte(err.Error())}
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, header: resp.Header, body: data}
}

// prompt sends one prompt of a Session, through the proxy or straight to a
// random live instance.
func (c *cluster) prompt(sessionID, text string) reply {
	base := c.front.URL
	if !c.coin() {
		base = c.anyInstance().server.URL
	}
	return send(c.t, base, http.MethodPost, "/v1/sessions/"+sessionID+"/runs", map[string]any{"prompt": text}, sessionID)
}

// create makes a Session for a task. A task with a history is created on an
// instance directly, since HTTP creates only empty Sessions; the others
// through the proxy, which sends the request to a random instance.
func (c *cluster) create(task testkit.Task) string {
	c.t.Helper()
	if len(task.History) > 0 {
		s, err := c.anyInstance().runner.CreateSession(context.Background(), task.Name, nil, session.WithMessages(task.History...))
		if err != nil {
			c.t.Fatal(err)
		}
		return s.ID()
	}
	r := send(c.t, c.front.URL, http.MethodPost, "/v1/sessions", map[string]any{"agent": task.Name}, "")
	id := r.header.Get(httpapi.SessionIDHeader)
	if r.status != http.StatusCreated || id == "" {
		c.t.Fatalf("create = %d %s", r.status, r.body)
	}
	return id
}

// ---- what is compared -------------------------------------------------------

// view is what a Session's Record holds that must not depend on the number of
// instances: its Transcript, and its Events in order, by number and kind. The
// Claims differ by nature (which instance, and why), so they are compared by
// number and kind only.
type view struct {
	transcript string
	events     []string
	runs       int
}

func viewOf(t *testing.T, store session.Store, id string) view {
	t.Helper()
	ctx := context.Background()
	record, err := session.View(ctx, store, id)
	if err != nil {
		t.Fatal(err)
	}
	events, err := session.StoredEvents(ctx, store, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := view{transcript: shape(record.Messages()), runs: len(record.Snapshot().Runs)}
	for _, event := range events {
		line := fmt.Sprintf("%d %s", event.Head().Number, event.Kind())
		switch e := event.(type) {
		case gotato.MessageCommitted:
			line += " " + gotato.TextOf(e.Message)
		case gotato.ReplyEnded:
			line += " " + gotato.TextOf(e.Message)
		case gotato.RunEnded:
			line += " " + string(e.Status)
		case gotato.ToolEnded:
			line += " " + string(e.Status)
		}
		out.events = append(out.events, line)
	}
	return out
}

// reference plays a task on one instance, directly, and returns its view.
func reference(t *testing.T, task testkit.Task) view {
	t.Helper()
	store := session.NewMemoryStore()
	runner := taskRunner(t, store, task, "single")
	ctx := context.Background()
	s, err := runner.CreateSession(ctx, task.Name, nil, session.WithMessages(task.History...))
	if err != nil {
		t.Fatal(err)
	}
	for _, prompt := range task.Prompts {
		if _, err := runner.Run(ctx, service.RunRequest{SessionID: s.ID(), Prompt: prompt}); err != nil {
			t.Fatal(err)
		}
	}
	return viewOf(t, store, s.ID())
}

// watcher follows a Session's stream on one instance, as a client does, and
// keeps the numbers and kinds of the stored Events it read.
type watcher struct {
	mu     sync.Mutex
	events []string
	cancel context.CancelFunc
	done   chan struct{}
}

func watch(t *testing.T, base, sessionID string) *watcher {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w := &watcher{cancel: cancel, done: make(chan struct{})}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/sessions/"+sessionID+"/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("stream = %d", resp.StatusCode)
	}
	go func() {
		defer close(w.done)
		defer func() { _ = resp.Body.Close() }()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 1<<20), 1<<24)
		var id, kind string
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				kind = strings.TrimPrefix(line, "event: ")
			case line == "":
				if id != "" {
					w.mu.Lock()
					w.events = append(w.events, id+" "+kind)
					w.mu.Unlock()
				}
				id, kind = "", ""
			}
		}
	}()
	t.Cleanup(cancel)
	return w
}

func (w *watcher) read() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.events)
}

// kindsOnly keeps "number kind" of an event line.
func kindsOnly(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		fields := strings.Fields(line)
		out[i] = fields[0] + " " + fields[1]
	}
	return out
}

// ---- the tests --------------------------------------------------------------

// sessionPlan is one Session of the harness: a task and its ID.
type sessionPlan struct {
	task testkit.Task
	id   string
}

// TestRandomInstancesGiveWhatOneInstanceGives is the Phase 5 exit test: the
// reference tasks are played on Sessions that are created through the proxy,
// and whose prompts are sent through the proxy, with the session-id header, or
// straight to a random instance, with Sessions running concurrently and an
// instance killed between two prompts. Each Session ends with the Transcript
// and the Events, in the same order and under the same numbers, that one
// instance gives for the same task. A client that follows each Session's stream
// on an instance that is not the writer reads the same Events.
func TestRandomInstancesGiveWhatOneInstanceGives(t *testing.T) {
	tasks := testkit.ReferenceTasks()
	for _, kill := range []bool{false, true} {
		t.Run(fmt.Sprintf("killed=%v", kill), func(t *testing.T) {
			c := newCluster(t, 3, tasks, 17)
			const perTask = 2
			var plans []sessionPlan
			for _, task := range tasks {
				for range perTask {
					plans = append(plans, sessionPlan{task: task, id: c.create(task)})
				}
			}
			// Streams on the instance that is never killed, which is the
			// writer of some of these Sessions and not of others.
			watchers := map[string]*watcher{}
			for _, p := range plans {
				watchers[p.id] = watch(t, c.instances[0].server.URL, p.id)
			}

			play := func(from, to func(task testkit.Task) int) {
				var wg sync.WaitGroup
				for _, p := range plans {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for i := from(p.task); i < to(p.task) && i < len(p.task.Prompts); i++ {
							r := c.prompt(p.id, p.task.Prompts[i])
							if r.status != http.StatusOK {
								t.Errorf("%s %s prompt %d = %d %s", p.task.Name, p.id, i, r.status, r.body)
								return
							}
						}
					}()
				}
				wg.Wait()
			}
			// The first prompt of every Session, all Sessions at once.
			play(func(testkit.Task) int { return 0 }, func(testkit.Task) int { return 1 })
			victim := c.instances[len(c.instances)-1]
			held := map[string]bool{}
			if kill {
				// The victim holds the Claim of every Session that has more
				// prompts to come: it has opened each as a Writer, as a Run
				// does, and never releases it. Then it dies, and the rest of
				// the prompts take each Session over from a dead holder.
				for _, p := range plans {
					if len(p.task.Prompts) < 2 {
						continue
					}
					if _, err := session.OpenWriter(context.Background(), victim.store, p.id, victim.name); err != nil {
						t.Fatal(err)
					}
					held[p.id] = true
				}
				if len(held) == 0 {
					t.Fatal("no Session has a second prompt to take over")
				}
				c.kill(victim)
			}
			// The rest, with the instance gone.
			play(func(testkit.Task) int { return 1 }, func(testkit.Task) int { return 1 << 20 })

			references := map[string]view{}
			for _, task := range tasks {
				references[task.Name] = reference(t, task)
			}
			for _, p := range plans {
				got := viewOf(t, c.backing, p.id)
				want := references[p.task.Name]
				if held[p.id] {
					got.events = withoutHeldClaim(t, got.events)
					assertTakenOver(t, c.backing, p.id, victim.name)
				}
				if got.transcript != want.transcript {
					t.Fatalf("%s %s: the Transcript differs from one instance's\n%s\nwant\n%s", p.task.Name, p.id, got.transcript, want.transcript)
				}
				if !slices.Equal(got.events, want.events) {
					t.Fatalf("%s %s: the Events differ from one instance's\n%s", p.task.Name, p.id, firstDifference(got.events, want.events))
				}
			}
			// The watchers, which read the Session streams on instance 0 only,
			// saw every stored Event once, in order.
			for _, p := range plans {
				want := kindsOnly(viewOf(t, c.backing, p.id).events)
				deadline := time.Now().Add(10 * time.Second)
				var got []string
				for time.Now().Before(deadline) {
					got = watchers[p.id].read()
					if len(got) >= len(want) {
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
				if !slices.Equal(got, want) {
					t.Fatalf("%s %s: a stream on one instance read\n%s", p.task.Name, p.id, firstDifference(got, want))
				}
			}
			t.Logf("%d Sessions, %d requests hashed on session-id, %d retried by the proxy", len(plans), c.proxy.hashed.Load(), c.proxy.retries.Load())
			if c.proxy.hashed.Load() == 0 {
				t.Fatal("no request went through the hash")
			}
		})
	}
}

// withoutHeldClaim removes the Claim that a dead instance held, which has no
// release after it, and numbers the Events after it as they would be without it.
func withoutHeldClaim(t *testing.T, events []string) []string {
	t.Helper()
	at := -1
	for i := 0; i+1 < len(events); i++ {
		if strings.Contains(events[i], " session_claimed") && strings.Contains(events[i+1], " session_claimed") {
			at = i
			break
		}
	}
	if at < 0 {
		t.Fatalf("no Claim without a release in %v", events)
	}
	out := slices.Clone(events[:at])
	for _, line := range events[at+1:] {
		number, rest, _ := strings.Cut(line, " ")
		var n int
		_, _ = fmt.Sscanf(number, "%d", &n)
		out = append(out, fmt.Sprintf("%d %s", n-1, rest))
	}
	return out
}

// assertTakenOver fails unless another instance claimed the Session from the
// dead holder.
func assertTakenOver(t *testing.T, store session.Store, id, dead string) {
	t.Helper()
	events, err := session.StoredEvents(context.Background(), store, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if claimed, ok := event.(gotato.SessionClaimed); ok && claimed.From == dead && claimed.Instance != dead {
			return
		}
	}
	t.Fatalf("Session %s: no instance claimed it from the dead %s", id, dead)
}

func firstDifference(got, want []string) string {
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			return fmt.Sprintf("at place %d:\n got  %s\n want %s", i+1, got[i], want[i])
		}
	}
	return fmt.Sprintf("one is a prefix of the other (%d and %d)", len(got), len(want))
}

// The proxy keeps a Session on one instance: requests for it with the header
// reach its writer, so none of them waits for a takeover, and a Session's
// writer is where the hash says.
func TestProxyKeepsASessionOnItsWriter(t *testing.T) {
	task := testkit.ConversationTask()
	c := newCluster(t, 3, []testkit.Task{task}, 3)
	id := c.create(task)
	for _, prompt := range task.Prompts[:4] {
		if r := send(t, c.front.URL, http.MethodPost, "/v1/sessions/"+id+"/runs", map[string]any{"prompt": prompt}, id); r.status != http.StatusOK {
			t.Fatalf("prompt = %d %s", r.status, r.body)
		}
	}
	record, err := session.View(context.Background(), c.backing, id)
	if err != nil {
		t.Fatal(err)
	}
	events, _ := session.StoredEvents(context.Background(), c.backing, id, 0)
	writers := map[string]bool{}
	for _, event := range events {
		if claimed, ok := event.(gotato.SessionClaimed); ok && claimed.Why != gotato.ClaimNewSession {
			writers[claimed.Instance] = true
		}
	}
	// The first Run is the Session's creation by a random instance; every
	// later Run was sent to the one instance that the id hashes to.
	if len(writers) != 1 || !writers[c.proxy.owner(id)] {
		t.Fatalf("Runs after the first were claimed by %v; the hash says %s", writers, c.proxy.owner(id))
	}
	if len(record.Messages()) < 8 {
		t.Fatalf("Transcript has %d Messages", len(record.Messages()))
	}
}

// Concurrent messages to one Session, sent through the proxy and straight to
// random instances, are never interleaved: the Transcript alternates a prompt
// and its reply, each prompt that was answered with 200 landed once, and a
// message that was refused is a busy 409, never a half-written Run.
func TestConcurrentMessagesToOneSessionAreNeverInterleaved(t *testing.T) {
	task := testkit.Task{Name: "echo", Model: testkit.EchoModel{}, Prompts: []string{"x"}}
	c := newCluster(t, 3, []testkit.Task{task}, 5)
	id := c.create(task)

	var wg sync.WaitGroup
	var mu sync.Mutex
	answered := map[string]bool{}
	statuses := map[int]int{}
	for worker := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := range 4 {
				text := fmt.Sprintf("w%d-%d", worker, k)
				r := c.prompt(id, text)
				mu.Lock()
				statuses[r.status]++
				if r.status == http.StatusOK {
					answered[text] = true
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	t.Logf("statuses: %v", statuses)
	for status := range statuses {
		if status != http.StatusOK && status != http.StatusConflict {
			t.Fatalf("a message got %d: only 200 and 409 are allowed", status)
		}
	}
	if len(answered) == 0 {
		t.Fatal("no message was answered")
	}
	record, err := session.View(context.Background(), c.backing, id)
	if err != nil {
		t.Fatal(err)
	}
	messages := record.Messages()
	landed := map[string]int{}
	for i := 0; i+1 < len(messages); i += 2 {
		prompt, replyText := gotato.TextOf(messages[i]), gotato.TextOf(messages[i+1])
		if messages[i].Role != gotato.RoleUser || messages[i+1].Role != gotato.RoleAssistant || replyText != "echo: "+prompt {
			t.Fatalf("Messages %d and %d are %q and %q: a Run was interleaved or cut", i, i+1, prompt, replyText)
		}
		landed[prompt]++
	}
	if len(messages)%2 != 0 {
		t.Fatalf("%d Messages: a prompt has no reply", len(messages))
	}
	for text, n := range landed {
		if n != 1 {
			t.Fatalf("prompt %q landed %d times", text, n)
		}
	}
	for text := range answered {
		if landed[text] != 1 {
			t.Fatalf("prompt %q was answered 200 and landed %d times", text, landed[text])
		}
	}
}

// An instance killed in the middle of the traffic, while it holds a Claim: the
// Run it was running is lost and its client gets no 200; every Run that did
// answer 200 landed once, followed by its reply; the Session the dead instance
// held is taken over by another instance; and the proxy drops the dead one from
// its ring. The kill waits for the held Run to start, so it does not depend on
// timing.
func TestKilledInstanceMidTrafficLosesOnlyTheStepInFlight(t *testing.T) {
	model := &holdModel{gate: make(chan struct{}), started: make(chan struct{}, 1)}
	task := testkit.Task{Name: "echo", Model: model, Prompts: []string{"x"}}
	c := newCluster(t, 3, []testkit.Task{task}, 11)
	t.Cleanup(func() {
		select {
		case <-model.gate:
		default:
			close(model.gate)
		}
	})
	victim := c.instances[2]
	// A Session that hashes to the victim: the proxy will send it there first.
	var target string
	var ids []string
	for range 60 {
		id := c.create(task)
		ids = append(ids, id)
		if target == "" && c.proxy.owner(id) == victim.name {
			target = id
		}
	}
	if target == "" {
		t.Fatal("no Session hashes to the instance that is killed")
	}
	// Traffic on every other Session, from the start.
	var wg sync.WaitGroup
	var mu sync.Mutex
	answered := map[string]map[string]bool{}
	for _, id := range ids {
		answered[id] = map[string]bool{}
	}
	for worker := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			random := rand.New(rand.NewPCG(uint64(worker), 1))
			for k := range 8 {
				id := ids[random.IntN(len(ids))]
				if id == target {
					continue
				}
				text := fmt.Sprintf("w%d-%d", worker, k)
				if r := c.prompt(id, text); r.status == http.StatusOK {
					mu.Lock()
					answered[id][text] = true
					mu.Unlock()
				}
			}
		}()
	}
	// The victim runs a prompt on the target Session and holds its Claim; the
	// kill comes when that Run has started, and not before.
	lost := make(chan reply, 1)
	go func() {
		lost <- send(t, victim.server.URL, http.MethodPost, "/v1/sessions/"+target+"/runs", map[string]any{"prompt": "hold"}, target)
	}()
	<-model.started
	c.kill(victim)
	if r := <-lost; r.status == http.StatusOK {
		t.Fatalf("the Run the victim was holding answered 200: %s", r.body)
	}
	// The Session is used again, through the proxy: it hashes to the dead
	// instance, whose connection is refused, so the proxy retries on another.
	// That instance waits for the dead holder's row to expire and takes the
	// Session over.
	for k := range 3 {
		text := fmt.Sprintf("after-%d", k)
		r := send(t, c.front.URL, http.MethodPost, "/v1/sessions/"+target+"/runs", map[string]any{"prompt": text}, target)
		if r.status != http.StatusOK {
			t.Fatalf("a prompt for the Session the dead instance held = %d %s", r.status, r.body)
		}
		answered[target][text] = true
	}
	wg.Wait()
	assertTakenOver(t, c.backing, target, victim.name)
	if c.proxy.backends[2].down.Load() != true {
		t.Fatal("the proxy still sends requests to the dead instance")
	}
	if c.proxy.owner(target) == victim.name {
		t.Fatal("the dead instance still owns the Session in the ring")
	}
	total := 0
	for id, prompts := range answered {
		view, err := session.View(context.Background(), c.backing, id)
		if err != nil {
			t.Fatal(err)
		}
		messages := view.Messages()
		landed := map[string]int{}
		for i, m := range messages {
			if m.Role != gotato.RoleUser {
				continue
			}
			text := gotato.TextOf(m)
			landed[text]++
			if prompts[text] && (i+1 >= len(messages) || gotato.TextOf(messages[i+1]) != "echo: "+text) {
				t.Fatalf("the reply to %q does not follow it", text)
			}
		}
		for text, n := range landed {
			if n > 1 {
				t.Fatalf("prompt %q landed %d times", text, n)
			}
		}
		for text := range prompts {
			if landed[text] != 1 {
				t.Fatalf("prompt %q answered 200 and landed %d times", text, landed[text])
			}
			total++
		}
	}
	if total < 3 {
		t.Fatal("too few Runs completed")
	}
	t.Logf("%d Runs answered 200, one instance killed holding a Claim, %d retried by the proxy", total, c.proxy.retries.Load())
}

// holdModel answers at once, except for the prompt "hold", which waits for its
// gate and says when it has started.
type holdModel struct {
	gate    chan struct{}
	started chan struct{}
}

func (m *holdModel) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	text := gotato.TextOf(request.Messages[len(request.Messages)-1])
	if text == "hold" {
		m.started <- struct{}{}
		select {
		case <-m.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return testkit.EchoModel{}.Stream(ctx, request)
}

// A full instance refuses a new Session with a 503 marked not run, and the
// proxy sends that request once to another instance, where it runs: the load
// balancer's part of the activation limit, end to end. The full instance
// activates nothing for the refused requests.
func TestProxyRetriesAFullInstancesRefusalElsewhere(t *testing.T) {
	model := &holdModel{gate: make(chan struct{}), started: make(chan struct{}, 1)}
	task := testkit.Task{Name: "echo", Model: model, Prompts: []string{"x"}}
	c := newCluster(t, 2, []testkit.Task{task}, 23, func(i int, cfg *service.Config) {
		if i == 0 {
			cfg.Admission.MaxActiveSessions = 1
		}
	})
	t.Cleanup(func() {
		select {
		case <-model.gate:
		default:
			close(model.gate)
		}
	})
	held := make(chan error, 1)
	go func() {
		_, err := c.instances[0].runner.Run(context.Background(), service.RunRequest{Agent: "echo", Prompt: "hold"})
		held <- err
	}()
	<-model.started

	for i := range 12 {
		r := send(t, c.front.URL, http.MethodPost, "/v1/runs", map[string]any{"agent": "echo", "prompt": fmt.Sprintf("p%d", i)}, "")
		if r.status != http.StatusOK {
			t.Fatalf("request %d = %d %s", i, r.status, r.body)
		}
	}
	if c.proxy.retries.Load() == 0 {
		t.Fatal("no request was refused by the full instance, so the retry was not exercised")
	}
	sessions, err := c.backing.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 13 {
		t.Fatalf("%d Sessions, want the held one and 12 more", len(sessions))
	}
	for _, summary := range sessions {
		loaded, err := c.backing.Load(context.Background(), summary.ID)
		if err != nil {
			t.Fatal(err)
		}
		record, err := session.FromEntries(loaded.Facts)
		if err != nil {
			t.Fatal(err)
		}
		holder := record.Claim().Instance
		busy := summary.Busy
		if busy && holder != c.instances[0].name || !busy && holder != c.instances[1].name {
			t.Fatalf("Session %s is held by %s (busy %v): the full instance activated a Session it refused", summary.ID, holder, busy)
		}
	}
	close(model.gate)
	if err := <-held; err != nil {
		t.Fatal(err)
	}
}
