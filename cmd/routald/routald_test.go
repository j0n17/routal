package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/j0n17/routal/pkg/config"
	"github.com/j0n17/routal/pkg/services"
)

// fakeControl is an unbound-control that appends "<command>\n<stdin>" to a log file.
func fakeControl(t *testing.T) (ctl, logPath string) {
	t.Helper()

	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	ctl = filepath.Join(dir, "unbound-control")

	script := fmt.Sprintf("#!/bin/sh\n{ echo \"== $3\"; cat; } >> %s\n", logPath)
	if err := os.WriteFile(ctl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	return ctl, logPath
}

func calls(t *testing.T, path string) string {
	t.Helper()

	raw, _ := os.ReadFile(path)
	_ = os.Remove(path)

	return string(raw)
}

func load(t *testing.T, yaml string) *config.Config {
	t.Helper()

	cfg, err := config.Load([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}

	return cfg
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if cond() {
			return
		}
	}

	t.Fatalf("timed out waiting for %s", what)
}

func TestSupervisor(t *testing.T) {
	dirs := services.Dirs{State: t.TempDir(), Run: t.TempDir()}
	s := newSupervisor(dirs)

	svc := func(conf string, testExit int) services.Service {
		return services.Service{
			Name: "daemon", File: "daemon.conf", Config: conf,
			Command:  []string{"/bin/sh", "-c", "echo running $DAEMON_ENV; exec sleep 60", "{conf}"},
			Test:     []string{"/bin/sh", "-c", fmt.Sprintf("test -f {conf} && exit %d", testExit)},
			Env:      []string{"DAEMON_ENV=x"},
			Warnings: []string{"link has no address yet"},
		}
	}

	if err := s.reconcile([]services.Service{svc("v1", 0)}); err != nil {
		t.Fatal(err)
	}

	if g := s.generation("daemon"); g != 1 {
		t.Fatalf("generation = %d, want 1", g)
	}

	if raw, _ := os.ReadFile(filepath.Join(dirs.State, "daemon.conf")); string(raw) != "v1" {
		t.Errorf("config file = %q", raw)
	}

	if st, _ := os.Stat(filepath.Join(dirs.State, "daemon.conf")); st.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %v, want 0600 (may hold secrets)", st.Mode().Perm())
	}

	// unchanged: not restarted
	_ = s.reconcile([]services.Service{svc("v1", 0)})
	if g := s.generation("daemon"); g != 1 {
		t.Errorf("restarted without a change (generation %d)", g)
	}

	// a config the daemon's check rejects is not installed; the running one stays
	if err := s.reconcile([]services.Service{svc("bad", 1)}); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("rejected config: err = %v", err)
	}

	if raw, _ := os.ReadFile(filepath.Join(dirs.State, "daemon.conf")); string(raw) != "v1" || s.generation("daemon") != 1 {
		t.Errorf("bad config replaced the running one: %q, generation %d", raw, s.generation("daemon"))
	}

	// changed: restarted
	_ = s.reconcile([]services.Service{svc("v2", 0)})
	if g := s.generation("daemon"); g != 2 {
		t.Errorf("generation after change = %d, want 2", g)
	}

	// exited: started again
	s.procs["daemon"].stop()
	_ = s.reconcile([]services.Service{svc("v2", 0)})

	if g := s.generation("daemon"); g != 3 {
		t.Errorf("generation after exit = %d, want 3", g)
	}

	// no longer configured: stopped
	_ = s.reconcile(nil)
	if s.generation("daemon") != 0 || len(s.procs) != 0 {
		t.Error("daemon still running")
	}

	_ = s.reconcile([]services.Service{svc("v3", 0)})
	s.stopAll()

	if s.generation("daemon") != 0 {
		t.Error("stopAll left it running")
	}
}

func TestBlocker(t *testing.T) {
	list := "0.0.0.0 ads.example.com\n0.0.0.0 tracker.example.com\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hosts" {
			http.NotFound(w, r)

			return
		}

		fmt.Fprint(w, list)
	}))
	defer srv.Close()

	ctl, log := fakeControl(t)
	b := &blocker{ctl: ctl, dirs: services.Dirs{State: t.TempDir()}}

	cfg := load(t, fmt.Sprintf(`apiVersion: v1alpha1
kind: DNSServerConfig
listenLinks: [lan0]
blocking:
  lists: [%s/hosts, %s/missing]
  domains: [metrics.example.com]
  allow: [ok.ads.example.com]
`, srv.URL, srv.URL))

	// unbound not running: nothing pushed
	if err := b.sync(cfg, 0); err != nil || calls(t, log) != "" {
		t.Fatalf("pushed without unbound: %v", err)
	}

	// first sync: inline names now, the list once downloaded
	_ = b.sync(cfg, 1)
	waitFor(t, "the list download", func() bool {
		_, err := os.Stat(b.cachePath(srv.URL + "/hosts"))
		return err == nil
	})
	_ = b.sync(cfg, 1)

	got := calls(t, log)
	for _, want := range []string{"ads.example.com. always_nxdomain", "tracker.example.com. always_nxdomain", "metrics.example.com. always_nxdomain", "ok.ads.example.com. transparent"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in unbound-control calls:\n%s", want, got)
		}
	}

	// unchanged: nothing sent
	if _ = b.sync(cfg, 1); calls(t, log) != "" {
		t.Error("re-pushed unchanged zones")
	}

	// a domain removed from the config is removed from unbound
	cfg.DNSServer.Blocking.Domains = nil
	_ = b.sync(cfg, 1)

	if got := calls(t, log); !strings.Contains(got, "== local_zones_remove\nmetrics.example.com.") {
		t.Errorf("not removed:\n%s", got)
	}

	// null response: every zone changes type
	cfg.DNSServer.Blocking.Response = "null"
	_ = b.sync(cfg, 1)

	if got := calls(t, log); !strings.Contains(got, "ads.example.com. always_null") {
		t.Errorf("response change not pushed:\n%s", got)
	}

	// unbound restarted: everything pushed again
	_ = b.sync(cfg, 2)
	if got := calls(t, log); strings.Contains(got, "local_zones_remove") || !strings.Contains(got, "tracker.example.com. always_null") {
		t.Errorf("not re-pushed after an unbound restart:\n%s", got)
	}

	// blocking removed: zones removed
	_ = b.sync(load(t, "apiVersion: v1alpha1\nkind: DNSServerConfig\nlistenLinks: [lan0]\n"), 2)
	if got := calls(t, log); !strings.Contains(got, "== local_zones_remove") || !strings.Contains(got, "ads.example.com.") {
		t.Errorf("zones not removed with the blocking config:\n%s", got)
	}
}

func TestDownloadErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "list.txt")
	if err := download(srv.URL, path); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("err = %v", err)
	}

	if err := download("http://127.0.0.1:1/x", path); err == nil {
		t.Error("connection refused accepted")
	}
}

func TestLeaseNames(t *testing.T) {
	ctl, log := fakeControl(t)
	dirs := services.Dirs{State: t.TempDir()}
	l := &leaseNames{ctl: ctl, dirs: dirs}

	cfg := load(t, `apiVersion: v1alpha1
kind: DNSServerConfig
listenLinks: [lan0]
localDomain: home.arpa
localRecords: [{name: taken, addresses: [192.168.1.2]}]
`)

	exp := time.Now().Add(time.Hour).Unix()
	leases := "address,hwaddr,client_id,valid_lifetime,expire,subnet_id,fqdn_fwd,fqdn_rev,hostname,state,user_context,pool_id\n" +
		fmt.Sprintf("192.168.1.100,aa:bb:cc:dd:ee:01,,3600,%d,1,0,0,phone,0,,0\n", exp) +
		fmt.Sprintf("192.168.1.101,aa:bb:cc:dd:ee:02,,3600,%d,1,0,0,taken,0,,0\n", exp)
	_ = os.WriteFile(filepath.Join(dirs.State, services.Kea4LeaseFile), []byte(leases), 0o644)

	if err := l.sync(cfg, 1); err != nil {
		t.Fatal(err)
	}

	got := calls(t, log)
	if !strings.Contains(got, "phone.home.arpa. 300 IN A 192.168.1.100") || !strings.Contains(got, "100.1.168.192.in-addr.arpa. 300 IN PTR phone.home.arpa.") {
		t.Errorf("lease not published:\n%s", got)
	}

	if strings.Contains(got, "taken.home.arpa") {
		t.Errorf("a lease took a static name:\n%s", got)
	}

	// the lease moves: old records removed, new ones added
	_ = os.WriteFile(filepath.Join(dirs.State, services.Kea4LeaseFile),
		[]byte(leases+fmt.Sprintf("192.168.1.100,aa:bb:cc:dd:ee:01,,3600,%d,1,0,0,,0,,0\n192.168.1.150,aa:bb:cc:dd:ee:01,,3600,%d,1,0,0,phone,0,,0\n", exp, exp)), 0o644)
	_ = l.sync(cfg, 1)

	got = calls(t, log)
	if !strings.Contains(got, "== local_datas_remove\nphone.home.arpa.") || !strings.Contains(got, "phone.home.arpa. 300 IN A 192.168.1.150") {
		t.Errorf("moved lease:\n%s", got)
	}

	// names off: nothing
	cfg.DNSServer.RegisterDHCPLeases = new(false)
	if _ = l.sync(cfg, 1); calls(t, log) != "" {
		t.Error("published with registerDHCPLeases false")
	}
}

func TestCommand(t *testing.T) {
	c := command([]string{"/usr/sbin/x", "-c", "{conf}", "--x={conf}"}, "/state/x.conf", []string{"A=1"})
	if strings.Join(c.Args, " ") != "/usr/sbin/x -c /state/x.conf --x=/state/x.conf" {
		t.Errorf("args = %v", c.Args)
	}

	if len(c.Env) != 2 || !strings.HasPrefix(c.Env[0], "PATH=") || c.Env[1] != "A=1" {
		t.Errorf("env = %v", c.Env)
	}
}

func TestLoadDUID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "duid")
	mac := net.HardwareAddr{0x52, 0x54, 0, 0, 0, 1}

	a, err := loadDUID(path, mac)
	if err != nil {
		t.Fatal(err)
	}

	b, err := loadDUID(path, nil) // read back, whatever the link has now
	if err != nil || string(a.ToBytes()) != string(b.ToBytes()) {
		t.Errorf("DUID not persisted: %v %v %v", a, b, err)
	}

	u, _ := loadDUID(filepath.Join(t.TempDir(), "duid"), nil) // PPP: no MAC
	if u.DUIDType().String() != "DUID-UUID" {
		t.Errorf("DUID without MAC = %s", u.DUIDType())
	}
}

// A PD client on a missing link waits, and stops when its document goes away.
func TestPDManager(t *testing.T) {
	m := &pdManager{stateDir: t.TempDir()}
	cfg := load(t, "apiVersion: v1alpha1\nkind: PrefixDelegationConfig\nname: isp\nlink: nosuchlink0\nacceptRA: false\nsubnets: [{link: lan0, id: 0}]\n")

	m.reconcile(cfg)
	m.reconcile(cfg) // unchanged: same client

	if len(m.clients) != 1 {
		t.Fatalf("clients = %d", len(m.clients))
	}

	first := m.clients["isp"]
	cfg.PrefixDelegations[0].PrefixLength = 56
	m.reconcile(cfg)

	if m.clients["isp"] == first {
		t.Error("changed config did not restart the client")
	}

	done := make(chan struct{})

	go func() { m.reconcile(&config.Config{}); close(done) }()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("client did not stop")
	}

	if len(m.clients) != 0 {
		t.Error("client left running")
	}

	m.stopAll()
}

func TestCLI(t *testing.T) {
	stdout := os.Stdout
	os.Stdout, _ = os.OpenFile(os.DevNull, os.O_WRONLY, 0)

	defer func() { os.Stdout = stdout }()

	dir := t.TempDir()
	ex := "../../examples/reference/router.yaml"

	current := filepath.Join(dir, "current.yaml")
	_ = os.WriteFile(current, []byte("apiVersion: v1alpha1\nkind: NetworkRuleConfig\nname: routal-gone\n"), 0o644)

	for _, args := range [][]string{
		{"validate", ex},
		{"render", ex},
		{"render-services", ex, filepath.Join(dir, "out")},
		{"talos-patch", ex},
		{"talos-patch", "-current", current, ex},
		{"talos-render", "../../examples/reference/talos.yaml"},
		{"addr", "eth0"}, // the test container's interface
	} {
		if err := run(args); err != nil {
			t.Errorf("routald %v: %v", args, err)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, "out", "kea-dhcp4.json")); err != nil {
		t.Error("render-services wrote nothing")
	}

	for _, args := range [][]string{
		{}, {"nope", ex}, {"validate"}, {"validate", "missing.yaml"}, {"render-services", ex},
		{"talos-render"}, {"talos-render", "missing.yaml"}, {"addr"}, {"addr", "lo"}, {"addr", "-6", "lo"},
		{"validate", "../../test/variants/invalid/zone-self.yaml"},
	} {
		if err := run(args); err == nil {
			t.Errorf("routald %v: no error", args)
		}
	}
}
