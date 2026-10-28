package workload_test

import (
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

	"github.com/jinhuang712/gotato/internal/reftasks"
	"github.com/jinhuang712/gotato/service"
	"github.com/jinhuang712/gotato/service/httpapi"
	"github.com/jinhuang712/gotato/session"
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

func (s *killStore) Create(ctx context.Context, id string, entries []session.Entry, summary session.RecordInfo) error {
	if err := s.alive(); err != nil {
		return err
	}
	return s.Store.Create(ctx, id, entries, summary)
}

func (s *killStore) Append(ctx context.Context, id string, after uint64, entries []session.Entry, summary session.RecordInfo) error {
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

func (s *killStore) Log(ctx context.Context, id string, after uint64, limit int) ([]session.Entry, error) {
	if err := s.alive(); err != nil {
		return nil, err
	}
	return s.Store.Log(ctx, id, after, limit)
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

// clusterLiveness keeps every instance's row alive through a late renewal.
// victimLiveness is for the instance a test kills, whose row must expire for
// another instance to take its Sessions over.
var (
	clusterLiveness = service.Liveness{TTL: 5 * time.Second, Wait: 5 * time.Second}
	victimLiveness  = service.Liveness{TTL: 300 * time.Millisecond, Wait: 5 * time.Second}
)

// lastIsVictim gives the last of n instances the short Liveness TTL.
func lastIsVictim(n int) func(i int, cfg *service.Config) {
	return func(i int, cfg *service.Config) {
		if i == n-1 {
			cfg.Liveness = victimLiveness
		}
	}
}

// newCluster starts n instances over one new store, each serving every task as
// an Agent, and the hashing proxy in front of them.
func newCluster(t *testing.T, n int, tasks []reftasks.Task, seed uint64, adjust ...func(i int, cfg *service.Config)) *cluster {
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
			Admission: service.Admission{Queue: service.QueuePolicyWait},
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
	// Stop accepting first, so a request that dials now is refused, as the
	// proxy retries, and not accepted and then reset.
	_ = in.server.Listener.Close()
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

// proxyTransport opens a connection for every request. A pooled connection to
// an instance that was just killed looks alive until the next write fails, and
// that failure comes after the request may have been sent, so it is not
// retried; a new connection is refused instead, which the proxy retries.
var proxyTransport = &http.Transport{DisableKeepAlives: true}

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
		resp, err := proxyTransport.RoundTrip(out)
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
func (c *cluster) create(task reftasks.Task) string {
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
