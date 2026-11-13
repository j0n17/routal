package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/j0n17/routal/pkg/config"
	"github.com/j0n17/routal/pkg/leases"
	"github.com/j0n17/routal/pkg/services"
)

// leaseNames publishes the hostnames of DHCP clients in unbound (<hostname>.<localDomain>), through
// unbound-control, so that new leases do not restart unbound and flush its cache.
type leaseNames struct {
	ctl  string // unbound-control binary
	dirs services.Dirs

	gen    int                        // unbound generation the records were pushed to
	pushed map[string]services.Record // by name
}

func (l *leaseNames) sync(cfg *config.Config, unboundGen int) error {
	dns := cfg.DNSServer
	if dns == nil || !*dns.RegisterDHCPLeases || dns.LocalDomain == "" || unboundGen == 0 {
		l.pushed = nil

		return nil
	}

	if unboundGen != l.gen {
		l.gen, l.pushed = unboundGen, map[string]services.Record{}
	}

	taken := map[string]bool{}
	for _, r := range services.StaticRecords(cfg) {
		taken[r.Name] = true
	}

	want := map[string]services.Record{}

	active, err := leases.ReadKea4(filepath.Join(l.dirs.State, services.Kea4LeaseFile))
	if err != nil {
		return err
	}

	for _, lease := range active {
		name := services.Qualify(lease.Hostname, dns.LocalDomain)
		if !taken[name] {
			want[name] = services.Record{Name: name, Addr: lease.Address}
		}
	}

	var remove, add []string

	for name, r := range l.pushed {
		if w, ok := want[name]; !ok || w != r {
			remove = append(remove, name+".", strings.Fields(r.PTR())[0])
		}
	}

	for name, r := range want {
		if p, ok := l.pushed[name]; !ok || p != r {
			add = append(add, r.RR(), r.PTR())
		}
	}

	if len(remove) > 0 {
		if err := l.control("local_datas_remove", remove); err != nil {
			return err
		}
	}

	if len(add) > 0 {
		if err := l.control("local_datas", add); err != nil {
			return err
		}
	}

	l.pushed = want

	return nil
}

func (l *leaseNames) control(cmd string, lines []string) error {
	c := exec.Command(l.ctl, "-c", filepath.Join(l.dirs.State, "unbound.conf"), cmd)
	c.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")

	var out bytes.Buffer

	c.Stdout, c.Stderr = &out, &out

	if err := c.Run(); err != nil {
		return fmt.Errorf("unbound-control %s: %w: %s", cmd, err, strings.TrimSpace(out.String()))
	}

	return nil
}
