package gateway

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// YAMLConfig is the on-disk configuration format for gotato-gateway.
// Environment variables may be referenced as ${NAME}; this is useful for
// keeping API keys out of the configuration file committed to source control.
type AuthConfig struct {
	// Type is "api_key" (the only supported scheme). The gateway is a
	// service-level adapter: it never performs an interactive login or
	// touches another program's credential files.
	Type string `yaml:"type"`
}

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
	data = []byte(os.ExpandEnv(string(data)))
	var fileConfig YAMLConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
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

func (c YAMLConfig) Config() (Config, error) {
	data, err := yaml.Marshal(c)
	if err != nil {
		return Config{}, err
	}
	return ParseYAML(data)
}
