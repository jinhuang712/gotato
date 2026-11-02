package gateway

import (
	"fmt"
	"net/http"
	"sort"
	"time"
)

// Window is a model's context window as a team states it: a number of tokens,
// or "unknown" written out. The zero Window states nothing and is refused when
// a Client is built from a ModelProfile, so leaving the window out is never an
// accident.
type Window struct {
	tokens  int
	stated  bool
	unknown bool
}

// WindowTokens states a window of n tokens. n must be above 0; any other value
// is refused when the Client is built.
func WindowTokens(n int) Window { return Window{tokens: n, stated: true} }

// WindowUnknown states that the window is not known. Compaction then has no
// ceiling and runs only after the provider refuses a request as too long.
func WindowUnknown() Window { return Window{stated: true, unknown: true} }

// Tokens returns the window in tokens, or 0 when it is unknown or not stated.
func (w Window) Tokens() int { return w.tokens }

// ModelProfile says what a model is, apart from the connection it is reached
// by. The team supplies it; the gateway keeps no table of models.
type ModelProfile struct {
	// Name is the model id sent to the provider. Required.
	Name string
	// Window is the context window. It must be stated: WindowTokens(n) or
	// WindowUnknown().
	Window Window
	// Provider, CacheRetention, NoDropBlock, OverflowPhrases and
	// DeferredTools are the fields of Config with the same names.
	Provider        Provider
	CacheRetention  time.Duration
	NoDropBlock     bool
	OverflowPhrases []string
	DeferredTools   DeferredForm
}

// Connection is how to reach a model: the fields of Config that are not about
// the model. Profile may name a ModelProfile in a ProfileSet.
type Connection struct {
	API                   API
	Endpoint              string
	BaseURL               string
	APIKey                string
	HTTPClient            *http.Client
	Headers               map[string]string
	CacheKeyHeader        string
	CacheKeyField         string
	MaxRetries            int
	RetryBackoff          time.Duration
	MaxRetryWait          time.Duration
	ConnectTimeout        time.Duration
	ResponseHeaderTimeout time.Duration
	// Profile is the name of a profile in a ProfileSet; NewWithProfile
	// ignores it.
	Profile string
}

// NewWithProfile builds a Client from a connection and a profile. It checks
// the same things New does, and also that the profile states its window.
func NewWithProfile(connection Connection, profile ModelProfile) (*Client, error) {
	if !profile.Window.stated {
		return nil, fmt.Errorf("gateway: ModelProfile.Window is not stated: use gateway.WindowTokens(n) with n > 0, or gateway.WindowUnknown()")
	}
	if !profile.Window.unknown && profile.Window.tokens <= 0 {
		return nil, fmt.Errorf("gateway: ModelProfile.Window: WindowTokens needs n > 0, got %d (use gateway.WindowUnknown() for an unknown window)", profile.Window.tokens)
	}
	return New(Config{
		API:                   connection.API,
		Endpoint:              connection.Endpoint,
		BaseURL:               connection.BaseURL,
		APIKey:                connection.APIKey,
		HTTPClient:            connection.HTTPClient,
		Headers:               connection.Headers,
		CacheKeyHeader:        connection.CacheKeyHeader,
		CacheKeyField:         connection.CacheKeyField,
		MaxRetries:            connection.MaxRetries,
		RetryBackoff:          connection.RetryBackoff,
		MaxRetryWait:          connection.MaxRetryWait,
		ConnectTimeout:        connection.ConnectTimeout,
		ResponseHeaderTimeout: connection.ResponseHeaderTimeout,
		Model:                 profile.Name,
		ContextWindowTokens:   profile.Window.tokens,
		Provider:              profile.Provider,
		CacheRetention:        profile.CacheRetention,
		NoDropBlock:           profile.NoDropBlock,
		OverflowPhrases:       profile.OverflowPhrases,
		DeferredTools:         profile.DeferredTools,
	})
}

// ProfileSet keeps a team's profiles in one place, by name.
type ProfileSet map[string]ModelProfile

// Get returns the profile named ref, or an error that lists the names it has.
func (s ProfileSet) Get(ref string) (ModelProfile, error) {
	if profile, ok := s[ref]; ok {
		return profile, nil
	}
	names := make([]string, 0, len(s))
	for name := range s {
		names = append(names, name)
	}
	sort.Strings(names)
	return ModelProfile{}, fmt.Errorf("gateway: no model profile named %q (have %v)", ref, names)
}

// New builds a Client from a connection whose Profile names a profile in the
// set.
func (s ProfileSet) New(connection Connection) (*Client, error) {
	profile, err := s.Get(connection.Profile)
	if err != nil {
		return nil, err
	}
	return NewWithProfile(connection, profile)
}
