// Package gatewayconf reads the gateway YAML file that Gotato's own command
// line tools take (gotato --gateway-config, gotato-workload -gateway-config).
// It is the one place a wire API's name maps to a gateway.WireAPI value: the
// gateway library keeps no names, no table of providers and no file format,
// so a service builds its Client in Go, and only these tools read a file.
//
// gateway.example.yaml, next to this file, is the commented file to copy.
package gatewayconf

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jinhuang712/gotato/gateway"
	"github.com/jinhuang712/gotato/gotato"
)

// The wire APIs a file can name, in the key `api`.
const (
	APIChatCompletions   = "openai-chat-completions"
	APIResponses         = "openai-responses"
	APIAnthropicMessages = "anthropic-messages"
)

// Config is what a gateway file stands for, in the arguments of gateway.New.
type Config struct {
	Wire       gateway.WireAPI
	Connection gateway.Connection
	Model      gotato.ModelConfig
	Options    []gateway.Option
}

// New builds the Client the file describes.
func (c Config) New() (*gateway.Client, error) {
	return gateway.New(c.Wire, c.Connection, c.Model, c.Options...)
}

// file is the on-disk format. Environment variables may be referenced as
// ${NAME}; this is useful for keeping API keys out of a file committed to
// source control.
type file struct {
	// API is the wire protocol: openai-chat-completions (the default),
	// openai-responses, or anthropic-messages.
	API      string            `yaml:"api"`
	Endpoint string            `yaml:"endpoint"`
	APIKey   string            `yaml:"api_key"`
	Headers  map[string]string `yaml:"headers"`

	// CacheKeyHeader is Connection's field of the same name; CacheKeyField,
	// AnthropicVersion and DeferredTools are the wire's own options.
	CacheKeyHeader   string               `yaml:"cache_key_header"`
	CacheKeyField    string               `yaml:"cache_key_field"`
	AnthropicVersion string               `yaml:"anthropic_version"`
	DeferredTools    gateway.DeferredForm `yaml:"deferred_tools"`

	// Model is the model's name, and the rest is what the model is: its
	// facts (Window is a number or "unknown", and must be stated) and its
	// default sampling.
	Model           string        `yaml:"model"`
	Window          gotato.Window `yaml:"window"`
	CacheRetention  string        `yaml:"cache_retention"`
	RoundReasoning  bool          `yaml:"round_reasoning"`
	ThinkingBinding bool          `yaml:"thinking_binding"`

	Temperature      *float64 `yaml:"temperature"`
	MaxTokens        uint32   `yaml:"max_tokens"`
	ReasoningEffort  string   `yaml:"reasoning_effort"`
	ReasoningSummary string   `yaml:"reasoning_summary"`

	// MaxRetries, RetryBackoff and MaxRetryWait are a Retry. A file that sets
	// none keeps gateway.DefaultRetry, and max_retries: 0 turns retries off.
	MaxRetries   *int   `yaml:"max_retries"`
	RetryBackoff string `yaml:"retry_backoff"`
	MaxRetryWait string `yaml:"max_retry_wait"`

	// ConnectTimeout and ResponseHeaderTimeout are the arguments of
	// gateway.NewHTTPClient, Go durations.
	ConnectTimeout        string `yaml:"connect_timeout"`
	ResponseHeaderTimeout string `yaml:"response_header_timeout"`

	// OverflowPhrases adds a team's own wording for "the request was too
	// long" to gateway.DefaultClassifier.
	OverflowPhrases []string `yaml:"overflow_phrases"`
}

// Load reads a gateway file and checks it with gateway.New. Write ${NAME} in a
// value to read it from the environment, and a key the format does not know is
// an error. gateway.example.yaml, next to this file, lists every key with its
// default; copy it as a starting point.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("gateway: read config %q: %w", path, err)
	}
	config, err := Parse(data)
	if err != nil {
		return Config{}, fmt.Errorf("gateway: parse config %q: %w", path, err)
	}
	return config, nil
}

// Parse reads a gateway file's bytes, as Load does.
func Parse(data []byte) (Config, error) {
	// Expand after decoding: a substituted value is then a plain scalar and
	// can never change the document structure, and only the documented
	// ${NAME} form is recognized.
	expanded := expandBraceEnvDocument(data)
	var in file
	decoder := yaml.NewDecoder(bytes.NewReader(expanded))
	decoder.KnownFields(true)
	if err := decoder.Decode(&in); err != nil {
		return Config{}, err
	}
	config, err := in.config()
	if err != nil {
		return Config{}, err
	}
	if _, err := config.New(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (in file) config() (Config, error) {
	wire, err := in.wire()
	if err != nil {
		return Config{}, err
	}
	config := Config{
		Wire: wire,
		Connection: gateway.Connection{
			Endpoint:       strings.TrimSpace(in.Endpoint),
			APIKey:         in.APIKey,
			CacheKeyHeader: strings.TrimSpace(in.CacheKeyHeader),
			Headers:        in.Headers,
		},
		Model: gotato.ModelConfig{
			Name: in.Model,
			Facts: gotato.ModelFacts{
				Window:          in.Window,
				RoundReasoning:  in.RoundReasoning,
				ThinkingBinding: in.ThinkingBinding,
			},
			Sampling: gotato.Sampling{
				Temperature:      in.Temperature,
				MaxTokens:        in.MaxTokens,
				ReasoningEffort:  in.ReasoningEffort,
				ReasoningSummary: in.ReasoningSummary,
			},
		},
	}
	var retention, backoff, maxWait, connect, header time.Duration
	for _, field := range []struct {
		key   string
		value string
		into  *time.Duration
	}{
		{"cache_retention", in.CacheRetention, &retention},
		{"retry_backoff", in.RetryBackoff, &backoff},
		{"max_retry_wait", in.MaxRetryWait, &maxWait},
		{"connect_timeout", in.ConnectTimeout, &connect},
		{"response_header_timeout", in.ResponseHeaderTimeout, &header},
	} {
		if field.value == "" {
			continue
		}
		duration, err := time.ParseDuration(strings.TrimSpace(field.value))
		if err != nil {
			return Config{}, fmt.Errorf("invalid %s %q: %w", field.key, field.value, err)
		}
		*field.into = duration
	}
	config.Model.Facts.CacheRetention = retention
	if in.MaxRetries != nil || backoff != 0 || maxWait != 0 {
		retry := gateway.DefaultRetry
		if in.MaxRetries != nil {
			retry.Max = *in.MaxRetries
		}
		if backoff != 0 {
			retry.Backoff = backoff
		}
		if maxWait != 0 {
			retry.MaxWait = maxWait
		}
		config.Options = append(config.Options, gateway.WithRetry(retry))
	}
	if connect != 0 || header != 0 {
		if connect < 0 || header < 0 {
			return Config{}, fmt.Errorf("connect_timeout and response_header_timeout cannot be negative")
		}
		config.Connection.HTTPClient = gateway.NewHTTPClient(withDefault(connect, 10*time.Second), withDefault(header, 60*time.Second))
	}
	if phrases := cleanPhrases(in.OverflowPhrases); len(phrases) > 0 {
		config.Options = append(config.Options, gateway.WithClassifier(withOverflowPhrases(gateway.DefaultClassifier, phrases)))
	}
	return config, nil
}

// wire maps the name of a wire API to its value, with the wire's own options.
func (in file) wire() (gateway.WireAPI, error) {
	switch strings.TrimSpace(in.API) {
	case "", APIChatCompletions:
		if in.AnthropicVersion != "" {
			return nil, fmt.Errorf("anthropic_version applies to %s only", APIAnthropicMessages)
		}
		if in.DeferredTools != "" && in.DeferredTools != gateway.DeferredFormList {
			return nil, fmt.Errorf("deferred_tools %q: %s has only the list form", in.DeferredTools, APIChatCompletions)
		}
		return gateway.ChatCompletions{CacheKeyField: in.CacheKeyField}, nil
	case APIResponses:
		if in.AnthropicVersion != "" {
			return nil, fmt.Errorf("anthropic_version applies to %s only", APIAnthropicMessages)
		}
		return gateway.Responses{CacheKeyField: in.CacheKeyField, Deferred: in.DeferredTools}, nil
	case APIAnthropicMessages:
		if in.CacheKeyField != "" {
			return nil, fmt.Errorf("cache_key_field is not supported by %s, which caches by cache_control", APIAnthropicMessages)
		}
		return gateway.AnthropicMessages{Version: in.AnthropicVersion, Deferred: in.DeferredTools}, nil
	}
	return nil, fmt.Errorf("unsupported api %q (use %s, %s, or %s)", in.API, APIChatCompletions, APIResponses, APIAnthropicMessages)
}

func withDefault(value, fallback time.Duration) time.Duration {
	if value == 0 {
		return fallback
	}
	return value
}

// cleanPhrases trims and lower-cases the phrases and drops an empty one.
func cleanPhrases(phrases []string) []string {
	var out []string
	for _, phrase := range phrases {
		if phrase = strings.ToLower(strings.TrimSpace(phrase)); phrase != "" {
			out = append(out, phrase)
		}
	}
	return out
}

// withOverflowPhrases wraps a Classifier so that a refusal whose message, code
// or type holds one of the phrases, matched without regard to case, is an
// overflow too: a 400 or 413, or an error told in a 2xx response.
func withOverflowPhrases(base gateway.Classifier, phrases []string) gateway.Classifier {
	return func(f gateway.Failure) gateway.Verdict {
		v := base(f)
		if v.Overflow || (f.Status != 0 && f.Status != 400 && f.Status != 413) {
			return v
		}
		for _, text := range []string{f.Message, f.Code, f.Type} {
			lower := strings.ToLower(text)
			for _, phrase := range phrases {
				if strings.Contains(lower, phrase) {
					// The limit the message names, if its wording is one the default reads.
					limit := gateway.DefaultClassifier(gateway.Failure{Status: 400, Message: f.Message, Type: "context_length_exceeded"}).Limit
					return gateway.Verdict{Overflow: true, Limit: limit}
				}
			}
		}
		return v
	}
}

// expandBraceEnvDocument resolves ${NAME} references in every string scalar of
// a YAML document and leaves every other byte, including a bare $ and $NAME,
// literal.
//
// Expansion happens on the decoded document rather than on the raw text, so an
// environment value that contains YAML metacharacters stays a single string and
// cannot add keys, change nesting, or retag a scalar. Only scalars are visited:
// keys, comments, and anchors keep whatever they contained.
//
// A document that does not parse is returned as it is: the decoder that
// follows reports the error.
func expandBraceEnvDocument(data []byte) []byte {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return data
	}
	expandEnvScalars(&document)
	expanded, err := yaml.Marshal(&document)
	if err != nil {
		return data
	}
	return expanded
}

func expandEnvScalars(node *yaml.Node) {
	switch node.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range node.Content {
			expandEnvScalars(child)
		}
	case yaml.MappingNode:
		for index, child := range node.Content {
			// Odd indices are values; keys (even) stay literal.
			if index%2 == 1 {
				expandEnvScalars(child)
			}
		}
	case yaml.ScalarNode:
		if node.Tag == "!!str" && strings.Contains(node.Value, "${") {
			node.Value = expandBraceEnv(node.Value)
		}
	}
}

// expandBraceEnv replaces ${NAME} with the value of environment variable NAME.
// Unlike os.ExpandEnv it does not expand a bare $NAME or a lone $, so a literal
// $ in an API key or header value survives.
func expandBraceEnv(value string) string {
	if !strings.Contains(value, "${") {
		return value
	}
	var b strings.Builder
	b.Grow(len(value))
	for i := 0; i < len(value); {
		if value[i] == '$' && i+1 < len(value) && value[i+1] == '{' {
			if end := strings.IndexByte(value[i+2:], '}'); end >= 0 {
				b.WriteString(os.Getenv(value[i+2 : i+2+end]))
				i += 2 + end + 1
				continue
			}
		}
		b.WriteByte(value[i])
		i++
	}
	return b.String()
}
