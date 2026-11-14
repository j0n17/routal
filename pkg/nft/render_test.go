package nft_test

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/j0n17/routal/pkg/config"
	"github.com/j0n17/routal/pkg/env"
	"github.com/j0n17/routal/pkg/nft"
)

func TestRenderHomeRouter(t *testing.T) {
	cfg, err := config.LoadFile("../../examples/home-router/router.yaml")
	if err != nil {
		t.Fatal(err)
	}

	// lan0 is an alias of the kernel interface eth1
	e := env.Static()
	e.Resolve = func(n string) string {
		if n == "lan0" {
			return "eth1"
		}

		return n
	}

	out := nft.Render(cfg, e)

	for _, want := range []string{
		"add table inet routal\ndelete table inet routal\n",
		"type filter hook forward priority filter; policy drop;",
		"ct state established,related accept",
		`iifname { "eth1" } oifname { "wan0" } accept comment "lan-to-wan"`,
		`iifname { "wan0" } oifname { "eth1" } ip daddr { 192.168.1.10/32 } meta l4proto tcp th dport { 443 } accept comment "wan-to-nas-https"`,
		`iifname { "eth1" } meta l4proto udp th dport { 53, 67 } accept comment "lan-dns-dhcp"`,
		`iifname { "eth1" } meta l4proto tcp th dport { 53 } accept comment "lan-dns-tcp"`,
		"iifname \"lo\" accept\n\t\tmeta l4proto udp th dport { 53, 67, 1900, 5351 } drop\n\t\tmeta l4proto tcp th dport { 53, 5000 } drop\n",
		`iifname "wan0" meta l4proto udp th dport { 1024-49151 } drop comment "upnp: no mapping"`,
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

	out := nft.Render(cfg, env.Static())

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

func TestRenderGroups(t *testing.T) {
	cfg, err := config.Load([]byte(`apiVersion: v1alpha1
kind: FirewallZoneConfig
name: lan
links: [lan0]
---
apiVersion: v1alpha1
kind: FirewallZoneConfig
name: srv
links: [srv0]
---
apiVersion: v1alpha1
kind: FirewallAddressGroupConfig
name: nas
subnets: [192.168.30.10/32, fd00:30::10/128]
---
apiVersion: v1alpha1
kind: FirewallAddressGroupConfig
name: printers
subnets: [192.168.30.50/32]
---
apiVersion: v1alpha1
kind: FirewallPortGroupConfig
name: web
ports: [80, 443, "8000-8100"]
---
apiVersion: v1alpha1
kind: FirewallRuleConfig
name: lan-to-servers
from: lan
to: srv
protocol: tcp
destinationGroups: [nas, printers]
portGroups: [web]
`))
	if err != nil {
		t.Fatal(err)
	}

	out := nft.Render(cfg, env.Static())

	for _, want := range []string{
		"set addr_nas_v4 {\n\t\ttype ipv4_addr; flags interval;\n\t\telements = { 192.168.30.10/32 }\n\t}",
		"set addr_nas_v6 {\n\t\ttype ipv6_addr; flags interval;\n\t\telements = { fd00:30::10/128 }\n\t}",
		"set port_web {\n\t\ttype inet_service; flags interval;\n\t\telements = { 80, 443, 8000-8100 }\n\t}",
		`ip daddr @addr_nas_v4 meta l4proto tcp th dport @port_web accept comment "lan-to-servers"`,
		`ip6 daddr @addr_nas_v6 meta l4proto tcp th dport @port_web accept comment "lan-to-servers"`,
		`ip daddr @addr_printers_v4 meta l4proto tcp th dport @port_web accept comment "lan-to-servers"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	if strings.Contains(out, "addr_printers_v6") {
		t.Errorf("printers has no IPv6 subnet, no v6 set or rule expected:\n%s", out)
	}
}

// Two zones sharing a router port must both keep access (regression: each rule used to drop the
// other zone's links).
func TestRenderSelfSharedPorts(t *testing.T) {
	cfg, err := config.LoadFile("../../examples/nas-vpn/router.yaml")
	if err != nil {
		t.Fatal(err)
	}

	out := nft.Render(cfg, env.Static())

	for _, want := range []string{
		`iifname { "lan0" } meta l4proto udp th dport { 53, 67, 123, 547, 5353 } accept comment "lan-dns-dhcp"`,
		`iifname { "srv0" } meta l4proto udp th dport { 53, 67, 123, 547, 5353 } accept comment "servers-dns-dhcp"`,
		`iifname { "wg0" } meta l4proto udp th dport { 53 } accept comment "vpn-dns"`,
		`iifname { "wan0" } meta l4proto udp th dport { 51820 } accept comment "wireguard"`,
		"meta l4proto udp th dport { 53, 67, 123, 547, 5353, 51820 } drop",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	if strings.Contains(out, "iifname !=") {
		t.Errorf("no negated interface drops expected:\n%s", out)
	}
}

func TestMergeRanges(t *testing.T) {
	cfg, err := config.Load([]byte(`apiVersion: v1alpha1
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
name: r1
from: a
to: self
protocol: tcp
ports: ["8000-8100", 22]
---
apiVersion: v1alpha1
kind: FirewallRuleConfig
name: r2
from: b
to: self
protocol: tcp
ports: ["8050-8200", 23]
`))
	if err != nil {
		t.Fatal(err)
	}

	if out := nft.Render(cfg, env.Static()); !strings.Contains(out, "meta l4proto tcp th dport { 22-23, 8000-8200 } drop") {
		t.Errorf("overlapping/adjacent ranges not merged:\n%s", out)
	}
}

func TestRenderReflectionAndUPnP(t *testing.T) {
	cfg, err := config.Load([]byte(`apiVersion: v1alpha1
kind: FirewallZoneConfig
name: wan
links: [wan0]
---
apiVersion: v1alpha1
kind: FirewallZoneConfig
name: lan
links: [lan0]
---
apiVersion: v1alpha1
kind: PortForwardConfig
name: web
inLink: wan0
protocol: tcp
ports: [443]
toAddress: 192.168.1.10
toPort: 8443
---
apiVersion: v1alpha1
kind: UPnPConfig
externalLink: wan0
internalLinks: [lan0]
`))
	if err != nil {
		t.Fatal(err)
	}

	e := env.Static()
	e.Addrs = func(link string) []netip.Prefix {
		if link == "wan0" {
			return []netip.Prefix{netip.MustParsePrefix("203.0.113.5/24"), netip.MustParsePrefix("fe80::1/64")}
		}

		return nil
	}

	out := nft.Render(cfg, e)

	for _, want := range []string{
		// from the inside, to the public address
		`iifname != "wan0" ip daddr { 203.0.113.5 } meta nfproto ipv4 tcp dport { 443 } dnat ip to 192.168.1.10:8443 comment "web reflection"`,
		`iifname != "wan0" ct status dnat ip daddr 192.168.1.10 meta l4proto tcp th dport { 8443 } accept comment "web reflection"`,
		`iifname != "wan0" ct status dnat ip daddr 192.168.1.10 meta l4proto tcp th dport { 8443 } masquerade comment "web reflection"`,
		// UPnP
		`iifname "wan0" oifname { "lan0" } ct status dnat accept comment "upnp"`,
		"ct state established,related accept\n\t\tiifname { \"lan0\" } meta l4proto udp th dport { 1900, 5351 } accept comment \"upnp\"",
		`iifname "wan0" meta l4proto tcp th dport { 1024-49151 } drop comment "upnp: no mapping"`,
		"add table inet routal_upnp\n",
		"add chain inet routal_upnp prerouting { type nat hook prerouting priority dstnat; policy accept; }",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	// offline: the public address is unknown, so no reflection DNAT
	if strings.Contains(nft.Render(cfg, env.Static()), "daddr { 203.0.113.5 }") {
		t.Error("reflection rendered without a known address")
	}
}

func TestRenderPPPoEClamp(t *testing.T) {
	cfg, err := config.LoadFile("../../examples/reference/router.yaml")
	if err != nil {
		t.Fatal(err)
	}

	out := nft.Render(cfg, env.Static())
	want := "ct state invalid drop"
	clamp := `oifname "ppp0" tcp flags syn / syn,rst tcp option maxseg size set rt mtu comment "mss isp"`

	if i, j := strings.Index(out, clamp), strings.Index(out, want); i < 0 || i > j {
		t.Errorf("MSS clamp missing or after the established accept:\n%s", out)
	}
}
