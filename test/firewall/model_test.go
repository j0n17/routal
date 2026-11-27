// Package firewall checks the rendered nftables ruleset against a model of what router.yaml means,
// with real packets in a real kernel: random zones' rules are generated, loaded with nft in a
// network namespace that routes between four links, and probes are sent from every side. What
// arrives must be exactly what the model says, for every probe (make fuzz-firewall).
//
// The model is written from the documentation of router.yaml, not from the renderer: rules are read
// in order, the first match decides, unmatched forwarded traffic gets forwardDefault, traffic to the
// router from a link to the Internet is closed unless a `to: self` rule opens it, and on the other
// links a port named by some `to: self` rule is closed for the zones no rule names.
package firewall

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/j0n17/routal/pkg/config"
	"github.com/j0n17/routal/pkg/env"
	"github.com/j0n17/routal/pkg/nft"
)

// side is one link of the router and the host behind it; each link is its own zone.
type side struct {
	zone, link string
	router     netip.Prefix // the router's address on the link
	host       netip.Addr   // the host behind it
}

var sides = []side{
	{"wan", "wan0", netip.MustParsePrefix("203.0.113.2/24"), netip.MustParseAddr("203.0.113.1")},
	{"lan", "lan0", netip.MustParsePrefix("192.168.1.1/24"), netip.MustParseAddr("192.168.1.10")},
	{"guest", "guest0", netip.MustParsePrefix("192.168.2.1/24"), netip.MustParseAddr("192.168.2.10")},
	{"srv", "srv0", netip.MustParsePrefix("192.168.3.1/24"), netip.MustParseAddr("192.168.3.10")},
}

var (
	ports  = []uint16{53, 80, 443, 8080, 50000}
	protos = []string{"tcp", "udp"}
)

const routerNS = "fw-router"

func hostNS(s side) string { return "fw-" + s.zone }

func sh(t *testing.T, format string, args ...any) string {
	t.Helper()

	cmd := fmt.Sprintf(format, args...)

	out, err := exec.Command("sh", "-c", cmd).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", cmd, err, out)
	}

	return string(out)
}

// probeTable records what gets in: protocol, source port and destination port of every TCP SYN
// and UDP datagram. On the router it hooks after routal's input chain, so it only sees what that
// chain let through.
const probeTable = `table inet probe {
	set seen {
		type inet_proto . inet_service . inet_service
		size 65535
		flags dynamic
	}
	chain in {
		type filter hook input priority 100; policy accept;
		tcp flags & (syn | ack) == syn add @seen { meta l4proto . tcp sport . tcp dport }
		meta l4proto udp add @seen { meta l4proto . udp sport . udp dport }
	}
}
`

func setup(t *testing.T) {
	t.Helper()

	teardown()
	t.Cleanup(teardown)

	sh(t, "ip netns add %s", routerNS)
	sh(t, "ip netns exec %s sysctl -q -w net.ipv4.ip_forward=1 net.ipv4.conf.all.rp_filter=0 net.ipv4.conf.default.rp_filter=0", routerNS)
	sh(t, "ip netns exec %s ip link set lo up", routerNS)

	if err := os.WriteFile("/tmp/fw-probe.nft", []byte(probeTable), 0o600); err != nil {
		t.Fatal(err)
	}

	sh(t, "ip netns exec %s nft -f /tmp/fw-probe.nft", routerNS)

	for _, s := range sides {
		ns := hostNS(s)
		sh(t, "ip netns add %s", ns)
		sh(t, "ip link add %s netns %s type veth peer name eth0 netns %s", s.link, routerNS, ns)
		sh(t, "ip netns exec %s ip addr add %s dev %s", routerNS, s.router, s.link)
		sh(t, "ip netns exec %s ip link set %s up", routerNS, s.link)
		sh(t, "ip netns exec %s ip link set lo up", ns)
		sh(t, "ip netns exec %s ip addr add %s/%d dev eth0", ns, s.host, s.router.Bits())
		sh(t, "ip netns exec %s ip link set eth0 up", ns)
		sh(t, "ip netns exec %s ip route add default via %s", ns, s.router.Addr())
		sh(t, "ip netns exec %s nft -f /tmp/fw-probe.nft", ns)
	}
}

func teardown() {
	for _, ns := range []string{routerNS, "fw-wan", "fw-lan", "fw-guest", "fw-srv"} {
		_ = exec.Command("ip", "netns", "del", ns).Run()
	}
}

// rule is a generated FirewallRuleConfig, kept next to its YAML for the model.
type rule struct {
	from, to   string
	block      bool
	proto      string
	ports      [][2]uint16
	src, dst   []netip.Prefix
	hasSrc     bool
	hasDst     bool
	sourceText string
}

type scenario struct {
	yaml           string
	rules          []rule
	forwardAccept  bool
	masquerade     bool
	selfPortsNamed map[string]map[uint16]bool // proto -> ports some `to: self` rule names
}

func pick[T any](r *rand.Rand, xs []T) T { return xs[r.Intn(len(xs))] }

// subnets returns nil (no restriction), or one or two prefixes that may or may not hold addr.
func subnets(r *rand.Rand, s side) []netip.Prefix {
	if r.Intn(10) < 6 {
		return nil
	}

	candidates := []netip.Prefix{
		netip.PrefixFrom(s.host, 32),
		s.router.Masked(),
		netip.PrefixFrom(s.router.Masked().Addr().Next().Next().Next(), 32), // .3: nobody
		netip.MustParsePrefix("10.99.0.0/16"),
	}

	out := []netip.Prefix{pick(r, candidates)}
	if r.Intn(4) == 0 {
		out = append(out, pick(r, candidates))
	}

	return out
}

func list[T any](xs []T) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprint(x)
	}

	return "[" + strings.Join(parts, ", ") + "]"
}

func generate(r *rand.Rand) scenario {
	var (
		b  strings.Builder
		sc = scenario{selfPortsNamed: map[string]map[uint16]bool{"tcp": {}, "udp": {}}}
	)

	doc := func(kind, body string) {
		fmt.Fprintf(&b, "---\napiVersion: v1alpha1\nkind: %s\n%s", kind, body)
	}

	for _, s := range sides {
		doc("FirewallZoneConfig", fmt.Sprintf("name: %s\nlinks: [%s]\n", s.zone, s.link))
	}

	if sc.forwardAccept = r.Intn(5) == 0; sc.forwardAccept {
		doc("FirewallConfig", "forwardDefault: accept\n")
	}

	if sc.masquerade = r.Intn(10) < 7; sc.masquerade {
		doc("MasqueradeConfig", "name: out\noutLink: wan0\n")
	}

	for i, n := 0, r.Intn(9); i < n; i++ {
		from := pick(r, sides)
		ru := rule{from: from.zone}

		body := fmt.Sprintf("name: r%d\nfrom: %s\n", i, from.zone)

		toSelf := r.Intn(3) == 0
		if toSelf {
			ru.to = config.SelfZone
			ru.proto = pick(r, protos)
		} else {
			to := pick(r, sides)
			for to.zone == from.zone {
				to = pick(r, sides)
			}

			ru.to = to.zone
			ru.block = r.Intn(3) == 0
			ru.proto = pick(r, []string{"", "tcp", "udp"})

			if ru.dst = subnets(r, to); ru.dst != nil {
				ru.hasDst = true
			}
		}

		body += fmt.Sprintf("to: %s\n", ru.to)

		if ru.block {
			body += "action: block\n"
		}

		if ru.proto != "" {
			body += fmt.Sprintf("protocol: %s\n", ru.proto)

			if toSelf || r.Intn(3) > 0 {
				var texts []string

				for j, m := 0, 1+r.Intn(2); j < m; j++ {
					lo := pick(r, ports)
					hi := lo

					if r.Intn(4) == 0 {
						if hi = pick(r, ports); hi < lo {
							lo, hi = hi, lo
						}
					}

					ru.ports = append(ru.ports, [2]uint16{lo, hi})

					if lo == hi {
						texts = append(texts, strconv.Itoa(int(lo)))
					} else {
						texts = append(texts, fmt.Sprintf("%q", fmt.Sprintf("%d-%d", lo, hi)))
					}
				}

				body += fmt.Sprintf("ports: [%s]\n", strings.Join(texts, ", "))
			}
		}

		if ru.src = subnets(r, from); ru.src != nil {
			ru.hasSrc = true
			body += fmt.Sprintf("sourceSubnets: %s\n", list(ru.src))
		}

		if ru.hasDst {
			body += fmt.Sprintf("destinationSubnets: %s\n", list(ru.dst))
		}

		if toSelf {
			for _, pr := range ru.ports {
				for _, p := range ports {
					if p >= pr[0] && p <= pr[1] {
						sc.selfPortsNamed[ru.proto][p] = true
					}
				}
			}
		}

		ru.sourceText = body
		sc.rules = append(sc.rules, ru)

		doc("FirewallRuleConfig", body)
	}

	sc.yaml = strings.TrimPrefix(b.String(), "---\n")

	return sc
}

func within(prefixes []netip.Prefix, a netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}

	return false
}

func (ru rule) matches(proto string, port uint16, src, dst netip.Addr) bool {
	if ru.proto != "" && ru.proto != proto {
		return false
	}

	if len(ru.ports) > 0 {
		in := false

		for _, pr := range ru.ports {
			in = in || (port >= pr[0] && port <= pr[1])
		}

		if !in {
			return false
		}
	}

	if ru.hasSrc && !within(ru.src, src) {
		return false
	}

	return !ru.hasDst || within(ru.dst, dst)
}

// forwarded says whether the model lets a packet from the host of one side to the host of another.
func (sc scenario) forwarded(from, to side, proto string, port uint16) bool {
	for _, ru := range sc.rules {
		if ru.from == from.zone && ru.to == to.zone && ru.matches(proto, port, from.host, to.host) {
			return !ru.block
		}
	}

	return sc.forwardAccept
}

// local says whether the model lets a packet from the host of a side to the router itself.
func (sc scenario) local(from side, proto string, port uint16, dst netip.Addr) bool {
	for _, ru := range sc.rules {
		if ru.from == from.zone && ru.to == config.SelfZone && ru.matches(proto, port, from.host, dst) {
			return true
		}
	}

	// the link to the Internet is closed by default
	if sc.masquerade && from.link == "wan0" {
		return false
	}

	// elsewhere routald only narrows the ports its rules name; the rest is Talos' ingress firewall
	return !sc.selfPortsNamed[proto][port]
}

type probe struct {
	from       side
	dst        netip.Addr
	dstNS      string
	proto      string
	port       uint16
	sport      uint16
	want       bool
	what, kind string
}

// send emits every probe from its side's namespace: a TCP SYN or a UDP datagram.
func send(t *testing.T, probes []probe) {
	t.Helper()

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	orig, err := os.Open("/proc/thread-self/ns/net")
	if err != nil {
		t.Fatal(err)
	}
	defer orig.Close()

	var open []int

	for _, s := range sides {
		ns, err := os.Open("/var/run/netns/" + hostNS(s))
		if err != nil {
			t.Fatal(err)
		}

		if err := unix.Setns(int(ns.Fd()), unix.CLONE_NEWNET); err != nil {
			t.Fatal(err)
		}

		ns.Close()

		for _, p := range probes {
			if p.from.zone != s.zone {
				continue
			}

			typ := unix.SOCK_DGRAM
			if p.proto == "tcp" {
				typ = unix.SOCK_STREAM | unix.SOCK_NONBLOCK
			}

			fd, err := unix.Socket(unix.AF_INET, typ, 0)
			if err != nil {
				t.Fatal(err)
			}

			open = append(open, fd)

			if err := unix.Bind(fd, &unix.SockaddrInet4{Port: int(p.sport)}); err != nil {
				t.Fatalf("bind %d: %v", p.sport, err)
			}

			to := &unix.SockaddrInet4{Port: int(p.port), Addr: p.dst.As4()}

			if p.proto == "tcp" {
				if err := unix.Connect(fd, to); err != nil && err != unix.EINPROGRESS {
					t.Fatalf("connect %s:%d: %v", p.dst, p.port, err)
				}
			} else if err := unix.Sendto(fd, []byte("probe"), 0, to); err != nil {
				t.Fatalf("sendto %s:%d: %v", p.dst, p.port, err)
			}
		}
	}

	if err := unix.Setns(int(orig.Fd()), unix.CLONE_NEWNET); err != nil {
		t.Fatal(err)
	}

	time.Sleep(60 * time.Millisecond)

	for _, fd := range open {
		_ = unix.Close(fd)
	}
}

// seen reads and empties the probe set of a namespace: "proto/sport/dport".
func seen(t *testing.T, ns string) map[string]bool {
	t.Helper()

	raw := sh(t, "ip netns exec %s nft -j list set inet probe seen && ip netns exec %s nft flush set inet probe seen", ns, ns)

	var doc any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}

	out := map[string]bool{}

	var walk func(v any)

	walk = func(v any) {
		switch v := v.(type) {
		case []any:
			for _, x := range v {
				walk(x)
			}
		case map[string]any:
			if c, ok := v["concat"].([]any); ok && len(c) == 3 {
				out[fmt.Sprintf("%v/%v/%v", c[0], c[1], c[2])] = true

				return
			}

			for _, x := range v {
				walk(x)
			}
		}
	}

	walk(doc)

	return out
}

func TestRulesetMatchesModel(t *testing.T) {
	if os.Getenv("ROUTAL_FIREWALL_NETNS") == "" {
		t.Skip("needs root, ip netns and nft: make fuzz-firewall")
	}

	rounds := 150
	if n, err := strconv.Atoi(os.Getenv("ROUNDS")); err == nil {
		rounds = n
	}

	seed := time.Now().UnixNano()
	if n, err := strconv.ParseInt(os.Getenv("SEED"), 10, 64); err == nil {
		seed = n
	}

	t.Logf("SEED=%d ROUNDS=%d", seed, rounds)

	setup(t)

	addrs := map[string][]netip.Prefix{}
	for _, s := range sides {
		addrs[s.link] = []netip.Prefix{s.router}
	}

	e := env.Env{
		Resolve: func(n string) string { return n },
		Addrs:   func(n string) []netip.Prefix { return addrs[n] },
	}

	var total, passed int

	for round := 0; round < rounds; round++ {
		sc := generate(rand.New(rand.NewSource(seed + int64(round))))

		cfg, err := config.Load([]byte(sc.yaml))
		if err != nil {
			t.Fatalf("round %d: the generated config is refused: %v\n%s", round, err, sc.yaml)
		}

		ruleset := nft.Render(cfg, e)

		if err := os.WriteFile("/tmp/fw-ruleset.nft", []byte(ruleset), 0o600); err != nil {
			t.Fatal(err)
		}

		sh(t, "ip netns exec %s nft -f /tmp/fw-ruleset.nft", routerNS)
		// connections of the previous round must not be taken for established ones
		sh(t, "ip netns exec %s sh -c 'conntrack -F >/dev/null 2>&1 || true'", routerNS)

		var probes []probe

		sport := uint16(20000)

		for _, from := range sides {
			for _, proto := range protos {
				for _, port := range ports {
					for _, to := range sides {
						sport++

						if to.zone == from.zone {
							// to the router, on the address of the link the packet comes in through
							probes = append(probes, probe{
								from: from, dst: from.router.Addr(), dstNS: routerNS, proto: proto, port: port, sport: sport,
								want: sc.local(from, proto, port, from.router.Addr()), kind: "to the router",
							})

							continue
						}

						probes = append(probes, probe{
							from: from, dst: to.host, dstNS: hostNS(to), proto: proto, port: port, sport: sport,
							want: sc.forwarded(from, to, proto, port), kind: "to " + to.zone,
						})
					}
				}
			}
		}

		send(t, probes)

		arrived := map[string]map[string]bool{routerNS: seen(t, routerNS)}
		for _, s := range sides {
			arrived[hostNS(s)] = seen(t, hostNS(s))
		}

		for _, p := range probes {
			total++

			got := arrived[p.dstNS][fmt.Sprintf("%s/%d/%d", p.proto, p.sport, p.port)]
			if got == p.want {
				passed++

				continue
			}

			t.Errorf("round %d (SEED=%d): from %s %s, %s %d to %s: arrived=%v, the model says %v\n--- router.yaml\n%s\n--- ruleset\n%s",
				round, seed, p.from.zone, p.kind, p.proto, p.port, p.dst, got, p.want, sc.yaml, ruleset)

			return
		}
	}

	t.Logf("%d configs, %d probes, all as the model says", rounds, passed)

	if passed != total || total == 0 {
		t.Errorf("%d of %d probes", passed, total)
	}
}
