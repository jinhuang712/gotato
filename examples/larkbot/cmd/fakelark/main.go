// Command fakelark plays Lark for the bot, so it can run without a connection
// to Lark: in tests and in a local cluster. See the README.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jinhuang712/gotato/examples/larkbot/internal/fakelark"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9000", "listen address")
	bots := flag.String("bots", "", "comma-separated bot base URLs, e.g. http://127.0.0.1:8080,http://127.0.0.1:8081")
	dns := flag.String("bots-dns", "", "name:port resolved at every delivery (a headless service)")
	redeliver := flag.Bool("redeliver", true, "redeliver an event whose ack is late (3 s) or not 2xx, up to 3 times")
	seed := flag.Int64("seed", 0, "seed of the endpoint choice (0: random)")
	fromClock := flag.Bool("ids-from-clock", false, "count message IDs from the clock, so a restart on a kept store never reuses a Session ID")
	flag.Parse()
	if *bots == "" && *dns == "" {
		fmt.Fprintln(os.Stderr, "fakelark: set -bots or -bots-dns")
		os.Exit(2)
	}
	cfg := fakelark.Config{BotsDNS: *dns, Redeliver: *redeliver, Seed: *seed}
	if *fromClock {
		cfg.SeqStart = int(time.Now().Unix()) * 1000
	}
	for b := range strings.SplitSeq(*bots, ",") {
		if b = strings.TrimRight(strings.TrimSpace(b), "/"); b != "" {
			cfg.Bots = append(cfg.Bots, b)
		}
	}
	srv := &http.Server{Addr: *addr, Handler: fakelark.New(cfg).Handler(), ReadHeaderTimeout: 5 * time.Second}
	log.Printf("fakelark listening on %s", *addr)
	log.Fatal(srv.ListenAndServe())
}
