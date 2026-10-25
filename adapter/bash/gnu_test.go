package bash_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestAgainstGNU runs the conformance table against installed GNU tools and
// checks that the expectations in the table are what GNU gives. It is for
// development and CI of this module only: it runs only when GOTATO_GNU=1, and
// the engine never starts a program.
//
// The tools are found as <prefix>cat, <prefix>head and <prefix>grep, where the
// prefix is GOTATO_GNU_PREFIX ("g" on macOS, where Homebrew installs GNU
// coreutils and grep under that name, and empty elsewhere). Set LC_ALL for the
// locale you want to check; the table assumes UTF-8.
func TestAgainstGNU(t *testing.T) {
	if os.Getenv("GOTATO_GNU") != "1" {
		t.Skip("set GOTATO_GNU=1 to compare with installed GNU tools")
	}
	prefix, ok := os.LookupEnv("GOTATO_GNU_PREFIX")
	if !ok && runtime.GOOS == "darwin" {
		prefix = "g"
	}
	bin := t.TempDir()
	for _, tool := range []string{"cat", "head", "grep"} {
		path, err := exec.LookPath(prefix + tool)
		if err != nil {
			t.Fatalf("GNU %s not found as %s%s: %v", tool, prefix, tool, err)
		}
		if err := os.Symlink(path, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	files := conformanceFiles()
	files["results/uni"] = "é\nab\n"
	for name, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Cases whose text GNU words differently, on purpose: ours uses ASCII
	// quotes where GNU uses the locale's, and RE2 words its own errors.
	differs := map[string]string{
		"head bad number": "GNU quotes with the locale's quotation marks",
	}
	for _, c := range conformanceCases {
		t.Run(c.name, func(t *testing.T) {
			if why, skip := differs[c.name]; skip {
				t.Skip(why)
			}
			cmd := exec.Command("/bin/bash", "-c", strings.ReplaceAll(c.cmd, "/results", root+"/results"))
			cmd.Dir = root
			cmd.Env = []string{"PATH=" + bin, "LC_ALL=" + envOr("LC_ALL", "en_US.UTF-8"), "HOME=/"}
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			code := 0
			if err := cmd.Run(); err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				code = exit.ExitCode()
			}
			gotOut := strings.ReplaceAll(stdout.String(), root, "")
			gotErr := strings.ReplaceAll(stderr.String(), root, "")
			if gotOut != c.stdout || gotErr != c.stderr || code != c.code {
				t.Errorf("%s\n GNU: stdout %q stderr %q exit %d\ntable: stdout %q stderr %q exit %d", c.cmd, gotOut, gotErr, code, c.stdout, c.stderr, c.code)
			}
		})
	}
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
