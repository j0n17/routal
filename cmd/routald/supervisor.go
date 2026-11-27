package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/j0n17/routal/pkg/services"
)

// supervisor runs the service daemons as child processes. A daemon is restarted when its rendered
// config changes, and started again when it exits, after a delay that doubles while it keeps
// exiting quickly (a daemon failing against a remote service must not hammer it); a config the
// daemon's own check rejects is not installed and the running daemon keeps the previous one.
type supervisor struct {
	dirs  services.Dirs
	procs map[string]*proc
	warns map[string]string
}

// restart delays after an exit: doubled after each exit within a minute of the start
const (
	minRestartDelay = 5 * time.Second
	maxRestartDelay = 5 * time.Minute
	stableRun       = time.Minute
)

type proc struct {
	svc  services.Service
	cmd  *exec.Cmd
	done chan struct{}
	gen  int // incremented on every start

	started, exited time.Time
	restartDelay    time.Duration // wait after an exit before starting again
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

// stopGrace is how long a daemon gets to exit after SIGTERM before it is killed.
var stopGrace = 5 * time.Second

// stop ends the daemon and the processes it started (its process group): a helper left behind
// would keep the log pipe open and the stop would never return.
func (p *proc) stop() {
	if !p.running() {
		return
	}

	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)

	select {
	case <-p.done:
	case <-time.After(stopGrace):
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
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

		p := s.procs[svc.Name]
		if p.running() && p.svc.Config == svc.Config {
			continue
		}

		if p != nil && p.svc.Config == svc.Config && time.Since(p.exited) < p.restartDelay {
			continue // crashed: wait before starting it again
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
		if err := openDir(dir); err != nil {
			return err
		}
	}

	cred, err := s.identity(svc)
	if err != nil {
		return err
	}

	path := filepath.Join(s.dirs.State, svc.File)
	old := s.procs[svc.Name]

	if old == nil || old.svc.Config != svc.Config {
		// configs may hold secrets (PPPoE password, DNS provider token): readable by root and,
		// through its group, by the daemon's account only
		if err := os.WriteFile(path+".new", []byte(svc.Config), 0o600); err != nil {
			return err
		}

		if cred != nil {
			if err := errors.Join(os.Chown(path+".new", 0, int(cred.Gid)), os.Chmod(path+".new", 0o640)); err != nil {
				return err
			}
		}

		if svc.Test != nil {
			test := command(svc.Test, path+".new", svc.Env, cred, nil)
			if out, err := test.CombinedOutput(); err != nil {
				return fmt.Errorf("generated config rejected, keeping the running one: %w: %s", err, strings.TrimSpace(string(out)))
			}
		}

		if err := os.Rename(path+".new", path); err != nil {
			return err
		}
	}

	gen, delay := 1, minRestartDelay
	if old != nil {
		crashed := !old.running() && old.svc.Config == svc.Config
		old.stop()
		gen = old.gen + 1

		if crashed && old.exited.Sub(old.started) < stableRun {
			delay = min(old.restartDelay*2, maxRestartDelay)
			log.Printf("%s keeps exiting (ran %s), next restart in %s at the earliest", svc.Name, old.exited.Sub(old.started).Round(time.Second), delay)
		}
	}

	cmd := command(svc.Command, path, svc.Env, cred, svc.Caps)

	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return err
	}

	p := &proc{svc: svc, cmd: cmd, done: make(chan struct{}), gen: gen, started: time.Now(), restartDelay: delay}
	s.procs[svc.Name] = p

	go func() {
		logLines(svc.Name, out)

		err := cmd.Wait()
		log.Printf("%s exited: %v", svc.Name, err)
		p.exited = time.Now() // read only after done is closed
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

// lookupUser returns the uid and gid of an account of the container (/etc/passwd).
var lookupUser = func(name string) (uid, gid uint32, err error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, err
	}

	id, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return 0, 0, err
	}

	group, err := strconv.ParseUint(u.Gid, 10, 32)

	return uint32(id), uint32(group), err
}

// identity returns the account a service runs as (nil: root) and makes its directories, which
// only that account can enter. A service whose account is missing is not started as root instead.
func (s *supervisor) identity(svc services.Service) (*syscall.Credential, error) {
	if svc.User == "" {
		return nil, nil
	}

	uid, gid, err := lookupUser(svc.User)
	if err != nil {
		return nil, fmt.Errorf("account %s: %w", svc.User, err)
	}

	for _, dir := range append([]string{s.dirs.Data(svc.Dir), s.dirs.Runtime(svc.Dir)}, svc.OwnDirs...) {
		if err := errors.Join(os.MkdirAll(dir, 0o750), os.Chown(dir, int(uid), int(gid)), os.Chmod(dir, 0o750)); err != nil {
			return nil, err
		}
	}

	// no supplementary groups
	return &syscall.Credential{Uid: uid, Gid: gid}, nil
}

// openDir creates dir and lets every account list it and get to it. Talos creates the mount
// sources of the extension (/var/lib/routal, and /run above the runtime directory) with mode 0700:
// the daemons, which are not root, have to get through them to their config and their own
// directory. Ancestors only gain the search bit they lack.
func openDir(dir string) error {
	if err := errors.Join(os.MkdirAll(dir, 0o755), os.Chmod(dir, 0o755)); err != nil {
		return err
	}

	for p := filepath.Dir(dir); p != filepath.Dir(p); p = filepath.Dir(p) {
		st, err := os.Stat(p)
		if err != nil {
			return err
		}

		if mode := st.Mode().Perm(); mode&0o011 != 0o011 {
			if err := os.Chmod(p, mode|0o011); err != nil {
				return err
			}
		}
	}

	return nil
}

// command builds the command of a daemon or of its config check. With cred, the process leaves
// root before it executes and keeps caps as its only capabilities.
func command(argv []string, conf string, env []string, cred *syscall.Credential, caps []uintptr) *exec.Cmd {
	args := slices.Clone(argv)
	for i, a := range args {
		args[i] = strings.ReplaceAll(a, "{conf}", conf)
	}

	cmd := exec.Command(args[0], args[1:]...)
	// extension services start with an empty environment
	cmd.Env = append([]string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}, env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // see proc.stop

	if cred != nil {
		cmd.SysProcAttr.Credential = cred
		cmd.SysProcAttr.AmbientCaps = caps
	}

	return cmd
}

func logLines(name string, r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		log.Printf("%s: %s", name, sc.Text())
	}
}
