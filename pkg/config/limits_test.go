package config_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/j0n17/routal/pkg/config"
)

// reference loads examples/reference/router.yaml with one piece of text replaced.
func reference(t *testing.T, old, repl string) (*config.Config, error) {
	t.Helper()

	raw, err := os.ReadFile("../../examples/reference/router.yaml")
	if err != nil {
		t.Fatal(err)
	}

	if old != "" && !strings.Contains(string(raw), old) {
		t.Fatalf("the reference example no longer has %q", old)
	}

	return config.LoadDir([]byte(strings.Replace(string(raw), old, repl, 1)), "../../examples/reference")
}

// The limits of the validation, on both sides (mutation testing found none of them was pinned).
func TestLimits(t *testing.T) {
	for _, c := range []struct {
		old, repl string
		ok        bool
	}{
		{"leaseTime: 12h", "leaseTime: 1m", true},
		{"leaseTime: 12h", "leaseTime: 59s", false},
		{"  refresh: 24h", "  refresh: 1m", true},
		{"  refresh: 24h", "  refresh: 59s", false},
		{"prefixLength: 56", "prefixLength: 32", true},
		// the reference delegates the /64 number 20: it fits in a /59 (32 of them), not in a /60
		{"prefixLength: 56", "prefixLength: 59", true},
		{"prefixLength: 56", "prefixLength: 60", false},
		{"prefixLength: 56", "prefixLength: 64", false},
		{"prefixLength: 56", "prefixLength: 31", false},
		{"prefixLength: 56", "prefixLength: 65", false}, // used to panic: a negative shift
		{"mtu: 1492", "mtu: 1280", true},
		{"mtu: 1492", "mtu: 1500", true},
		{"mtu: 1492", "mtu: 1279", false},
		{"mtu: 1492", "mtu: 1501", false},
		{"period: 5m", "period: 1m", true},
		{"period: 5m", "period: 59s", false},
		// the UPnP mapping range may come close to the Talos API (50000-50001), not touch it
		{`externalPorts: ["1024-49151"]`, `externalPorts: ["1024-49999"]`, true},
		{`externalPorts: ["1024-49151"]`, `externalPorts: ["1024-50000"]`, false},
		{`externalPorts: ["1024-49151"]`, `externalPorts: ["50001-50010"]`, false},
		{`externalPorts: ["1024-49151"]`, `externalPorts: ["50002-50010"]`, true},
	} {
		_, err := reference(t, c.old, c.repl)
		if (err == nil) != c.ok {
			t.Errorf("%s: accepted = %v, want %v (%v)", c.repl, err == nil, c.ok, err)
		}
	}
}

// Fields left out take the documented defaults.
func TestStatedDefaults(t *testing.T) {
	cfg, err := reference(t, "", "")
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ old string }{
		{"cacheSize: 10000"}, {"routerLifetime: 30m"}, {"defaultRoute: true"}, {"acceptRA: true"}, {"mtu: 1492"}, {"period: 5m"}, {"leaseTime: 12h"},
	} {
		// the reference states the default: without the line, the config must be the same
		without, err := reference(t, c.old, "# "+c.old)
		if err != nil {
			t.Errorf("without %q: %v", c.old, err)

			continue
		}

		if cfg.DNSServer.CacheSize != without.DNSServer.CacheSize ||
			cfg.RouterAdvertisements[0].RouterLifetime != without.RouterAdvertisements[0].RouterLifetime ||
			*cfg.PPPoE[0].DefaultRoute != *without.PPPoE[0].DefaultRoute || *cfg.PPPoE[0].IPv6 != *without.PPPoE[0].IPv6 ||
			cfg.PPPoE[0].MTU != without.PPPoE[0].MTU ||
			*cfg.PrefixDelegations[0].AcceptRA != *without.PrefixDelegations[0].AcceptRA ||
			cfg.DynamicDNS[0].Period != without.DynamicDNS[0].Period ||
			cfg.DHCPServers[0].LeaseTime != without.DHCPServers[0].LeaseTime {
			t.Errorf("without %q the config differs from the stated default", c.old)
		}
	}

	if cfg.DNSServer.CacheSize != 10000 || cfg.RouterAdvertisements[0].RouterLifetime != 30*time.Minute ||
		!*cfg.PPPoE[0].DefaultRoute || !*cfg.PrefixDelegations[0].AcceptRA || cfg.DHCPServers[0].LeaseTime != 12*time.Hour {
		t.Errorf("defaults: cache %d, RA lifetime %s, PPPoE default route %v, accept RA %v, lease time %s",
			cfg.DNSServer.CacheSize, cfg.RouterAdvertisements[0].RouterLifetime, *cfg.PPPoE[0].DefaultRoute,
			*cfg.PrefixDelegations[0].AcceptRA, cfg.DHCPServers[0].LeaseTime)
	}
}
