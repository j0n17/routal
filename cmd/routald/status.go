package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/j0n17/routal/pkg/services"
)

// StatusFile is the health report of routald, in Dirs.Run, rewritten after every reconcile. On
// Talos the host sees it as /run/routal/routal/status.json:
//
//	talosctl read /run/routal/routal/status.json | routald status -
const StatusFile = "status.json"

// Status is what routald knows about its own health.
type Status struct {
	// Time of the report; a report that is not renewed means routald itself is stuck or gone.
	Time time.Time `json:"time"`
	// Started is when this routald started.
	Started time.Time `json:"started"`
	// Healthy: the config is applied and every daemon it needs is running and stays up.
	Healthy bool `json:"healthy"`
	// Problems are the reasons it is not healthy.
	Problems []string `json:"problems,omitempty"`
	// Warnings are parts of the config that wait for something (a link without an address yet).
	Warnings []string `json:"warnings,omitempty"`
	// Daemons are the supervised daemons the config needs.
	Daemons []DaemonStatus `json:"daemons"`
}

// DaemonStatus is one supervised daemon.
type DaemonStatus struct {
	Name string `json:"name"`
	// State is running, restarting (it keeps exiting) or stopped.
	State string `json:"state"`
	PID   int    `json:"pid,omitempty"`
	// Starts counts how many times it was started since routald started.
	Starts int `json:"starts"`
	// Since is when it last started.
	Since time.Time `json:"since,omitempty"`
	// LastExit is when it last exited, when it is not running.
	LastExit time.Time `json:"lastExit,omitempty"`
}

// status reports the daemons the last reconcile wanted.
func (s *supervisor) status() (daemons []DaemonStatus, problems, warnings []string) {
	for _, name := range s.wanted {
		d := DaemonStatus{Name: name, State: "stopped"}

		switch p := s.procs[name]; {
		case p == nil:
			problems = append(problems, name+" could not be started")
		case p.running():
			d.State, d.PID, d.Starts, d.Since = "running", p.cmd.Process.Pid, p.gen, p.started

			// started again after exiting early: not settled until it has run for a while
			if p.restartDelay > minRestartDelay && time.Since(p.started) < stableRun {
				d.State = "restarting"
				problems = append(problems, fmt.Sprintf("%s keeps exiting (started %d times)", name, p.gen))
			}
		default:
			d.Starts, d.Since, d.LastExit = p.gen, p.started, p.exited
			d.State = "restarting"
			problems = append(problems, fmt.Sprintf("%s is not running (exited %s ago, started %d times)",
				name, time.Since(p.exited).Round(time.Second), p.gen))
		}

		if w := s.warns[name]; w != "" {
			warnings = append(warnings, name+": "+w)
		}

		daemons = append(daemons, d)
	}

	return daemons, problems, warnings
}

// report writes the status file and logs when the health changes: the log is what a Talos console
// and `talosctl logs ext-routald` show.
func (a *agent) report(reconcileErr error) {
	st := Status{Time: time.Now(), Started: a.started}

	st.Daemons, st.Problems, st.Warnings = a.services.status()
	if reconcileErr != nil {
		st.Problems = append([]string{"the config is not fully applied: " + firstLine(reconcileErr.Error())}, st.Problems...)
	}

	st.Healthy = len(st.Problems) == 0

	// logged when it changes, and once at the start
	if summary := strings.Join(st.Problems, "; "); summary != a.lastHealth || !a.reported {
		if st.Healthy {
			log.Printf("health: ok (%d daemons)", len(st.Daemons))
		} else {
			log.Printf("health: NOT OK: %s", summary)
		}

		a.lastHealth = summary
	}

	a.reported = true

	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}

	path := filepath.Join(a.dirs.Run, StatusFile)
	if err := os.MkdirAll(a.dirs.Run, 0o755); err != nil {
		return
	}

	// written beside, then renamed: a reader never sees half a report
	if err := os.WriteFile(path+".new", append(raw, '\n'), 0o644); err == nil {
		_ = os.Rename(path+".new", path)
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")

	return line
}

// printStatus is `routald status`: it prints a report and fails when the router is not healthy or
// the report is older than maxAge (0: any age).
func printStatus(r io.Reader, w io.Writer, maxAge time.Duration, now time.Time) error {
	var st Status

	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(raw, &st); err != nil {
		return fmt.Errorf("not a routald status report: %w", err)
	}

	age := now.Sub(st.Time).Round(time.Second)

	state := "healthy"
	if !st.Healthy {
		state = "NOT healthy"
	}

	fmt.Fprintf(w, "routald: %s (report %s old, up %s)\n", state, age, st.Time.Sub(st.Started).Round(time.Second))

	for _, p := range st.Problems {
		fmt.Fprintf(w, "  problem: %s\n", p)
	}

	for _, p := range st.Warnings {
		fmt.Fprintf(w, "  waiting: %s\n", p)
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "DAEMON\tSTATE\tPID\tSTARTS\tRUNNING FOR")

	slices.SortFunc(st.Daemons, func(a, b DaemonStatus) int { return strings.Compare(a.Name, b.Name) })

	for _, d := range st.Daemons {
		pid, up := "-", "-"
		if d.State == "running" {
			pid, up = fmt.Sprint(d.PID), st.Time.Sub(d.Since).Round(time.Second).String()
		}

		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", d.Name, d.State, pid, d.Starts, up)
	}

	_ = tw.Flush()

	switch {
	case maxAge > 0 && (age > maxAge || age < -maxAge):
		return fmt.Errorf("the report is %s old: routald is not running, or the clocks differ (-max-age 0 to ignore)", age)
	case !st.Healthy:
		return fmt.Errorf("not healthy: %s", strings.Join(st.Problems, "; "))
	}

	return nil
}

func statusCommand(args []string) error {
	path := filepath.Join(services.DefaultDirs.Run, StatusFile)
	maxAge := time.Minute

	for len(args) > 0 {
		switch {
		case args[0] == "-max-age" && len(args) > 1:
			d, err := time.ParseDuration(args[1])
			if err != nil {
				return err
			}

			maxAge, args = d, args[2:]
		default:
			path, args = args[0], args[1:]
		}
	}

	in := io.Reader(os.Stdin)

	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		in = f
	}

	return printStatus(in, os.Stdout, maxAge, time.Now())
}
