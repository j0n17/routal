package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/j0n17/routal/pkg/services"
)

// supervisor runs the service daemons as child processes. A daemon is restarted when its rendered
// config changes, and started again when it exits; a config the daemon's own check rejects is not
// installed and the running daemon keeps the previous one.
type supervisor struct {
	dirs  services.Dirs
	procs map[string]*proc
	warns map[string]string
}

type proc struct {
	svc  services.Service
	cmd  *exec.Cmd
	done chan struct{}
	gen  int // incremented on every start
}

func (p *proc) running() bool {
	if p == nil || p.cmd == nil {
		return false
	}

	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (p *proc) stop() {
	if !p.running() {
		return
	}

	_ = p.cmd.Process.Signal(syscall.SIGTERM)

	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

func newSupervisor(dirs services.Dirs) *supervisor {
	return &supervisor{dirs: dirs, procs: map[string]*proc{}, warns: map[string]string{}}
}

// generation returns how many times a service was started (0: never), so that callers can redo
// runtime state they pushed to it.
func (s *supervisor) generation(name string) int {
	if p := s.procs[name]; p.running() {
		return p.gen
	}

	return 0
}

func (s *supervisor) reconcile(svcs []services.Service) error {
	var errs []error

	want := map[string]bool{}

	for _, svc := range svcs {
		want[svc.Name] = true

		if w := strings.Join(svc.Warnings, "; "); w != s.warns[svc.Name] {
			s.warns[svc.Name] = w

			if w != "" {
				log.Printf("%s: %s (retrying)", svc.Name, w)
			}
		}

		if p := s.procs[svc.Name]; p.running() && p.svc.Config == svc.Config {
			continue
		}

		if err := s.start(svc); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", svc.Name, err))
		}
	}

	for name, p := range s.procs {
		if !want[name] {
			log.Printf("%s: no longer configured, stopping", name)
			p.stop()
			delete(s.procs, name)
		}
	}

	return errors.Join(errs...)
}

func (s *supervisor) start(svc services.Service) error {
	for _, dir := range []string{s.dirs.State, s.dirs.Run} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	path := filepath.Join(s.dirs.State, svc.File)
	old := s.procs[svc.Name]

	if old == nil || old.svc.Config != svc.Config {
		if err := os.WriteFile(path+".new", []byte(svc.Config), 0o644); err != nil {
			return err
		}

		if svc.Test != nil {
			test := command(svc.Test, path+".new", svc.Env)
			if out, err := test.CombinedOutput(); err != nil {
				return fmt.Errorf("generated config rejected, keeping the running one: %w: %s", err, strings.TrimSpace(string(out)))
			}
		}

		if err := os.Rename(path+".new", path); err != nil {
			return err
		}
	}

	gen := 1
	if old != nil {
		old.stop()
		gen = old.gen + 1
	}

	cmd := command(svc.Command, path, svc.Env)

	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return err
	}

	p := &proc{svc: svc, cmd: cmd, done: make(chan struct{}), gen: gen}
	s.procs[svc.Name] = p

	go func() {
		logLines(svc.Name, out)

		err := cmd.Wait()
		log.Printf("%s exited: %v", svc.Name, err)
		close(p.done)
	}()

	log.Printf("%s started (pid %d)", svc.Name, cmd.Process.Pid)

	return nil
}

func (s *supervisor) stopAll() {
	for _, p := range s.procs {
		p.stop()
	}
}

func command(argv []string, conf string, env []string) *exec.Cmd {
	args := slices.Clone(argv)
	for i, a := range args {
		args[i] = strings.ReplaceAll(a, "{conf}", conf)
	}

	cmd := exec.Command(args[0], args[1:]...)
	// extension services start with an empty environment
	cmd.Env = append([]string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}, env...)

	return cmd
}

func logLines(name string, r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		log.Printf("%s: %s", name, sc.Text())
	}
}
