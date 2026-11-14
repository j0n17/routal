package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"

	"github.com/j0n17/routal/pkg/env"
	"github.com/j0n17/routal/pkg/services"
)

// fakeVRF is a kernel with links in VRFs and routing rules.
type fakeVRF struct {
	links   map[string]*vrfLink
	have    []netlink.Rule
	sysctls []string
	fail    error
}

func (k *fakeVRF) link(name string) (*vrfLink, error) { return k.links[name], k.fail }
func (k *fakeVRF) rules() ([]netlink.Rule, error)     { return slices.Clone(k.have), k.fail }

func (k *fakeVRF) addRule(r *netlink.Rule) error {
	k.have = append(k.have, *r)

	return k.fail
}

func (k *fakeVRF) delRule(r *netlink.Rule) error {
	k.have = slices.DeleteFunc(k.have, func(o netlink.Rule) bool {
		return o.Priority == r.Priority && ruleKey(&o) == ruleKey(r)
	})

	return k.fail
}

func (k *fakeVRF) sysctl(name string) error {
	k.sysctls = append(k.sysctls, name)

	return k.fail
}

func (k *fakeVRF) ours() []string {
	var out []string

	for i := range k.have {
		if k.have[i].Priority == vrfRulePriority {
			out = append(out, ruleKey(&k.have[i]))
		}
	}

	slices.Sort(out)

	return out
}

func TestVRFServices(t *testing.T) {
	cfg := load(t, `apiVersion: v1alpha1
kind: DNSServerConfig
listenLinks: [lan0, lab0]
---
apiVersion: v1alpha1
kind: NTPServerConfig
listenLinks: [lab0]
`)

	foreign := netlink.NewRule()
	foreign.Priority = 1000
	foreign.Table = 100

	k := &fakeVRF{links: map[string]*vrfLink{}, have: []netlink.Rule{*foreign}}
	v := &vrfServices{kernel: k}

	// no link in a VRF: nothing is touched
	if err := v.reconcile(cfg, env.Static()); err != nil || len(k.ours()) != 0 || len(k.sysctls) != 0 {
		t.Fatalf("without VRF: %v %v %v", err, k.ours(), k.sysctls)
	}

	// lab0 joins a VRF: a rule per address (not the link-local one), and the sysctls
	k.links["lab0"] = &vrfLink{VRF: "vrf-lab", Table: 100, Addrs: []net.IP{
		net.ParseIP("192.168.30.1"), net.ParseIP("fd00:30::1"), net.ParseIP("fe80::1"),
	}}

	if err := v.reconcile(cfg, env.Static()); err != nil {
		t.Fatal(err)
	}

	want := []string{"192.168.30.1/32 table 100", "fd00:30::1/128 table 100"}
	if !slices.Equal(k.ours(), want) || !slices.Equal(k.sysctls, vrfSysctls) {
		t.Errorf("rules = %v, sysctls = %v", k.ours(), k.sysctls)
	}

	// again: nothing added twice
	if err := v.reconcile(cfg, env.Static()); err != nil || len(k.have) != 3 {
		t.Errorf("second run: %v, %d rules", err, len(k.have))
	}

	// the address changes: the old rule goes, rules of others stay
	k.links["lab0"].Addrs = []net.IP{net.ParseIP("192.168.31.1")}

	if err := v.reconcile(cfg, env.Static()); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(k.ours(), []string{"192.168.31.1/32 table 100"}) || len(k.have) != 2 {
		t.Errorf("after the change: %v (%d rules)", k.ours(), len(k.have))
	}

	// the link leaves the VRF: no rule left
	delete(k.links, "lab0")

	if err := v.reconcile(cfg, env.Static()); err != nil || len(k.ours()) != 0 || v.logged != "" {
		t.Errorf("after leaving: %v %v", err, k.ours())
	}

	// kernel errors are reported
	k.fail = errors.New("no netlink")
	if err := v.reconcile(cfg, env.Static()); err == nil {
		t.Error("link error not reported")
	}

	k2 := &failingRules{fakeVRF: fakeVRF{links: map[string]*vrfLink{"lab0": {VRF: "vrf-lab", Table: 100, Addrs: []net.IP{net.ParseIP("192.168.30.1")}}}}}
	k2.have = []netlink.Rule{staleRule()}

	err := (&vrfServices{kernel: k2}).reconcile(cfg, env.Static())
	for _, want := range []string{"adding rule", "removing rule", "sysctl"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v does not mention %q", err, want)
		}
	}

	k2.listFails = true
	if err := (&vrfServices{kernel: k2}).reconcile(cfg, env.Static()); err == nil || !strings.Contains(err.Error(), "listing") {
		t.Errorf("list error: %v", err)
	}

	// the real kernel: the test container has no VRF, so nothing to do
	if err := (&vrfServices{}).reconcile(cfg, env.Static()); err != nil {
		t.Errorf("host: %v", err)
	}

	if l, err := (hostVRF{}).link("lo"); l != nil || err != nil {
		t.Errorf("lo in a VRF: %v %v", l, err)
	}
}

func staleRule() netlink.Rule {
	r := netlink.NewRule()
	r.Priority = vrfRulePriority
	r.Table = 100
	r.Src = &net.IPNet{IP: net.ParseIP("10.9.9.9").To4(), Mask: net.CIDRMask(32, 32)}

	return *r
}

// failingRules finds links but cannot change anything.
type failingRules struct {
	fakeVRF
	listFails bool
}

func (k *failingRules) rules() ([]netlink.Rule, error) {
	if k.listFails {
		return nil, errors.New("denied")
	}

	return k.have, nil
}

func (k *failingRules) addRule(*netlink.Rule) error { return errors.New("denied") }
func (k *failingRules) delRule(*netlink.Rule) error { return errors.New("denied") }
func (k *failingRules) sysctl(string) error         { return errors.New("sysctl denied") }

// fakeNft logs its arguments and its input; it fails on what the files in its directory say.
func fakeNft(t *testing.T) (bin, dir string) {
	t.Helper()

	dir = t.TempDir()
	bin = filepath.Join(dir, "nft")
	script := fmt.Sprintf(`#!/bin/sh
echo "== $*" >> %[1]s/calls.log
if [ "$1" = list ]; then [ -e %[1]s/table-exists ]; exit; fi
cat >> %[1]s/calls.log
if [ -e %[1]s/fail ]; then echo "syntax error" >&2; exit 1; fi
`, dir)

	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	return bin, dir
}

func newTestAgent(t *testing.T, nftBin, config string) *agent {
	t.Helper()

	dirs := services.Dirs{State: t.TempDir(), Run: t.TempDir()}
	path := filepath.Join(t.TempDir(), "router.yaml")

	if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	return &agent{
		configPath: path, nftBin: nftBin, dirs: dirs, services: newSupervisor(dirs),
		names: &leaseNames{ctl: "/bin/false", dirs: dirs}, pd: &pdManager{stateDir: dirs.State},
		blocker: &blocker{ctl: "/bin/false", dirs: dirs}, vrf: vrfServices{kernel: &fakeVRF{}},
	}
}

// When the config cannot be applied and no rules are in place, forwarding is blocked; rules that
// are in place are kept.
func TestAgentFailsClosed(t *testing.T) {
	bin, dir := fakeNft(t)
	log := filepath.Join(dir, "calls.log")

	a := newTestAgent(t, bin, "apiVersion: v1alpha1\nkind: Nope\n")
	if err := a.reconcile(); err == nil {
		t.Fatal("invalid config applied")
	}

	got := calls(t, log)
	if !strings.Contains(got, "== list table inet routal") || !strings.Contains(got, "policy drop;") || strings.Contains(got, "accept") {
		t.Errorf("forwarding not blocked:\n%s", got)
	}

	// rules from an earlier run are still there: they are kept
	_ = os.WriteFile(filepath.Join(dir, "table-exists"), nil, 0o644)

	if err := a.reconcile(); err == nil || strings.Contains(calls(t, log), "policy drop") {
		t.Error("existing rules replaced by the closed ruleset")
	}

	// a ruleset nft rejects, with nothing in place: closed too, and that failing is only logged
	_ = os.Remove(filepath.Join(dir, "table-exists"))
	_ = os.WriteFile(filepath.Join(dir, "fail"), nil, 0o644)

	b := newTestAgent(t, bin, "apiVersion: v1alpha1\nkind: FirewallConfig\n")
	if err := b.reconcile(); err == nil || !strings.Contains(err.Error(), "syntax error") {
		t.Errorf("nft error = %v", err)
	}

	if got := calls(t, log); strings.Count(got, "== -f -") != 2 {
		t.Errorf("want the ruleset then the closed ruleset:\n%s", got)
	}

	// a good config: applied once, then left alone
	_ = os.Remove(filepath.Join(dir, "fail"))
	_ = b.reconcile() // the forwarding sysctls may be read-only in the test container
	first := calls(t, log)

	if !strings.Contains(first, "chain forward") {
		t.Errorf("ruleset not applied:\n%s", first)
	}

	if b.applied != [32]byte{} {
		_ = b.reconcile()

		if again := calls(t, log); strings.Contains(again, "-f -") {
			t.Errorf("unchanged ruleset applied again:\n%s", again)
		}

		b.failClosed() // rules applied: nothing to do

		if calls(t, log) != "" {
			t.Error("failClosed touched applied rules")
		}
	}

	// run reconciles until the context ends
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := a.run(ctx); err != nil {
		t.Errorf("run: %v", err)
	}
}

// A daemon that ignores SIGTERM is killed with the helpers it started.
func TestStopKills(t *testing.T) {
	old := stopGrace
	stopGrace = 300 * time.Millisecond

	defer func() { stopGrace = old }()

	s := newSupervisor(services.Dirs{State: t.TempDir(), Run: t.TempDir()})
	svc := services.Service{Name: "stubborn", File: "stubborn.conf", Command: []string{"/bin/sh", "-c", `trap "" TERM; sleep 600 & wait`}}

	if err := s.reconcile([]services.Service{svc}); err != nil {
		t.Fatal(err)
	}

	time.Sleep(300 * time.Millisecond) // let the shell install its trap

	start := time.Now()
	s.stopAll()

	if d := time.Since(start); d < stopGrace || d > 5*time.Second || s.procs["stubborn"].running() {
		t.Errorf("stop took %s, running: %v", d, s.procs["stubborn"].running())
	}

	// a command that cannot start, a state directory that cannot be created
	svc.Name, svc.Command = "missing", []string{"/no/such/daemon"}
	if err := s.reconcile([]services.Service{svc}); err == nil {
		t.Error("missing binary started")
	}

	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, nil, 0o644)

	bad := newSupervisor(services.Dirs{State: filepath.Join(file, "state"), Run: t.TempDir()})
	if err := bad.reconcile([]services.Service{svc}); err == nil {
		t.Error("state directory below a file accepted")
	}
}

func TestDownloadLimits(t *testing.T) {
	old := maxListSize
	maxListSize = 16

	defer func() { maxListSize = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.Repeat("0.0.0.0 ads.example.com\n", 10))
	}))
	defer srv.Close()

	dir := t.TempDir()

	if err := download(srv.URL, filepath.Join(dir, "list")); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("oversized list: %v", err)
	}

	if err := download("http://bad host/", filepath.Join(dir, "list")); err == nil {
		t.Error("invalid URL accepted")
	}

	maxListSize = old
	file := filepath.Join(dir, "file")
	_ = os.WriteFile(file, nil, 0o644)

	if err := download(srv.URL, filepath.Join(file, "sub", "list")); err == nil {
		t.Error("cache directory below a file accepted")
	}

	_ = os.Mkdir(filepath.Join(dir, "taken.new"), 0o755)

	if err := download(srv.URL, filepath.Join(dir, "taken")); err == nil {
		t.Error("cache file that cannot be written accepted")
	}

	// without resolv.conf (a Talos extension service), names are resolved by Talos' host DNS
	oldConf := resolvConf
	resolvConf = filepath.Join(dir, "no-resolv.conf")

	defer func() { resolvConf = oldConf }()

	c := httpClient()
	if c.Transport == nil {
		t.Fatal("default resolver used without resolv.conf")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// nothing answers on the host DNS address here: the lookup must go there and fail
	if conn, err := c.Transport.(*http.Transport).DialContext(ctx, "tcp", "blocklist.invalid:80"); err == nil {
		conn.Close() //nolint:errcheck
		t.Error("name resolved without the host DNS")
	}
}

// unbound-control failing is reported, and tried again on the next run.
func TestControlFailures(t *testing.T) {
	dirs := services.Dirs{State: t.TempDir()}

	if err := unboundControl("/bin/false", "x.conf", "local_zones", []string{"a"}); err == nil {
		t.Error("failing unbound-control accepted")
	}

	cfg := load(t, `apiVersion: v1alpha1
kind: DNSServerConfig
listenLinks: [lan0]
localDomain: home.arpa
blocking:
  domains: [ads.example.com]
`)

	b := &blocker{ctl: "/bin/false", dirs: dirs}
	if err := b.sync(cfg, 1); err == nil {
		t.Error("blocker: failing unbound-control accepted")
	}

	exp := time.Now().Add(time.Hour).Unix()
	leases := fmt.Sprintf("address,hwaddr,client_id,valid_lifetime,expire,subnet_id,fqdn_fwd,fqdn_rev,hostname,state\n192.168.1.100,aa:bb:cc:dd:ee:01,,3600,%d,1,0,0,phone,0\n", exp)
	lease := filepath.Join(dirs.State, services.Kea4LeaseFile)
	_ = os.WriteFile(lease, []byte(leases), 0o644)

	l := &leaseNames{ctl: "/bin/false", dirs: dirs}
	if err := l.sync(cfg, 1); err == nil {
		t.Error("lease names: failing unbound-control accepted")
	}

	// the names are pushed, then the lease goes away and removing them fails
	ctl, _ := fakeControl(t)
	l.ctl = ctl

	if err := l.sync(cfg, 1); err != nil {
		t.Fatal(err)
	}

	_ = os.WriteFile(lease, []byte("address,hostname,expire,state\n"), 0o644)
	l.ctl = "/bin/false"

	if err := l.sync(cfg, 1); err == nil {
		t.Error("lease names: failing removal accepted")
	}

	// a lease file that cannot be read
	_ = os.Remove(lease)
	_ = os.Mkdir(lease, 0o755)

	if err := l.sync(cfg, 1); err == nil {
		t.Error("unreadable lease file accepted")
	}
}

func TestPPPDevice(t *testing.T) {
	old := pppDevice
	defer func() { pppDevice = old }()

	pppDevice = filepath.Join(t.TempDir(), "ppp")
	ensurePPPDevice() // created where the container may create devices, logged otherwise
	ensurePPPDevice()

	pppDevice = filepath.Join(t.TempDir(), "missing", "ppp")
	ensurePPPDevice()

	if _, err := os.Stat(pppDevice); err == nil {
		t.Error("device created in a missing directory")
	}
}

func TestCLIErrors(t *testing.T) {
	stdout := os.Stdout
	os.Stdout, _ = os.OpenFile(os.DevNull, os.O_WRONLY, 0)

	defer func() { os.Stdout = stdout }()

	dir := t.TempDir()
	ex := "../../examples/reference/router.yaml"

	file := filepath.Join(dir, "file")
	_ = os.WriteFile(file, []byte("privateKeyFile: missing.key\n"), 0o644)

	garbage := filepath.Join(dir, "current.yaml")
	_ = os.WriteFile(garbage, []byte("kind: [\n"), 0o644)

	for _, args := range [][]string{
		{"talos-render", file},
		{"talos-patch", "-current", filepath.Join(dir, "missing.yaml"), ex},
		{"talos-patch", "-current", garbage, ex},
		{"render-services", ex, filepath.Join(file, "out")},
	} {
		if err := run(args); err == nil {
			t.Errorf("routald %v: no error", args)
		}
	}

	// a directory that cannot be written to
	ro := filepath.Join(dir, "out")
	_ = os.Mkdir(ro, 0o755)
	_ = os.Mkdir(filepath.Join(ro, "kea-dhcp4.json"), 0o755)

	if err := run([]string{"render-services", ex, ro}); err == nil {
		t.Error("render-services over a directory: no error")
	}
}
