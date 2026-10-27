package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/jinhuang712/gotato/session"
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
	add := func(check doctorCheck) {
		if !check.OK && !check.Warning {
			report.OK = false
		}
		report.Checks = append(report.Checks, check)
	}

	rt, err := c.newRuntime(flags)
	if err != nil {
		add(doctorCheck{Name: "store", OK: false, Detail: err.Error()})
	} else {
		probe := session.New()
		if _, err := session.CreateWriter(ctx, rt.store, probe, rt.runner.Instance()); err != nil {
			add(doctorCheck{Name: "store", OK: false, Detail: rt.storeDir + ": not writable: " + err.Error()})
		} else if err := rt.store.Delete(ctx, probe.ID()); err != nil {
			add(doctorCheck{Name: "store", OK: false, Detail: rt.storeDir + ": probe cleanup failed: " + err.Error()})
		} else if list, err := rt.store.List(ctx); err != nil {
			add(doctorCheck{Name: "store", OK: false, Detail: rt.storeDir + ": cannot list sessions: " + err.Error()})
		} else {
			add(doctorCheck{Name: "store", OK: true, Detail: fmt.Sprintf("%s (%d sessions)", rt.storeDir, len(list))})
		}
		add(doctorCheck{Name: "model.echo", OK: true, Detail: "deterministic, no credentials"})
		add(doctorCheck{Name: "model.demo", OK: true, Detail: "deterministic tool loop, no credentials"})
		if rt.gatewayErr == nil {
			spec, _ := rt.runner.Agent("gateway")
			add(doctorCheck{Name: "model.gateway", OK: true, Detail: rt.gatewayCfg + " → " + spec.ModelName})
		} else if fileExists(rt.gatewayCfg) {
			add(doctorCheck{Name: "model.gateway", OK: false, Detail: rt.gatewayErr.Error()})
		} else {
			add(doctorCheck{Name: "model.gateway", OK: false, Warning: true, Detail: rt.gatewayCfg + " not found; --model gateway unavailable"})
		}
		names := make([]string, 0)
		for _, tool := range builtinTools() {
			names = append(names, tool.Spec().ID)
		}
		add(doctorCheck{Name: "tools", OK: true, Detail: strings.Join(names, ", ")})
	}
	if _, err := os.UserHomeDir(); err != nil {
		add(doctorCheck{Name: "home", OK: false, Warning: true, Detail: err.Error()})
	}

	if c.machine() {
		c.writeJSON(report)
	} else {
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
	if !report.OK {
		return ExitError
	}
	return ExitOK
}
