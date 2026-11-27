package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/j0n17/routal/pkg/services"
)

func TestStatusReport(t *testing.T) {
	dirs := services.Dirs{State: t.TempDir(), Run: t.TempDir()}
	a := &agent{dirs: dirs, services: newSupervisor(dirs), started: time.Now()}

	good := services.Service{
		Name: "good", File: "good.conf", Config: "x", Command: []string{"/bin/sh", "-c", "exec sleep 60"},
		Warnings: []string{"wan0 has no address yet"},
	}
	crash := services.Service{Name: "crash", File: "crash.conf", Config: "x", Command: []string{"/bin/sh", "-c", "exit 48"}}
	refused := services.Service{
		Name: "refused", File: "refused.conf", Config: "x", Command: []string{"/bin/sh", "-c", "exec sleep 60"},
		Test: []string{"/bin/sh", "-c", "exit 1"},
	}

	defer a.services.stopAll()

	read := func() Status {
		t.Helper()

		raw, err := os.ReadFile(filepath.Join(dirs.Run, StatusFile))
		if err != nil {
			t.Fatal(err)
		}

		var st Status
		if err := json.Unmarshal(raw, &st); err != nil {
			t.Fatal(err)
		}

		return st
	}

	// one daemon running, with something it waits for: healthy
	a.report(a.services.reconcile([]services.Service{good}))

	st := read()
	if !st.Healthy || len(st.Daemons) != 1 || st.Daemons[0].State != "running" || st.Daemons[0].PID == 0 || st.Daemons[0].Starts != 1 {
		t.Errorf("one running daemon: %+v", st)
	}

	if len(st.Warnings) != 1 || !strings.Contains(st.Warnings[0], "wan0 has no address yet") {
		t.Errorf("warnings = %v", st.Warnings)
	}

	// a daemon that exits and one whose config its own check refuses: not healthy, with the reasons
	err := a.services.reconcile([]services.Service{good, crash, refused})
	<-a.services.procs["crash"].done
	a.report(err)

	st = read()
	problems := strings.Join(st.Problems, "\n")

	if st.Healthy || len(st.Daemons) != 3 {
		t.Fatalf("with a crashed daemon: %+v", st)
	}

	for _, want := range []string{"the config is not fully applied: refused:", "crash is not running", "refused could not be started"} {
		if !strings.Contains(problems, want) {
			t.Errorf("problems lack %q:\n%s", want, problems)
		}
	}

	// started again after an early exit: not settled yet, even while it runs
	crash.Command = []string{"/bin/sh", "-c", "exec sleep 60"}
	p := a.services.procs["crash"]
	p.exited = time.Now().Add(-p.restartDelay)
	a.services.procs["crash"].svc.Config = "x"

	for range 2 { // two early exits: the delay doubles
		_ = a.services.reconcile([]services.Service{good, {Name: "crash", File: "crash.conf", Config: "x", Command: []string{"/bin/sh", "-c", "exit 48"}}})
		p = a.services.procs["crash"]
		<-p.done
		p.exited = time.Now().Add(-p.restartDelay)
	}

	a.report(a.services.reconcile([]services.Service{good, crash}))

	if st = read(); st.Healthy || !strings.Contains(strings.Join(st.Problems, "\n"), "crash keeps exiting") {
		t.Errorf("a daemon restarted after early exits: %+v", st)
	}

	// back to what works
	a.report(a.services.reconcile([]services.Service{good}))

	if st = read(); !st.Healthy || len(st.Problems) != 0 || len(st.Daemons) != 1 {
		t.Errorf("after the bad daemons are gone: %+v", st)
	}
}

func TestStatusReportOfAFailedReconcile(t *testing.T) {
	dirs := services.Dirs{State: t.TempDir(), Run: t.TempDir()}
	a := &agent{dirs: dirs, services: newSupervisor(dirs), started: time.Now()}

	a.report(errors.New("nft: exit status 1: syntax error\nsecond line"))

	raw, _ := os.ReadFile(filepath.Join(dirs.Run, StatusFile))
	if !strings.Contains(string(raw), `"healthy": false`) || !strings.Contains(string(raw), "nft: exit status 1: syntax error") ||
		strings.Contains(string(raw), "second line") {
		t.Errorf("report: %s", raw)
	}
}

func TestPrintStatus(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

	report := func(st Status) string {
		raw, _ := json.Marshal(st)

		return string(raw)
	}

	healthy := Status{
		Time: now.Add(-3 * time.Second), Started: now.Add(-time.Hour), Healthy: true,
		Warnings: []string{"inadyn: wan0 has no address yet"},
		Daemons: []DaemonStatus{
			{Name: "unbound", State: "running", PID: 22, Starts: 1, Since: now.Add(-time.Hour)},
			{Name: "kea-dhcp4", State: "running", PID: 20, Starts: 2, Since: now.Add(-time.Minute)},
		},
	}

	var out bytes.Buffer
	if err := printStatus(strings.NewReader(report(healthy)), &out, time.Minute, now); err != nil {
		t.Errorf("healthy report: %v", err)
	}

	for _, want := range []string{"routald: healthy (report 3s old, up 59m57s)", "waiting: inadyn: wan0 has no address yet", "kea-dhcp4  running  20   2"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}

	if strings.Index(out.String(), "kea-dhcp4") > strings.Index(out.String(), "unbound") {
		t.Errorf("daemons are not sorted:\n%s", out.String())
	}

	// a report that is not renewed: routald is gone
	if err := printStatus(strings.NewReader(report(healthy)), &out, time.Minute, now.Add(10*time.Minute)); err == nil || !strings.Contains(err.Error(), "old") {
		t.Errorf("stale report: %v", err)
	}

	if err := printStatus(strings.NewReader(report(healthy)), &out, 0, now.Add(10*time.Minute)); err != nil {
		t.Errorf("stale report with -max-age 0: %v", err)
	}

	sick := healthy
	sick.Healthy, sick.Problems = false, []string{"unbound is not running (exited 4s ago, started 3 times)"}
	sick.Daemons = []DaemonStatus{{Name: "unbound", State: "restarting", Starts: 3}}

	out.Reset()

	if err := printStatus(strings.NewReader(report(sick)), &out, time.Minute, now); err == nil || !strings.Contains(err.Error(), "unbound is not running") {
		t.Errorf("sick report: %v", err)
	}

	if !strings.Contains(out.String(), "routald: NOT healthy") || !strings.Contains(out.String(), "unbound  restarting  -    3") {
		t.Errorf("sick output:\n%s", out.String())
	}

	if err := printStatus(strings.NewReader("garbage"), &out, 0, now); err == nil {
		t.Error("garbage was taken for a report")
	}
}

// A config file already on disk with the same content is not written again: with a full disk the
// write would fail, and the daemon would not start with a config that is there.
func TestSupervisorKeepsAnUnchangedFile(t *testing.T) {
	dirs := services.Dirs{State: t.TempDir(), Run: t.TempDir()}
	path := filepath.Join(dirs.State, "daemon.conf")

	if err := os.WriteFile(path, []byte("same"), 0o600); err != nil {
		t.Fatal(err)
	}

	// writing the file again cannot work: a directory is in the way of the temporary file
	if err := os.Mkdir(path+".new", 0o755); err != nil {
		t.Fatal(err)
	}

	s := newSupervisor(dirs)
	defer s.stopAll()

	svc := services.Service{Name: "daemon", File: "daemon.conf", Config: "same", Command: []string{"/bin/sh", "-c", "exec sleep 60"}}
	if err := s.reconcile([]services.Service{svc}); err != nil || s.generation("daemon") != 1 {
		t.Errorf("with an unchanged file on a full disk: err = %v, generation %d", err, s.generation("daemon"))
	}

	svc.Config = "other"
	if err := s.reconcile([]services.Service{svc}); err == nil {
		t.Error("a changed config was reported as written")
	}
}
