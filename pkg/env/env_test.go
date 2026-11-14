package env_test

import (
	"net/netip"
	"testing"

	"github.com/j0n17/routal/pkg/env"
)

func TestStatic(t *testing.T) {
	e := env.Static()
	if e.Resolve("lan0") != "lan0" || e.Addrs("lan0") != nil || !e.Offline {
		t.Errorf("static env = %+v", e)
	}
}

func TestHost(t *testing.T) {
	e := env.Host()

	// the loopback always exists; names that are neither a link nor an alias are kept
	if e.Resolve("lo") != "lo" || e.Resolve("no-such-link") != "no-such-link" {
		t.Error("Resolve changed a name")
	}

	found := false

	for _, p := range e.Addrs("lo") {
		found = found || p == netip.MustParsePrefix("127.0.0.1/8")
	}

	if !found {
		t.Errorf("lo addresses = %v", e.Addrs("lo"))
	}

	if e.Addrs("no-such-link") != nil {
		t.Error("addresses on a missing link")
	}
}

func TestAddr(t *testing.T) {
	e := env.Static()
	e.Addrs = func(string) []netip.Prefix {
		return []netip.Prefix{
			netip.MustParsePrefix("fe80::1/64"), netip.MustParsePrefix("192.168.1.1/24"),
			netip.MustParsePrefix("10.0.0.1/8"), netip.MustParsePrefix("fd00::1/64"),
		}
	}

	for _, c := range []struct {
		within string
		v4     bool
		want   string
	}{
		{"", true, "192.168.1.1"},
		{"", false, "fd00::1"}, // never the link-local address
		{"10.0.0.0/8", true, "10.0.0.1"},
		{"172.16.0.0/12", true, ""},
	} {
		var within netip.Prefix
		if c.within != "" {
			within = netip.MustParsePrefix(c.within)
		}

		a, ok := e.Addr("lan0", within, c.v4)
		if (c.want == "") == ok || (ok && a.String() != c.want) {
			t.Errorf("Addr(%q, v4=%v) = %v %v, want %q", c.within, c.v4, a, ok, c.want)
		}
	}
}
