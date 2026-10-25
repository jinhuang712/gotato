// Command gotato-workload-bash plays the bash_lookup reference task and prints
// its report as JSON on stdout, in the form of gotato-workload.
//
// The task needs the bash tool, which links the engine of adapter/bash, so it
// cannot be part of the root module's gotato-workload. This command is the
// same driver with the tool installed:
//
//	cd adapter/bash && go run ./cmd/gotato-workload-bash -plays 20 -concurrency 4
//
// Exit codes: 0 when the task passed in every play, with no failure caused by
// Gotato or the store and no Turn whose Context differed from its inspection;
// 1 on an error; 2 on a usage error; 3 when one of those goals failed.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/jinhuang712/gotato/adapter/bash/bashtool"
	"github.com/jinhuang712/gotato/service/workload"
	"github.com/jinhuang712/gotato/testkit"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("gotato-workload-bash", flag.ContinueOnError)
	flags.SetOutput(stderr)
	plays := flags.Int("plays", 1, "how many times the task is played")
	concurrency := flags.Int("concurrency", 1, "how many plays run at once")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 || *plays < 1 || *concurrency < 1 {
		fmt.Fprintln(stderr, "gotato-workload-bash: no arguments; -plays and -concurrency are at least 1")
		return 2
	}
	report, err := workload.Run(ctx, workload.Config{Plays: *plays, Concurrency: *concurrency},
		[]testkit.Task{testkit.BashLookupTask(bashtool.Tools())})
	if err != nil {
		fmt.Fprintln(stderr, "gotato-workload-bash:", err)
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintln(stderr, "gotato-workload-bash:", err)
		return 1
	}
	if report.GotatoFailures > 0 || report.ContextMismatches > 0 || report.Tasks[0].Passed != report.Tasks[0].Plays {
		fmt.Fprintln(stderr, "gotato-workload-bash: the task failed")
		return 3
	}
	return 0
}
