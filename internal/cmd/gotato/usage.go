package main

import (
	"fmt"
	"io"
	"strings"
)

func (c *cli) usage() { c.usageTo(c.stdout) }

// usageTo writes the human usage text to w. stdout for an explicit help
// request, stderr for a usage mistake.
func (c *cli) usageTo(w io.Writer) {
	fmt.Fprint(w, strings.TrimLeft(`
gotato — minimalistic, composable Go agent runtime

Usage:
  gotato [global flags] <command> [flags] [args]

Commands:
  run        [--session ID] [--model echo|demo|gateway] [--compact-ceiling N] [--json|--events jsonl] "prompt"
  session    create | list | show <id> | fork <id> | events <id> | resume <id> "prompt" | message <id> [--kind steer|follow_up|interrupt] "text" | notice <id> --kind K "text" | abort <id> | delete <id>
  context    inspect <id> | build <id> | compact <id> [--keep-messages N] [--recap truncate|model]
  tools      list | describe <id> | active | activate <id> --session ID | deactivate <id> --session ID
  events     --session <id> [--jsonl|--json]
  serve      [--addr HOST:PORT] [--max-runs N] [--max-sessions N] [--queue reject|wait] [--warm-ttl D] [--no-idle-compaction] [--advertise URL] [--forward]   HTTP session service
  doctor     [--json]
  version

Global flags (also accepted after the command):
  --store DIR   session store directory (default $GOTATO_HOME/sessions or ~/.gotato/sessions)
  --json        JSON output on stdout      --jsonl   JSON Lines output on stdout
  --quiet       suppress non-error output
  --timeout D   overall deadline, e.g. 30s

Exit codes: 0 ok · 1 runtime error · 2 usage error · 3 not found · 4 run did not complete
`, "\n"))
}
