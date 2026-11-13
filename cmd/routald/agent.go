package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/j0n17/routal/pkg/config"
	"github.com/j0n17/routal/pkg/nft"
)

const reconcileInterval = 5 * time.Second

// agent applies the config and re-applies it when the file or the link names change.
// Rules are left in place on exit so that restarting routald never opens the firewall.
type agent struct {
	configPath string
	nftBin     string
	dnsmasq    *dnsmasqSupervisor

	applied [32]byte
}

func (a *agent) run(ctx context.Context) error {
	log.SetFlags(0)
	log.Printf("routald starting, config %s", a.configPath)

	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()

	for {
		if err := a.reconcile(); err != nil {
			log.Printf("error: %v (keeping the previously applied rules)", err)
		}

		select {
		case <-ctx.Done():
			a.dnsmasq.stop()
			log.Printf("routald stopping, rules left in place")

			return nil
		case <-ticker.C:
		}
	}
}

func (a *agent) reconcile() error {
	cfg, err := config.LoadFile(a.configPath)
	if err != nil {
		return err
	}

	return errors.Join(a.applyFirewall(cfg), a.dnsmasq.reconcile(cfg))
}

// applyFirewall applies the forwarding sysctls and the nftables ruleset when they changed.
func (a *agent) applyFirewall(cfg *config.Config) error {
	var aliases []string

	ruleset := nft.Render(cfg, func(name string) string {
		kernel := resolveLink(name)
		if kernel != name {
			aliases = append(aliases, name+"="+kernel)
		}

		return kernel
	})

	sum := sha256.Sum256([]byte(fmt.Sprintf("%v%v\n%s", *cfg.Forwarding.IPv4, *cfg.Forwarding.IPv6, ruleset)))
	if sum == a.applied {
		return nil
	}

	if err := setSysctl("net/ipv4/conf/all/forwarding", *cfg.Forwarding.IPv4); err != nil {
		return err
	}

	if err := setSysctl("net/ipv6/conf/all/forwarding", *cfg.Forwarding.IPv6); err != nil {
		return err
	}

	cmd := exec.Command(a.nftBin, "-f", "-")
	cmd.Stdin = strings.NewReader(ruleset)

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nft: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	a.applied = sum

	slices.Sort(aliases)

	log.Printf("applied: forwarding ipv4=%v ipv6=%v, %d zones, %d rules, %d masquerades, %d port forwards, link aliases %v",
		*cfg.Forwarding.IPv4, *cfg.Forwarding.IPv6, len(cfg.Zones), len(cfg.Rules), len(cfg.Masquerades), len(cfg.PortForwards),
		slices.Compact(aliases))

	return nil
}

func setSysctl(name string, on bool) error {
	v := "0"
	if on {
		v = "1"
	}

	return os.WriteFile(filepath.Join("/proc/sys", name), []byte(v), 0o644)
}

// resolveLink maps a Talos link alias (set by LinkAliasConfig as the kernel ifalias) to the kernel
// interface name; names that are not aliases are returned unchanged.
func resolveLink(name string) string {
	if _, err := os.Stat(filepath.Join("/sys/class/net", name)); err == nil {
		return name
	}

	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return name
	}

	for _, e := range entries {
		alias, err := os.ReadFile(filepath.Join("/sys/class/net", e.Name(), "ifalias"))
		if err == nil && strings.TrimSpace(string(alias)) == name {
			return e.Name()
		}
	}

	return name
}
