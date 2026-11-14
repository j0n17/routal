package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"slices"
	"strings"

	"github.com/vishvananda/netlink"

	"github.com/j0n17/routal/pkg/config"
	"github.com/j0n17/routal/pkg/env"
)

// vrfRulePriority is the priority of the routing rules routald owns: before the kernel's VRF rule
// (1000) and the main table.
const vrfRulePriority = 900

// vrfSysctls let daemons of the default routing domain accept packets that came in through a VRF
// and bind addresses that are only local in a VRF's table.
var vrfSysctls = []string{"net/ipv4/tcp_l3mdev_accept", "net/ipv4/udp_l3mdev_accept", "net/ipv4/ip_nonlocal_bind", "net/ipv6/ip_nonlocal_bind"}

// vrfServices lets the router's daemons serve links that are in a VRF (Talos VRFConfig). The
// daemons run in the default routing domain, so:
//   - sockets must accept packets that came in through a VRF (tcp/udp_l3mdev_accept),
//   - daemons must be able to bind the router's addresses of those links, which are not local in
//     the main table (ip_nonlocal_bind),
//   - answers sent from those addresses must be routed in the VRF's table: one routing rule per
//     address.
type vrfServices struct {
	kernel vrfKernel // nil: the host
	logged string
}

// vrfLink is a link enslaved to a VRF.
type vrfLink struct {
	VRF   string
	Table uint32
	Addrs []net.IP
}

// vrfKernel is what vrfServices needs from the kernel.
type vrfKernel interface {
	link(name string) (*vrfLink, error) // nil when the link does not exist or is not in a VRF
	rules() ([]netlink.Rule, error)
	addRule(r *netlink.Rule) error
	delRule(r *netlink.Rule) error
	sysctl(name string) error
}

func (v *vrfServices) reconcile(cfg *config.Config, e env.Env) error {
	k := v.kernel
	if k == nil {
		k = hostVRF{}
	}

	want := map[string]*netlink.Rule{}

	var desc []string

	for _, name := range cfg.ServiceLinks() {
		l, err := k.link(e.Resolve(name))
		if err != nil {
			return fmt.Errorf("vrf: %s: %w", name, err)
		}

		if l == nil {
			continue
		}

		for _, ip := range l.Addrs {
			if ip.IsLinkLocalUnicast() {
				continue
			}

			r := netlink.NewRule()
			r.Priority = vrfRulePriority
			r.Table = int(l.Table)
			r.Family = netlink.FAMILY_V6
			r.Src = &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}

			if ip4 := ip.To4(); ip4 != nil {
				r.Family = netlink.FAMILY_V4
				r.Src = &net.IPNet{IP: ip4, Mask: net.CIDRMask(32, 32)}
			}

			want[ruleKey(r)] = r
			desc = append(desc, fmt.Sprintf("%s on %s (%s, table %d)", ip, name, l.VRF, l.Table))
		}
	}

	existing, err := k.rules()
	if err != nil {
		return fmt.Errorf("vrf: listing routing rules: %w", err)
	}

	var errs []error

	for i := range existing {
		r := &existing[i]
		if r.Priority != vrfRulePriority || r.Src == nil {
			continue // not ours
		}

		if _, ok := want[ruleKey(r)]; ok {
			delete(want, ruleKey(r))
		} else if err := k.delRule(r); err != nil {
			errs = append(errs, fmt.Errorf("vrf: removing rule from %s: %w", r.Src, err))
		}
	}

	for _, r := range want {
		if err := k.addRule(r); err != nil {
			errs = append(errs, fmt.Errorf("vrf: adding rule from %s: %w", r.Src, err))
		}
	}

	if len(desc) > 0 {
		for _, s := range vrfSysctls {
			if err := k.sysctl(s); err != nil {
				errs = append(errs, fmt.Errorf("vrf: %w", err))
			}
		}
	}

	slices.Sort(desc)

	if s := strings.Join(desc, ", "); s != v.logged && len(errs) == 0 {
		v.logged = s

		if s != "" {
			log.Printf("vrf: serving %s", s)
		}
	}

	return errors.Join(errs...)
}

func ruleKey(r *netlink.Rule) string {
	return fmt.Sprintf("%s table %d", r.Src, r.Table)
}

// hostVRF is the running kernel, through netlink.
type hostVRF struct{}

func (hostVRF) link(name string) (*vrfLink, error) {
	link, err := netlink.LinkByName(name)
	if err != nil || link.Attrs().MasterIndex == 0 {
		return nil, nil //nolint:nilerr // not there yet: nothing to serve
	}

	master, err := netlink.LinkByIndex(link.Attrs().MasterIndex)
	if err != nil {
		return nil, nil //nolint:nilerr
	}

	vrf, ok := master.(*netlink.Vrf)
	if !ok {
		return nil, nil // a bridge or a bond
	}

	addrs, err := netlink.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return nil, err
	}

	l := &vrfLink{VRF: vrf.Name, Table: vrf.Table}
	for _, a := range addrs {
		l.Addrs = append(l.Addrs, a.IP)
	}

	return l, nil
}

func (hostVRF) rules() ([]netlink.Rule, error) { return netlink.RuleList(netlink.FAMILY_ALL) }
func (hostVRF) addRule(r *netlink.Rule) error  { return netlink.RuleAdd(r) }
func (hostVRF) delRule(r *netlink.Rule) error  { return netlink.RuleDel(r) }
func (hostVRF) sysctl(name string) error       { return setSysctl(name, true) }
