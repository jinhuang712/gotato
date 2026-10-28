package main

import (
	"bufio"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// buildBinary builds the service stripped, the way G-B12 measures it.
func buildBinary(tb testing.TB) string {
	tb.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		tb.Skip("go is not on PATH")
	}
	bin := filepath.Join(tb.TempDir(), "refservice")
	out, err := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", bin, ".").CombinedOutput()
	if err != nil {
		tb.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// startOnce starts the binary and returns the time from just before Start to
// the first 200 from /healthz, then stops the process.
func startOnce(tb testing.TB, bin string) time.Duration {
	tb.Helper()
	cmd := exec.Command(bin)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		tb.Fatal(err)
	}
	begin := time.Now()
	if err := cmd.Start(); err != nil {
		tb.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGINT)
		_ = cmd.Wait()
	}()

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		tb.Fatalf("reading listening line: %v", err)
	}
	addr, ok := strings.CutPrefix(strings.TrimSpace(line), "listening ")
	if !ok {
		tb.Fatalf("unexpected first line %q", line)
	}
	url := fmt.Sprintf("http://%s/healthz", addr)
	deadline := begin.Add(30 * time.Second)
	for {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return time.Since(begin)
			}
		}
		if time.Now().After(deadline) {
			tb.Fatalf("healthz not ready: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestStartupServesHealthz(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and starts the binary")
	}
	d := startOnce(t, buildBinary(t))
	t.Logf("ready in %v", d)
}

// BenchmarkStartup measures G-B10: from process start to ready to serve.
func BenchmarkStartup(b *testing.B) {
	bin := buildBinary(b)
	b.ResetTimer()
	var total time.Duration
	for i := 0; i < b.N; i++ {
		total += startOnce(b, bin)
	}
	b.StopTimer()
	b.ReportMetric(float64(total.Microseconds())/1000/float64(b.N), "ms/start")
}
