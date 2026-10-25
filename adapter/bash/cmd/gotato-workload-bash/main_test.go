package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestRunPlaysTheTask(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-plays", "2"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var report struct {
		Tasks []struct {
			Task   string `json:"task"`
			Passed int    `json:"passed"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Tasks) != 1 || report.Tasks[0].Task != "bash_lookup" || report.Tasks[0].Passed != 2 {
		t.Fatalf("%s", stdout.String())
	}
}

func TestRunRejectsBadFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-plays", "0"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit %d", code)
	}
}
