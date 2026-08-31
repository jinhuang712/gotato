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
	// Type is currently "api_key" or "pi_oauth". For pi_oauth, File points
	// to a Pi auth.json and Provider defaults to openai-codex.
	Type      string `yaml:"type"`
	File      string `yaml:"file"`
	Provider  string `yaml:"provider"`
	AccountID string `yaml:"account_id"`
}

type YAMLConfig struct {
	// API selects the wire protocol. Empty keeps the legacy
	// openai-completions behavior.
	API          string            `yaml:"api"`
	Endpoint     string            `yaml:"endpoint"`
	BaseURL      string            `yaml:"base_url"`
	APIKey       string            `yaml:"api_key"`
	Model        string            `yaml:"model"`
	Auth         *AuthConfig       `yaml:"auth"`
	Headers      map[string]string `yaml:"headers"`
	MaxRetries   int               `yaml:"max_retries"`
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
