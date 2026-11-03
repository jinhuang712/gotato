package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadModelReadsTheExampleFile(t *testing.T) {
	t.Setenv("GOTATO_GATEWAY_API_KEY", "secret")
	_, cfg, err := loadModel("gateway.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "your-model" {
		t.Fatalf("model = %q", cfg.Name)
	}
}

func TestLoadModelRefusesAnUnknownKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.yaml")
	if err := os.WriteFile(path, []byte("endpoint: http://x/v1/messages\nmodel: m\nwindow: unknown\nnonsense: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadModel(path); err == nil || !strings.Contains(err.Error(), "nonsense") {
		t.Fatalf("err = %v", err)
	}
}
