package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/j0n17/routal/pkg/config"
	"github.com/j0n17/routal/pkg/dnsmasq"
)

// dnsmasqSupervisor runs dnsmasq as a child process with the rendered config, restarting it when
// the config changes and starting it again if it exits.
type dnsmasqSupervisor struct {
	bin      string
	stateDir string

	conf string // config of the running process
	cmd  *exec.Cmd
	done chan struct{}
}

func (s *dnsmasqSupervisor) reconcile(cfg *config.Config) error {
	if !dnsmasq.Needed(cfg) {
		if s.running() {
			log.Printf("dnsmasq: no DHCP/DNS/RA documents, stopping")
			s.stop()
		}

		return nil
	}

	conf := dnsmasq.Render(cfg, resolveLink, filepath.Join(s.stateDir, "dnsmasq.leases"))
	if conf == s.conf && s.running() {
		return nil
	}

	if err := os.MkdirAll(s.stateDir, 0o755); err != nil {
		return err
	}

	path := filepath.Join(s.stateDir, "dnsmasq.conf")

	if err := os.WriteFile(path+".new", []byte(conf), 0o644); err != nil {
		return err
	}

	if out, err := exec.Command(s.bin, "--test", "--conf-file="+path+".new").CombinedOutput(); err != nil {
		return fmt.Errorf("dnsmasq rejected the generated config (keeping the running one): %w: %s", err, strings.TrimSpace(string(out)))
	}

	if err := os.Rename(path+".new", path); err != nil {
		return err
	}

	s.stop()

	cmd := exec.Command(s.bin, "--keep-in-foreground", "--conf-file="+path)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting dnsmasq: %w", err)
	}

	s.cmd, s.conf, s.done = cmd, conf, make(chan struct{})

	go func(done chan struct{}) {
		err := cmd.Wait()
		log.Printf("dnsmasq exited: %v", err)
		close(done)
	}(s.done)

	log.Printf("dnsmasq started (pid %d): %d DHCP servers, DNS %v, %d router advertisements",
		cmd.Process.Pid, len(cfg.DHCPServers), cfg.DNSServer != nil, len(cfg.RouterAdvertisements))

	return nil
}

func (s *dnsmasqSupervisor) running() bool {
	if s.cmd == nil {
		return false
	}

	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

func (s *dnsmasqSupervisor) stop() {
	if s.running() {
		_ = s.cmd.Process.Signal(syscall.SIGTERM)

		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			_ = s.cmd.Process.Kill()
			<-s.done
		}
	}

	s.cmd, s.conf = nil, ""
}
