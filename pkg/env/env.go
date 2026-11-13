// Package env describes the runtime facts renderers need beyond router.yaml: kernel link names
// (Talos link aliases are resolved at runtime) and the addresses on each link.
package env

import (
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
)

// Env is the runtime environment a config is rendered for.
type Env struct {
	// Resolve maps a configured link name (kernel name or Talos link alias) to the kernel name.
	Resolve func(name string) string
	// Addrs returns the addresses (with prefix length) on a configured link; nil when unknown.
	Addrs func(name string) []netip.Prefix
	// Offline is set when rendering without a router (CLI, tests): renderers then use placeholders
	// for runtime facts instead of leaving parts out.
	Offline bool
}

// Static is the environment of offline rendering (CLI, tests): names are kept, addresses unknown.
func Static() Env {
	return Env{
		Resolve: func(n string) string { return n },
		Addrs:   func(string) []netip.Prefix { return nil },
		Offline: true,
	}
}

// Host is the environment of the running router: aliases from /sys/class/net, addresses from the kernel.
func Host() Env {
	e := Env{Resolve: ResolveLink}
	e.Addrs = func(name string) []netip.Prefix { return linkAddrs(e.Resolve(name)) }

	return e
}

// Addr returns the first address on a link that is inside prefix (or of the given family when
// prefix is invalid and v4 says which); false when there is none.
func (e Env) Addr(link string, within netip.Prefix, v4 bool) (netip.Addr, bool) {
	for _, p := range e.Addrs(link) {
		a := p.Addr()

		switch {
		case within.IsValid() && within.Contains(a):
			return a, true
		case !within.IsValid() && a.Is4() == v4 && !a.IsLinkLocalUnicast():
			return a, true
		}
	}

	return netip.Addr{}, false
}

// ResolveLink maps a Talos link alias (set by LinkAliasConfig as the kernel ifalias) to the kernel
// interface name; names that are not aliases are returned unchanged.
func ResolveLink(name string) string {
	if _, err := os.Stat(filepath.Join("/sys/class/net", name)); err == nil {
		return name
	}

	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return name
	}

	for _, e := range entries {
		alias, err := os.ReadFile(filepath.Join("/sys/class/net", e.Name(), "ifalias"))
		if err == nil && strings.TrimSpace(string(alias)) == name {
			return e.Name()
		}
	}

	return name
}

func linkAddrs(kernel string) []netip.Prefix {
	iface, err := net.InterfaceByName(kernel)
	if err != nil {
		return nil
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return nil
	}

	var out []netip.Prefix

	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}

		ip, ok := netip.AddrFromSlice(ipn.IP)
		if !ok {
			continue
		}

		bits, _ := ipn.Mask.Size()
		out = append(out, netip.PrefixFrom(ip.Unmap(), bits))
	}

	return out
}
