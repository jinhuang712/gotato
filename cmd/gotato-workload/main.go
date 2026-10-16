// Command gotato-workload runs the reference workload that GOALS.md measures
// and prints its report as JSON on stdout.
//
// With no flags it plays every task with its fake model, as CI does. With
// -live it plays them against the provider in the gateway configuration: the
// live run. The live run calls a paid model, so it runs on demand, never in
// CI.
//
//	gotato-workload -tasks lookup,history_100 -plays 20 -concurrency 4
//	gotato-workload -live -gateway-config gateway.yaml -price 3,15,0.3,3.75
//
// -price is the model's price in US dollars per million tokens: input,
// output, cache read and cache write. Keep the model and its price fixed from
// one release to the next, so the numbers compare.
//
// Exit codes: 0 when the report shows no failure caused by Gotato or the store
// (G-G04) and no Turn whose Context differed from its inspection (G-G03); 1 on
// an error; 2 on a usage error; 3 when one of those goals failed.
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
	"strconv"
	"strings"

	"github.com/jinhuang712/gotato/gateway"
	"github.com/jinhuang712/gotato/service/workload"
	"github.com/jinhuang712/gotato/session"
	"github.com/jinhuang712/gotato/testkit"
)

const (
	exitOK         = 0
	exitError      = 1
	exitUsage      = 2
	exitGoalFailed = 3
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getenv)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	flags := flag.NewFlagSet("gotato-workload", flag.ContinueOnError)
	flags.SetOutput(stderr)
	live := flags.Bool("live", false, "play the tasks against the provider in the gateway configuration")
	gatewayConfig := flags.String("gateway-config", "", "gateway YAML for -live (default $GOTATO_GATEWAY_CONFIG or gateway.yaml)")
	priceFlag := flags.String("price", "", "the model's price in USD per million tokens: input,output,cache_read,cache_write")
	taskNames := flags.String("tasks", "", "comma-separated tasks to play (default all)")
	plays := flags.Int("plays", 1, "how many times each task is played")
	concurrency := flags.Int("concurrency", 1, "how many plays of a task run at once")
	storeDir := flags.String("store", "", "keep Sessions in a file store in this directory (default in memory)")
	skipContext := flags.Bool("skip-context-check", false, "do not compare each Turn's Context with an inspection")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "gotato-workload: unexpected argument %q\n", flags.Arg(0))
		return exitUsage
	}
	if *plays < 1 || *concurrency < 1 {
		fmt.Fprintln(stderr, "gotato-workload: -plays and -concurrency must be at least 1")
		return exitUsage
	}
	price, err := parsePrice(*priceFlag)
	if err != nil {
		fmt.Fprintln(stderr, "gotato-workload:", err)
		return exitUsage
	}
	tasks, err := selectTasks(*taskNames)
	if err != nil {
		fmt.Fprintln(stderr, "gotato-workload:", err)
		return exitUsage
	}

	cfg := workload.Config{Price: price, Plays: *plays, Concurrency: *concurrency, SkipContextCheck: *skipContext}
	if *live {
		path := *gatewayConfig
		if path == "" {
			path = getenv("GOTATO_GATEWAY_CONFIG")
		}
		if path == "" {
			path = "gateway.yaml"
		}
		config, err := gateway.LoadYAML(path)
		if err != nil {
			fmt.Fprintln(stderr, "gotato-workload: the live run needs a gateway configuration:", err)
			return exitError
		}
		client, err := gateway.New(config)
		if err != nil {
			fmt.Fprintln(stderr, "gotato-workload:", err)
			return exitError
		}
		cfg.Model, cfg.ModelName = client, config.Model
		if price == (workload.Price{}) {
			fmt.Fprintln(stderr, "gotato-workload: no -price, so the report has no cost")
		}
	}
	if *storeDir != "" {
		store, err := session.NewFileStore(*storeDir)
		if err != nil {
			fmt.Fprintln(stderr, "gotato-workload:", err)
			return exitError
		}
		cfg.Store = store
	}

	report, err := workload.Run(ctx, cfg, tasks)
	if err != nil {
		fmt.Fprintln(stderr, "gotato-workload:", err)
		return exitError
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintln(stderr, "gotato-workload:", err)
		return exitError
	}
	if report.GotatoFailures > 0 || report.ContextMismatches > 0 {
		fmt.Fprintf(stderr, "gotato-workload: %d failures caused by Gotato or the store, %d Turns whose Context differed from its inspection\n",
			report.GotatoFailures, report.ContextMismatches)
		return exitGoalFailed
	}
	return exitOK
}

// parsePrice reads "input,output,cache_read,cache_write"; the cache prices
// may be left out.
func parsePrice(value string) (workload.Price, error) {
	if strings.TrimSpace(value) == "" {
		return workload.Price{}, nil
	}
	fields := strings.Split(value, ",")
	if len(fields) < 2 || len(fields) > 4 {
		return workload.Price{}, fmt.Errorf("-price wants input,output[,cache_read[,cache_write]], got %q", value)
	}
	numbers := make([]float64, 4)
	for i, field := range fields {
		n, err := strconv.ParseFloat(strings.TrimSpace(field), 64)
		if err != nil || n < 0 {
			return workload.Price{}, fmt.Errorf("-price: %q is not a price", field)
		}
		numbers[i] = n
	}
	return workload.Price{Input: numbers[0], Output: numbers[1], CacheRead: numbers[2], CacheWrite: numbers[3]}, nil
}

// selectTasks picks reference tasks by name, in the order given.
func selectTasks(names string) ([]testkit.Task, error) {
	all := testkit.ReferenceTasks()
	if strings.TrimSpace(names) == "" {
		return all, nil
	}
	byName := map[string]testkit.Task{}
	known := make([]string, 0, len(all))
	for _, task := range all {
		byName[task.Name] = task
		known = append(known, task.Name)
	}
	var tasks []testkit.Task
	for _, name := range strings.Split(names, ",") {
		task, ok := byName[strings.TrimSpace(name)]
		if !ok {
			return nil, fmt.Errorf("unknown task %q (known: %s)", name, strings.Join(known, ", "))
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}
