package gateway

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// yAMLConfig is the on-disk configuration format for gotato-gateway.
// Environment variables may be referenced as ${NAME}; this is useful for
// keeping API keys out of the configuration file committed to source control.
type yAMLConfig struct {
	// API selects the wire protocol: openai-chat-completions (default),
	// openai-responses, or anthropic-messages.
	API      API               `yaml:"api"`
	Endpoint string            `yaml:"endpoint"`
	BaseURL  string            `yaml:"base_url"`
	APIKey   string            `yaml:"api_key"`
	Model    string            `yaml:"model"`
	Headers  map[string]string `yaml:"headers"`
	// CacheKeyHeader and CacheKeyField say how each request's cache key is
	// sent (see Config).
	CacheKeyHeader string `yaml:"cache_key_header"`
	CacheKeyField  string `yaml:"cache_key_field"`
	MaxRetries     int    `yaml:"max_retries"`
	NoRetries      bool   `yaml:"no_retries"`
	RetryBackoff   string `yaml:"retry_backoff"`
	// Provider, CacheRetention (a duration such as 1h) and NoDropBlock are
	// Config's fields of the same names.
	Provider       Provider `yaml:"provider"`
	CacheRetention string   `yaml:"cache_retention"`
	NoDropBlock    bool     `yaml:"no_drop_block"`
	// ContextWindowTokens is the model's context window in tokens (Config's field
	// of the same name).
	ContextWindowTokens int `yaml:"context_window_tokens"`
	// OverflowPhrases adds the team's own wording for "the request was too
	// long" to the defaults (Config's field of the same name).
	OverflowPhrases []string `yaml:"overflow_phrases"`
	// DeferredTools is "native" or "list" (Config's field of the same name).
	DeferredTools DeferredForm `yaml:"deferred_tools"`
}

func LoadYAML(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("gateway: read config %q: %w", path, err)
	}
	config, err := parseYAML(data)
	if err != nil {
		return Config{}, fmt.Errorf("gateway: parse config %q: %w", path, err)
	}
	return config, nil
}

func parseYAML(data []byte) (Config, error) {
	// Expand after decoding: a substituted value is then a plain scalar and
	// can never change the document structure, and only the documented
	// ${NAME} form is recognized.
	expanded, err := expandBraceEnvDocument(data)
	if err != nil {
		return Config{}, err
	}
	var fileConfig yAMLConfig
	decoder := yaml.NewDecoder(bytes.NewReader(expanded))
	decoder.KnownFields(true)
	if err := decoder.Decode(&fileConfig); err != nil {
		return Config{}, err
	}
	config := Config{
		API:                 fileConfig.API,
		Endpoint:            fileConfig.Endpoint,
		BaseURL:             fileConfig.BaseURL,
		APIKey:              fileConfig.APIKey,
		Model:               fileConfig.Model,
		Headers:             fileConfig.Headers,
		CacheKeyHeader:      fileConfig.CacheKeyHeader,
		CacheKeyField:       fileConfig.CacheKeyField,
		MaxRetries:          fileConfig.MaxRetries,
		NoRetries:           fileConfig.NoRetries,
		Provider:            fileConfig.Provider,
		NoDropBlock:         fileConfig.NoDropBlock,
		ContextWindowTokens: fileConfig.ContextWindowTokens,

		OverflowPhrases: fileConfig.OverflowPhrases,
		DeferredTools:   fileConfig.DeferredTools,
	}
	if fileConfig.CacheRetention != "" {
		retention, err := time.ParseDuration(strings.TrimSpace(fileConfig.CacheRetention))
		if err != nil {
			return Config{}, fmt.Errorf("invalid cache_retention %q: %w", fileConfig.CacheRetention, err)
		}
		config.CacheRetention = retention
	}
	if fileConfig.RetryBackoff != "" {
		backoff, err := time.ParseDuration(strings.TrimSpace(fileConfig.RetryBackoff))
		if err != nil {
			return Config{}, fmt.Errorf("invalid retry_backoff %q: %w", fileConfig.RetryBackoff, err)
		}
		config.RetryBackoff = backoff
	}
	if _, err := New(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

// expandBraceEnvDocument resolves ${NAME} references in every string scalar of
// a YAML document and leaves every other byte, including a bare $ and $NAME,
// literal.
//
// Expansion happens on the decoded document rather than on the raw text, so an
// environment value that contains YAML metacharacters stays a single string and
// cannot add keys, change nesting, or retag a scalar. Only scalars are visited:
// keys, comments, and anchors keep whatever they contained.
func expandBraceEnvDocument(data []byte) ([]byte, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		// Return the original text: the caller reports the parse error.
		return data, nil
	}
	expandEnvScalars(&document)
	expanded, err := yaml.Marshal(&document)
	if err != nil {
		return data, nil
	}
	return expanded, nil
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

func (c yAMLConfig) Config() (Config, error) {
	data, err := yaml.Marshal(c)
	if err != nil {
		return Config{}, err
	}
	return parseYAML(data)
}
