package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/jinhuang712/gotato/record"

	"github.com/jinhuang712/gotato/store"
)

type doctorReport struct {
	OK      bool          `json:"ok"`
	Version string        `json:"version"`
	Go      string        `json:"go"`
	Checks  []doctorCheck `json:"checks"`
}

type doctorCheck struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail,omitempty"`
	Warning bool   `json:"warning,omitempty"`
}

func (c *cli) cmdDoctor(args []string) int {
	fs := c.newFlagSet("doctor")
	var flags modelFlags
	bindModel(fs, &flags)
	if _, err := parseInterspersed(fs, args); err != nil {
		return c.parseError(err)
	}
	ctx, cancel := c.ctx()
	defer cancel()
	report := doctorReport{OK: true, Version: Version, Go: runtime.Version()}

	rt, err := c.newRuntime(flags)
	if err != nil {
		report.add(doctorCheck{Name: "store", OK: false, Detail: err.Error()})
	} else {
		report.add(storeCheck(ctx, rt))
		report.add(doctorCheck{Name: "model.echo", OK: true, Detail: "deterministic, no credentials"})
		report.add(doctorCheck{Name: "model.demo", OK: true, Detail: "deterministic tool loop, no credentials"})
		report.add(gatewayCheck(ctx, rt))
		report.add(toolsCheck())
	}
	if _, err := os.UserHomeDir(); err != nil {
		report.add(doctorCheck{Name: "home", OK: false, Warning: true, Detail: err.Error()})
	}

	if c.machine() {
		c.writeJSON(report)
	} else {
		c.printDoctor(report)
	}
	if !report.OK {
		return ExitError
	}
	return ExitOK
}

// add appends a check; a failed check that is not a warning fails the report.
func (report *doctorReport) add(check doctorCheck) {
	if !check.OK && !check.Warning {
		report.OK = false
	}
	report.Checks = append(report.Checks, check)
}

// storeCheck writes, deletes and lists a probe Session in the store.
func storeCheck(ctx context.Context, rt *cliRuntime) doctorCheck {
	probe := record.New()
	if _, err := store.CreateWriter(ctx, rt.store.Records, probe, rt.runner.Instance()); err != nil {
		return doctorCheck{Name: "store", OK: false, Detail: rt.storeDir + ": not writable: " + err.Error()}
	}
	if err := rt.store.Records.Delete(ctx, probe.ID()); err != nil {
		return doctorCheck{Name: "store", OK: false, Detail: rt.storeDir + ": probe cleanup failed: " + err.Error()}
	}
	list, err := listAll(ctx, rt.store)
	if err != nil {
		return doctorCheck{Name: "store", OK: false, Detail: rt.storeDir + ": cannot list sessions: " + err.Error()}
	}
	return doctorCheck{Name: "store", OK: true, Detail: fmt.Sprintf("%s (%d sessions)", rt.storeDir, len(list))}
}

// gatewayCheck reports whether the gateway configuration loaded.
func gatewayCheck(ctx context.Context, rt *cliRuntime) doctorCheck {
	if rt.gatewayErr == nil {
		spec, _ := rt.runner.AgentProfile(ctx, "gateway")
		return doctorCheck{Name: "model.gateway", OK: true, Detail: rt.gatewayCfg + " → " + spec.ModelName}
	}
	if fileExists(rt.gatewayCfg) {
		return doctorCheck{Name: "model.gateway", OK: false, Detail: rt.gatewayErr.Error()}
	}
	return doctorCheck{Name: "model.gateway", OK: false, Warning: true, Detail: rt.gatewayCfg + " not found; --model gateway unavailable"}
}

// toolsCheck lists the built-in tools.
func toolsCheck() doctorCheck {
	names := make([]string, 0)
	for _, tool := range builtinTools() {
		names = append(names, tool.Spec().ID)
	}
	return doctorCheck{Name: "tools", OK: true, Detail: strings.Join(names, ", ")}
}

// printDoctor prints one line per check.
func (c *cli) printDoctor(report doctorReport) {
	for _, check := range report.Checks {
		mark := "ok  "
		if !check.OK {
			mark = "FAIL"
			if check.Warning {
				mark = "warn"
			}
		}
		fmt.Fprintf(c.stdout, "%s %-14s %s\n", mark, check.Name, check.Detail)
	}
}
