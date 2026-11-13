package dnsmasq_test

import (
	"strings"
	"testing"

	"github.com/j0n17/routal/pkg/config"
	"github.com/j0n17/routal/pkg/dnsmasq"
)

func TestRenderNASExample(t *testing.T) {
	cfg, err := config.LoadFile("../../examples/nas-vpn/router.yaml")
	if err != nil {
		t.Fatal(err)
	}

	if !dnsmasq.Needed(cfg) {
		t.Fatal("dnsmasq should be needed")
	}

	out := dnsmasq.Render(cfg, func(n string) string {
		if n == "lan0" {
			return "enp0s2"
		}

		return n
	}, "/var/lib/routal/dnsmasq.leases")

	for _, want := range []string{
		"interface=enp0s2\ninterface=srv0\ninterface=wg0\n",
		"server=9.9.9.9\nserver=2620:fe::fe\n",
		"domain=home.arpa\nlocal=/home.arpa/\nexpand-hosts\n",
		"dhcp-leasefile=/var/lib/routal/dnsmasq.leases\n",
		"dhcp-range=set:dhcp-lan,192.168.1.100,192.168.1.200,43200s\n",
		"dhcp-range=set:dhcp-servers,192.168.30.100,192.168.30.150,43200s\n",
		"dhcp-option=tag:dhcp-servers,option:domain-name,home.arpa\n",
		"dhcp-host=52:54:00:00:00:10,192.168.30.10,nas\n",
		"enable-ra\n",
		"dhcp-range=set:ra-lan,fd00:1::,ra-only,64\n",
		"ra-param=srv0,60,1800\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderNoDNS(t *testing.T) {
	cfg, err := config.Load([]byte(`apiVersion: v1alpha1
kind: DHCPServerConfig
name: lan
link: lan0
rangeStart: 10.0.0.10
rangeEnd: 10.0.0.20
`))
	if err != nil {
		t.Fatal(err)
	}

	out := dnsmasq.Render(cfg, nil, "/l")

	for _, want := range []string{"port=0", "dhcp-option=tag:dhcp-lan,option:dns-server #"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestNotNeeded(t *testing.T) {
	cfg, err := config.LoadFile("../../examples/home-router/router.yaml")
	if err != nil {
		t.Fatal(err)
	}

	cfg.DHCPServers, cfg.DNSServer = nil, nil
	if dnsmasq.Needed(cfg) {
		t.Error("no DHCP/DNS/RA documents: dnsmasq not needed")
	}
}
