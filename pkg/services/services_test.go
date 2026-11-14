package services_test

import (
	"encoding/json"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"github.com/j0n17/routal/pkg/config"
	"github.com/j0n17/routal/pkg/env"
	"github.com/j0n17/routal/pkg/services"
)

// nasEnv is the router of examples/nas-vpn with its addresses up; srv0 is the alias of eth1.30.
func nasEnv() env.Env {
	addrs := map[string][]string{
		"lan0": {"192.168.1.1/24", "fd00:1::1/64", "fe80::1/64"},
		"srv0": {"192.168.30.1/24", "fd00:30::1/64"},
		"wg0":  {"10.8.0.1/24", "fd00:8::1/64"},
	}

	return env.Env{
		Resolve: func(n string) string {
			if n == "srv0" {
				return "eth1.30"
			}

			return n
		},
		Addrs: func(n string) []netip.Prefix {
			var out []netip.Prefix
			for _, a := range addrs[n] {
				out = append(out, netip.MustParsePrefix(a))
			}

			return out
		},
	}
}

func render(t *testing.T, path string, e env.Env) map[string]services.Service {
	t.Helper()

	cfg, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	out := map[string]services.Service{}
	for _, s := range services.Render(cfg, e, services.DefaultDirs) {
		out[s.Name] = s
	}

	return out
}

func TestRenderNAS(t *testing.T) {
	svcs := render(t, "../../examples/nas-vpn/router.yaml", nasEnv())

	for _, name := range []string{"kea-dhcp4", "unbound", "radvd", "chronyd", "avahi-daemon"} {
		if _, ok := svcs[name]; !ok {
			t.Fatalf("service %s not rendered", name)
		}

		if w := svcs[name].Warnings; len(w) > 0 {
			t.Errorf("%s warnings: %v", name, w)
		}
	}

	// Kea: subnets from the link addresses, router/DNS/NTP options pointing at the router
	var kea struct {
		Dhcp4 struct {
			InterfacesConfig struct {
				Interfaces []string `json:"interfaces"`
			} `json:"interfaces-config"`
			Subnet4 []struct {
				Subnet       string              `json:"subnet"`
				Interface    string              `json:"interface"`
				Pools        []map[string]string `json:"pools"`
				OptionData   []map[string]string `json:"option-data"`
				Reservations []map[string]string `json:"reservations"`
			} `json:"subnet4"`
		}
	}

	conf := svcs["kea-dhcp4"].Config
	if err := json.Unmarshal([]byte(conf[strings.Index(conf, "{"):]), &kea); err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(kea.Dhcp4.InterfacesConfig.Interfaces, ","); got != "lan0,eth1.30" {
		t.Errorf("interfaces = %s", got)
	}

	srv := kea.Dhcp4.Subnet4[1]
	if srv.Subnet != "192.168.30.0/24" || srv.Interface != "eth1.30" || srv.Pools[0]["pool"] != "192.168.30.100 - 192.168.30.150" {
		t.Errorf("servers subnet: %+v", srv)
	}

	opts := map[string]string{}
	for _, o := range srv.OptionData {
		opts[o["name"]] = o["data"]
	}

	if opts["routers"] != "192.168.30.1" || opts["domain-name-servers"] != "192.168.30.1" || opts["ntp-servers"] != "192.168.30.1" || opts["domain-name"] != "home.arpa" {
		t.Errorf("servers options: %v", opts)
	}

	if r := srv.Reservations[0]; r["hw-address"] != "52:54:00:00:00:10" || r["ip-address"] != "192.168.30.10" || r["hostname"] != "nas" {
		t.Errorf("reservation: %v", r)
	}

	for name, wants := range map[string][]string{
		"unbound": {
			"interface: 192.168.1.1\n", "interface: fd00:30::1\n", "interface: 10.8.0.1\n",
			`local-zone: "home.arpa." static`,
			`local-data: "nas.home.arpa. 300 IN A 192.168.30.10"`,
			`local-data-ptr: "192.168.30.10 nas.home.arpa"`,
			"forward-addr: 9.9.9.9\n", "forward-addr: 2620:fe::fe\n",
			`control-interface: "/run/routal/unbound.ctl"`,
		},
		"radvd": {
			"interface eth1.30 {", "prefix fd00:30::/64 {", "RDNSS fd00:30::1 {", "DNSSL home.arpa {", "AdvOtherConfigFlag off;",
		},
		"chronyd":      {"server time.cloudflare.com iburst", "allow 192.168.1.0/24\n", "allow 192.168.30.0/24\n"},
		"avahi-daemon": {"allow-interfaces=lan0,eth1.30\n", "enable-reflector=yes", "disable-publishing=yes"},
	} {
		for _, w := range wants {
			if !strings.Contains(svcs[name].Config, w) {
				t.Errorf("%s: missing %q in:\n%s", name, w, svcs[name].Config)
			}
		}
	}

	if strings.Contains(svcs["unbound"].Config, "fe80::1") {
		t.Error("unbound listens on a link-local address")
	}
}

func TestRenderNotReady(t *testing.T) {
	e := nasEnv()
	e.Addrs = func(string) []netip.Prefix { return nil } // links up, no addresses yet

	svcs := render(t, "../../examples/nas-vpn/router.yaml", e)

	if !strings.Contains(svcs["kea-dhcp4"].Config, `"subnet4": []`) || len(svcs["kea-dhcp4"].Warnings) != 2 {
		t.Errorf("kea should wait for the addresses: %v\n%s", svcs["kea-dhcp4"].Warnings, svcs["kea-dhcp4"].Config)
	}

	if !strings.Contains(svcs["unbound"].Config, "interface: 127.0.0.1\n") {
		t.Error("unbound should fall back to localhost")
	}
}

func TestRenderOffline(t *testing.T) {
	files, _ := filepath.Glob("../../examples/*/router.yaml")
	for _, ex := range files {
		for name, s := range render(t, ex, env.Static()) {
			if len(s.Warnings) > 0 {
				t.Errorf("%s %s: warnings offline: %v", ex, name, s.Warnings)
			}
		}
	}
}

func TestRecordReverse(t *testing.T) {
	r := services.Record{Name: "nas.home.arpa", Addr: netip.MustParseAddr("fd00:30::10")}
	if got := r.PTR(); got != "0.1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.3.0.0.0.0.d.f.ip6.arpa. 300 IN PTR nas.home.arpa." {
		t.Errorf("PTR = %s", got)
	}
}
