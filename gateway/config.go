package gateway

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// AuthConfig is the authentication section of the on-disk configuration.
type AuthConfig struct {
	// Type is "api_key" (the only supported scheme). The gateway is a
	// service-level adapter: it never performs an interactive login or
	// touches another program's credential files.
	Type string `yaml:"type"`
}

// YAMLConfig is the on-disk configuration format for gotato-gateway.
// Environment variables may be referenced as ${NAME}; this is useful for
// keeping API keys out of the configuration file committed to source control.
type YAMLConfig struct {
	// API selects the wire protocol: openai-chat-completions (default) or
	// openai-responses.
	API          string            `yaml:"api"`
	Endpoint     string            `yaml:"endpoint"`
	BaseURL      string            `yaml:"base_url"`
	APIKey       string            `yaml:"api_key"`
	Model        string            `yaml:"model"`
	Auth         *AuthConfig       `yaml:"auth"`
	Headers      map[string]string `yaml:"headers"`
	MaxRetries   int               `yaml:"max_retries"`
	NoRetries    bool              `yaml:"no_retries"`
	RetryBackoff string            `yaml:"retry_backoff"`
}

func LoadYAML(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("gateway: read config %q: %w", path, err)
	}
	config, err := ParseYAML(data)
	if err != nil {
		return Config{}, fmt.Errorf("gateway: parse config %q: %w", path, err)
	}
	return config, nil
}

func ParseYAML(data []byte) (Config, error) {
	// Expand after decoding: a substituted value is then a plain scalar and
	// can never change the document structure, and only the documented
	// ${NAME} form is recognized.
	expanded, err := expandBraceEnvDocument(data)
	if err != nil {
		return Config{}, err
	}
	var fileConfig YAMLConfig
	decoder := yaml.NewDecoder(bytes.NewReader(expanded))
	decoder.KnownFields(true)
	if err := decoder.Decode(&fileConfig); err != nil {
		return Config{}, err
	}
	config := Config{
		API:        fileConfig.API,
		Endpoint:   fileConfig.Endpoint,
		BaseURL:    fileConfig.BaseURL,
		APIKey:     fileConfig.APIKey,
		Model:      fileConfig.Model,
		Headers:    fileConfig.Headers,
		MaxRetries: fileConfig.MaxRetries,
		NoRetries:  fileConfig.NoRetries,
	}
	if fileConfig.Auth != nil {
		config.Auth = *fileConfig.Auth
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

func (c YAMLConfig) Config() (Config, error) {
	data, err := yaml.Marshal(c)
	if err != nil {
		return Config{}, err
	}
	return ParseYAML(data)
}
