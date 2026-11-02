// Package model is the bridge to a model over HTTP: it provides the Model Gotato calls a provider through, a Client
// built from four layers a team can replace, over one description of the
// model.
//
//   - the wire API, a WireAPI, writes a request and reads its reply:
//     ChatCompletions, Responses and AnthropicMessages are shipped, and a new
//     protocol is one type outside Gotato;
//   - the Connection says where to send it and as whom, with the timeouts
//     NewHTTPClient puts on the HTTP client and any other authentication in its
//     RoundTripper;
//   - Retry, set with WithRetry, says how often to send again;
//   - a Classifier, set with WithClassifier, says what a failure means:
//     DefaultClassifier holds this package's own tables.
//
// The model is a gotato.ModelConfig: its name, its facts (the window, the cache
// retention, how it takes reasoning) and its default sampling. Gotato keeps no
// table of providers or models and guesses nothing from a name or an address:
// what the model is, the team states.
//
//	client, err := model.New(model.AnthropicMessages{}, model.Connection{
//		Endpoint: "https://llm.example.com/v1/messages",
//		APIKey:   key,
//	}, gotato.ModelConfig{
//		Name:  "a-model",
//		Facts: gotato.ModelFacts{Window: gotato.WindowTokens(200_000)},
//	})
//
// The package builds on the provider-neutral gotato Model contract and
// net/http only. Request encoding, retries, SSE reading and the reading of
// provider errors live here, outside Core.
package model

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/gotato"
)

// Option is a setting of New beyond the wire, the Connection and the model:
// WithRetry or WithClassifier.
type Option func(*options)

type options struct {
	retry    Retry
	classify Classifier
}

// WithClassifier sets the Classifier, which says what every Failure means.
// Wrap DefaultClassifier to add to it. A nil Classifier is DefaultClassifier.
func WithClassifier(classifier Classifier) Option {
	return func(o *options) { o.classify = classifier }
}

// Client is a gotato.Model that streams from one endpoint over one wire API.
// It keeps no per-request state, so one Client serves many concurrent Runs.
type Client struct {
	wire       WireAPI
	conn       Connection
	httpClient *http.Client
	model      gotato.ModelConfig
	retry      Retry
	classify   Classifier
}

// New checks its arguments and returns a Client. It makes no request: a
// refusal here is a mistake in the settings, and every one is reported before
// the first call to Stream.
//
// The model's window must be stated, with gotato.WindowTokens(n) for n above 0
// or gotato.WindowUnknown(): a window left out is never an accident. Every
// other fact may be left out. A team that omits RoundReasoning sends the
// provider more reasoning than it takes, and one that omits CacheRetention
// gets the provider's default retention; a provider accepts both.
func New(wire WireAPI, conn Connection, model gotato.ModelConfig, opts ...Option) (*Client, error) {
	if wire == nil {
		return nil, fmt.Errorf("model: a WireAPI is required (use model.ChatCompletions{}, model.Responses{} or model.AnthropicMessages{})")
	}
	if validator, ok := wire.(interface{ Validate() error }); ok {
		if err := validator.Validate(); err != nil {
			return nil, err
		}
	}
	if err := checkEndpoint(conn.Endpoint); err != nil {
		return nil, err
	}
	if err := checkHeaders(conn.Headers); err != nil {
		return nil, fmt.Errorf("model: Headers: %w", err)
	}
	if conn.CacheKeyHeader != "" && !validHeaderName(conn.CacheKeyHeader) {
		return nil, fmt.Errorf("model: CacheKeyHeader %q is not a header name", conn.CacheKeyHeader)
	}
	if err := checkModel(model); err != nil {
		return nil, err
	}
	var set options
	set.retry = DefaultRetry
	for _, opt := range opts {
		if opt != nil {
			opt(&set)
		}
	}
	retry, err := set.retry.checked()
	if err != nil {
		return nil, err
	}
	if set.classify == nil {
		set.classify = DefaultClassifier
	}
	client := conn.HTTPClient
	if client == nil {
		client = NewHTTPClient(defaultConnectTimeout, defaultResponseHeaderTimeout)
	}
	conn.Headers = cloneHeaders(conn.Headers)
	if model.Sampling.Temperature != nil {
		temperature := *model.Sampling.Temperature
		model.Sampling.Temperature = &temperature
	}
	return &Client{wire: wire, conn: conn, httpClient: client, model: model, retry: retry, classify: set.classify}, nil
}

// checkModel refuses a model this package cannot call: no name, a window not
// stated or not usable, a negative retention, a temperature no provider takes.
func checkModel(model gotato.ModelConfig) error {
	if strings.TrimSpace(model.Name) == "" {
		return fmt.Errorf("model: ModelConfig.Name is required")
	}
	if !model.Facts.Window.Stated() {
		return fmt.Errorf("model: ModelConfig.Facts.Window is not stated: use gotato.WindowTokens(n) with n > 0, or gotato.WindowUnknown()")
	}
	if err := model.Facts.Window.Valid(); err != nil {
		return fmt.Errorf("model: ModelConfig.Facts.Window: %w", err)
	}
	if model.Facts.CacheRetention < 0 {
		return fmt.Errorf("model: ModelConfig.Facts.CacheRetention cannot be negative")
	}
	if t := model.Sampling.Temperature; t != nil && (!(*t >= 0) || math.IsInf(*t, 1)) {
		return fmt.Errorf("model: ModelConfig.Sampling.Temperature is not a finite number of at least 0")
	}
	return nil
}

// Config implements gotato.Model: the ModelConfig the Client was built with,
// which the runtime reads for the model's facts and default sampling.
func (c *Client) Config() gotato.ModelConfig {
	config := c.model
	if config.Sampling.Temperature != nil {
		temperature := *config.Sampling.Temperature
		config.Sampling.Temperature = &temperature
	}
	return config
}

// fail is the Error a Failure becomes under the Client's Classifier.
func (c *Client) fail(failure Failure) *Error { return newError(failure, c.classify(failure)) }

// Stream sends the request and returns the reply as it arrives. A refused
// request is an *Error; a stream cut after its headers is sent again within
// the Retry (see stream.go). Closing the stream, or canceling ctx, ends the
// connection.
func (c *Client) Stream(ctx context.Context, request gotato.ModelRequest) (gotato.ModelStream, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := checkHeaders(RequestHeaders(ctx)); err != nil {
		// Sending it again cannot help, so it is not a transport failure.
		return nil, &Error{Message: "a request header is not valid: " + err.Error()}
	}
	base := c.baseHeader(ctx, request.CacheKey)
	encoded, err := c.wire.Encode(Call{Model: c.Config(), Request: request, APIKey: c.conn.APIKey, CacheKey: request.CacheKey, Header: base.Clone()})
	if err != nil {
		return nil, err
	}
	if encoded == nil || encoded.NewDecoder == nil {
		return nil, fmt.Errorf("model: the wire API returned no request or no decoder")
	}
	if err := checkHeaders(singleValues(encoded.Header)); err != nil {
		return nil, &Error{Message: "a header of the wire API is not valid: " + err.Error()}
	}
	header := base
	header.Set("Content-Type", "application/json")
	header.Set("Accept", "text/event-stream")
	for name, values := range encoded.Header {
		header[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
	}
	open := func(ctx context.Context) (*http.Response, error) { return c.send(ctx, encoded.Body, header) }
	response, err := open(ctx)
	if err != nil {
		return nil, err
	}
	return newStream(c, response, encoded.NewDecoder, open), nil
}

// baseHeader is the headers the Connection and the context give a request: the
// default User-Agent, then the Connection's, then the context's, then the
// cache key. The wire's own headers come after, so they win.
func (c *Client) baseHeader(ctx context.Context, cacheKey string) http.Header {
	header := http.Header{}
	header.Set("User-Agent", defaultUserAgent)
	for name, value := range c.conn.Headers {
		header.Set(name, value)
	}
	for name, value := range RequestHeaders(ctx) {
		header.Set(name, value)
	}
	if c.conn.CacheKeyHeader != "" && cacheKey != "" {
		header.Set(c.conn.CacheKeyHeader, cacheKey)
	}
	return header
}

// singleValues flattens headers for checkHeaders, which reads one value per
// name.
func singleValues(header http.Header) map[string]string {
	out := make(map[string]string, len(header))
	for name, values := range header {
		out[name] = strings.Join(values, ", ")
	}
	return out
}

// withDefault returns value, or fallback when value is empty.
func withDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// retryWait is how long to wait before attempt+1: the Retry's backoff, which
// grows with the attempt, or the provider's Retry-After, whichever applies,
// never more than MaxWait.
func (c *Client) retryWait(header http.Header, attempt int) time.Duration {
	delay := withJitter(c.retry.Backoff * time.Duration(attempt+1))
	if retryAfter := retryAfterDuration(header); retryAfter >= 0 {
		delay = retryAfter
	}
	return min(delay, c.retry.MaxWait)
}
