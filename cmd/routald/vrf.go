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

// vrfServices lets the router's daemons serve links that are in a VRF (Talos VRFConfig). The
// daemons run in the default routing domain, so:
//   - sockets must accept packets that came in through a VRF (tcp/udp_l3mdev_accept),
//   - daemons must be able to bind the router's addresses of those links, which are not local in
//     the main table (ip_nonlocal_bind),
//   - answers sent from those addresses must be routed in the VRF's table: one routing rule per
//     address.
type vrfServices struct {
	logged string
}

func (v *vrfServices) reconcile(cfg *config.Config, e env.Env) error {
	want := map[string]*netlink.Rule{}

	var desc []string

	for _, name := range cfg.ServiceLinks() {
		link, err := netlink.LinkByName(e.Resolve(name))
		if err != nil || link.Attrs().MasterIndex == 0 {
			continue
		}

		master, err := netlink.LinkByIndex(link.Attrs().MasterIndex)
		if err != nil {
			continue
		}

		vrf, ok := master.(*netlink.Vrf)
		if !ok {
			continue // a bridge or a bond
		}

		addrs, err := netlink.AddrList(link, netlink.FAMILY_ALL)
		if err != nil {
			return fmt.Errorf("vrf: %s: %w", name, err)
		}

		for _, a := range addrs {
			if a.IP.IsLinkLocalUnicast() {
				continue
			}

			r := netlink.NewRule()
			r.Priority = vrfRulePriority
			r.Table = int(vrf.Table)
			r.Family = netlink.FAMILY_V6
			r.Src = &net.IPNet{IP: a.IP, Mask: net.CIDRMask(128, 128)}

			if ip4 := a.IP.To4(); ip4 != nil {
				r.Family = netlink.FAMILY_V4
				r.Src = &net.IPNet{IP: ip4, Mask: net.CIDRMask(32, 32)}
			}

			want[ruleKey(r)] = r
			desc = append(desc, fmt.Sprintf("%s on %s (%s, table %d)", a.IP, name, vrf.Name, vrf.Table))
		}
	}

	existing, err := netlink.RuleList(netlink.FAMILY_ALL)
	if err != nil {
		return fmt.Errorf("vrf: listing routing rules: %w", err)
	}

	var errs []error

	for i := range existing {
		r := &existing[i]
		if r.Priority != vrfRulePriority || r.Src == nil {
			continue
		}

		if _, ok := want[ruleKey(r)]; ok {
			delete(want, ruleKey(r))
		} else if err := netlink.RuleDel(r); err != nil {
			errs = append(errs, fmt.Errorf("vrf: removing rule from %s: %w", r.Src, err))
		}
	}

	for _, r := range want {
		if err := netlink.RuleAdd(r); err != nil {
			errs = append(errs, fmt.Errorf("vrf: adding rule from %s: %w", r.Src, err))
		}
	}

	if len(desc) > 0 {
		for _, s := range []string{"net/ipv4/tcp_l3mdev_accept", "net/ipv4/udp_l3mdev_accept", "net/ipv4/ip_nonlocal_bind", "net/ipv6/ip_nonlocal_bind"} {
			if err := setSysctl(s, true); err != nil {
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
