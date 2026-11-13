package nft_test

import (
	"strings"
	"testing"

	"github.com/j0n17/routal/pkg/config"
	"github.com/j0n17/routal/pkg/nft"
)

func TestRenderHomeRouter(t *testing.T) {
	cfg, err := config.LoadFile("../../examples/home-router/router.yaml")
	if err != nil {
		t.Fatal(err)
	}

	// lan0 is an alias of the kernel interface eth1
	out := nft.Render(cfg, func(n string) string {
		if n == "lan0" {
			return "eth1"
		}

		return n
	})

	for _, want := range []string{
		"add table inet routal\ndelete table inet routal\n",
		"type filter hook forward priority filter; policy drop;",
		"ct state established,related accept",
		`iifname { "eth1" } oifname { "wan0" } accept comment "lan-to-wan"`,
		`iifname { "wan0" } oifname { "eth1" } ip daddr { 192.168.1.10/32 } meta l4proto tcp th dport { 443 } accept comment "wan-to-nas-https"`,
		`iifname != { "eth1", "lo" } meta l4proto udp th dport { 53, 67 } drop`,
		`iifname != { "eth1", "lo" } meta l4proto tcp th dport { 53 } drop`,
		`iifname "wan0" meta nfproto ipv4 tcp dport { 443 } dnat ip to 192.168.1.10 comment "nas-https"`,
		`oifname "wan0" ip saddr { 192.168.1.0/24 } masquerade comment "lan-out"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderFamilies(t *testing.T) {
	cfg, err := config.Load([]byte(`apiVersion: v1alpha1
kind: FirewallConfig
forwardDefault: accept
---
apiVersion: v1alpha1
kind: FirewallZoneConfig
name: a
links: [a0]
---
apiVersion: v1alpha1
kind: FirewallZoneConfig
name: b
links: [b0]
---
apiVersion: v1alpha1
kind: FirewallRuleConfig
name: dual
from: a
to: b
action: block
sourceSubnets: [10.0.0.0/8, fd00::/8]
log: true
---
apiVersion: v1alpha1
kind: MasqueradeConfig
name: fixed
outLink: b0
snatAddress: 203.0.113.7
---
apiVersion: v1alpha1
kind: PortForwardConfig
name: v6
inLink: b0
protocol: udp
ports: [53]
toAddress: fd00::53
toPort: 5353
`))
	if err != nil {
		t.Fatal(err)
	}

	out := nft.Render(cfg, nil)

	for _, want := range []string{
		"policy accept;",
		`ip saddr { 10.0.0.0/8 } log prefix "routal dual: " drop comment "dual"`,
		`ip6 saddr { fd00::/8 } log prefix "routal dual: " drop comment "dual"`,
		`oifname "b0" meta nfproto ipv4 snat ip to 203.0.113.7 comment "fixed"`,
		`meta nfproto ipv6 udp dport { 53 } dnat ip6 to [fd00::53]:5353 comment "v6"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
