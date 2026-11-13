package config_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/routal/routal/pkg/config"
)

func TestExamplesAreValid(t *testing.T) {
	files, err := filepath.Glob("../../examples/*/router.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no examples found: %v", err)
	}

	for _, f := range files {
		if _, err := config.LoadFile(f); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

func TestDefaults(t *testing.T) {
	cfg, err := config.LoadFile("../../examples/home-router/router.yaml")
	if err != nil {
		t.Fatal(err)
	}

	if !*cfg.Forwarding.IPv4 || !*cfg.Forwarding.IPv6 {
		t.Error("forwarding should default to on")
	}

	if cfg.Firewall.ForwardDefault != config.ActionBlock {
		t.Errorf("forwardDefault = %q, want block", cfg.Firewall.ForwardDefault)
	}

	if cfg.DHCPServers[0].LeaseTime != 12*time.Hour {
		t.Errorf("leaseTime = %v", cfg.DHCPServers[0].LeaseTime)
	}

	if got := cfg.Rules[1].Ports; len(got) != 2 || got[0].Lo != 53 || got[1].Lo != 67 {
		t.Errorf("ports = %v", got)
	}
}

func TestErrors(t *testing.T) {
	const zones = `apiVersion: v1alpha1
kind: FirewallZoneConfig
name: lan
links: [lan0]
---
apiVersion: v1alpha1
kind: FirewallZoneConfig
name: wan
links: [wan0]
---
`

	for _, tc := range []struct {
		name, doc, want string
	}{
		{"unknown kind", "apiVersion: v1alpha1\nkind: Nope\n", `unknown kind "Nope"`},
		{"bad apiVersion", "apiVersion: v2\nkind: FirewallConfig\n", "unsupported apiVersion"},
		{"unknown field", "apiVersion: v1alpha1\nkind: FirewallConfig\nlogBlockd: true\n", "field logBlockd not found"},
		{"missing zone", zones + "apiVersion: v1alpha1\nkind: FirewallRuleConfig\nname: x\nfrom: lan\nto: dmz\n", `to zone "dmz" does not exist`},
		{"link in two zones", zones + "apiVersion: v1alpha1\nkind: FirewallZoneConfig\nname: dmz\nlinks: [lan0]\n", `link "lan0" is in zones`},
		{"ports without proto", zones + "apiVersion: v1alpha1\nkind: FirewallRuleConfig\nname: x\nfrom: lan\nto: wan\nports: [22]\n", "ports need protocol"},
		{"self without ports", zones + "apiVersion: v1alpha1\nkind: FirewallRuleConfig\nname: x\nfrom: lan\nto: self\n", "need a protocol and ports"},
		{"bad port range", zones + "apiVersion: v1alpha1\nkind: FirewallRuleConfig\nname: x\nfrom: lan\nto: wan\nprotocol: tcp\nports: [\"9-1\"]\n", "invalid port range"},
		{"host bits", "apiVersion: v1alpha1\nkind: MasqueradeConfig\nname: x\noutLink: wan0\nsourceSubnets: [192.168.1.1/24]\n", "host bits set"},
		{"nat without forwarding", "apiVersion: v1alpha1\nkind: ForwardingConfig\nipv4: false\nipv6: false\n---\napiVersion: v1alpha1\nkind: MasqueradeConfig\nname: x\noutLink: wan0\n", "need forwarding enabled"},
		{"toPort with range", "apiVersion: v1alpha1\nkind: PortForwardConfig\nname: x\ninLink: wan0\nprotocol: tcp\nports: [\"80-90\"]\ntoAddress: 10.0.0.1\ntoPort: 8080\n", "single port"},
		{"two dhcp on link", "apiVersion: v1alpha1\nkind: DHCPServerConfig\nname: a\nlink: lan0\nrangeStart: 10.0.0.10\nrangeEnd: 10.0.0.20\n---\napiVersion: v1alpha1\nkind: DHCPServerConfig\nname: b\nlink: lan0\nrangeStart: 10.0.0.30\nrangeEnd: 10.0.0.40\n", "already has a DHCP server"},
		{"duplicate singleton", "apiVersion: v1alpha1\nkind: FirewallConfig\n---\napiVersion: v1alpha1\nkind: FirewallConfig\n", "duplicate FirewallConfig"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load([]byte(tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}
