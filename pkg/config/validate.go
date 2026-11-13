package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
)

var (
	nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)
	linkRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._@-]{0,31}$`)
	// group names become nftables set names
	groupRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,30}$`)
	hostRe  = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,62})(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,62}))*$`)
)

// Validate checks every document and the references between them.
//
//nolint:gocyclo,cyclop
func (cfg *Config) Validate() error {
	var errs []error

	fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	names := map[string]map[string]bool{}
	checkName := func(kind, name string) {
		if !nameRe.MatchString(name) {
			fail("%s: invalid name %q", kind, name)

			return
		}

		if names[kind] == nil {
			names[kind] = map[string]bool{}
		}

		if names[kind][name] {
			fail("%s: duplicate name %q", kind, name)
		}

		names[kind][name] = true
	}
	checkLink := func(where, link string) {
		if !linkRe.MatchString(link) {
			fail("%s: invalid link name %q", where, link)
		}
	}

	if a := cfg.Firewall.ForwardDefault; a != ActionAccept && a != ActionBlock {
		fail("FirewallConfig: forwardDefault must be accept or block, got %q", a)
	}

	// zones
	linkZone := map[string]string{}

	for _, z := range cfg.Zones {
		checkName("FirewallZoneConfig", z.Name)

		if z.Name == SelfZone {
			fail("FirewallZoneConfig: %q is reserved for the router itself", SelfZone)
		}

		if len(z.Links) == 0 {
			fail("FirewallZoneConfig %q: links must not be empty", z.Name)
		}

		for _, l := range z.Links {
			checkLink("FirewallZoneConfig "+z.Name, l)

			if other, ok := linkZone[l]; ok {
				fail("link %q is in zones %q and %q; a link can be in one zone only", l, other, z.Name)
			}

			linkZone[l] = z.Name
		}
	}

	zoneExists := func(z string) bool { return names["FirewallZoneConfig"][z] }

	// groups
	for _, g := range cfg.AddressGroups {
		checkName("FirewallAddressGroupConfig", g.Name)

		if !groupRe.MatchString(g.Name) {
			fail("FirewallAddressGroupConfig %q: group names must be letters, digits and _ (they become nftables set names)", g.Name)
		}

		if len(g.Subnets) == 0 {
			fail("FirewallAddressGroupConfig %q: subnets must not be empty", g.Name)
		}

		errs = append(errs, checkPrefixes("FirewallAddressGroupConfig "+g.Name, g.Subnets)...)
	}

	portGroups := map[string]bool{}

	for _, g := range cfg.PortGroups {
		checkName("FirewallPortGroupConfig", g.Name)

		if !groupRe.MatchString(g.Name) {
			fail("FirewallPortGroupConfig %q: group names must be letters, digits and _ (they become nftables set names)", g.Name)
		}

		if len(g.Ports) == 0 {
			fail("FirewallPortGroupConfig %q: ports must not be empty", g.Name)
		}

		portGroups[g.Name] = true
	}

	checkGroups := func(where, dir string, literal int, groups []string, exists func(string) bool) {
		if literal > 0 && len(groups) > 0 {
			fail("%s: use either %s or %sGroups, not both", where, dir, dir)
		}

		for _, g := range groups {
			if !exists(g) {
				fail("%s: %s group %q does not exist", where, dir, g)
			}
		}
	}
	addrGroupExists := func(n string) bool { _, ok := cfg.AddressGroup(n); return ok }

	// rules
	for _, r := range cfg.Rules {
		where := fmt.Sprintf("FirewallRuleConfig %q", r.Name)
		checkName("FirewallRuleConfig", r.Name)

		if !zoneExists(r.From) {
			fail("%s: from zone %q does not exist", where, r.From)
		}

		if r.To != SelfZone && !zoneExists(r.To) {
			fail("%s: to zone %q does not exist (use %q for the router itself)", where, r.To, SelfZone)
		}

		if r.Action != ActionAccept && r.Action != ActionBlock {
			fail("%s: action must be accept or block, got %q", where, r.Action)
		}

		checkGroups(where, "source", len(r.SourceSubnets), r.SourceGroups, addrGroupExists)
		checkGroups(where, "destination", len(r.DestinationSubnets), r.DestinationGroups, addrGroupExists)
		checkGroups(where, "port", len(r.Ports), r.PortGroups, func(n string) bool { return portGroups[n] })

		errs = append(errs, checkProtoPorts(where, r.Protocol, cfg.RulePorts(r), false)...)

		if r.To == SelfZone && r.Action == ActionAccept && len(cfg.RulePorts(r)) == 0 {
			fail("%s: rules to %q need a protocol and ports (they open router ports in the Talos ingress firewall)", where, SelfZone)
		}

		errs = append(errs, checkPrefixes(where, r.SourceSubnets)...)
		errs = append(errs, checkPrefixes(where, r.DestinationSubnets)...)
	}

	// NAT
	if (len(cfg.Masquerades) > 0 || len(cfg.PortForwards) > 0) && !*cfg.Forwarding.IPv4 && !*cfg.Forwarding.IPv6 {
		fail("MasqueradeConfig/PortForwardConfig need forwarding enabled in ForwardingConfig")
	}

	for _, m := range cfg.Masquerades {
		where := fmt.Sprintf("MasqueradeConfig %q", m.Name)
		checkName("MasqueradeConfig", m.Name)
		checkLink(where, m.OutLink)
		errs = append(errs, checkPrefixes(where, m.SourceSubnets)...)

		if m.SNATAddress.IsValid() {
			for _, p := range m.SourceSubnets {
				if p.Addr().Is4() != m.SNATAddress.Is4() {
					fail("%s: snatAddress %s and source subnet %s are of different families", where, m.SNATAddress, p)
				}
			}
		}
	}

	for _, p := range cfg.PortForwards {
		where := fmt.Sprintf("PortForwardConfig %q", p.Name)
		checkName("PortForwardConfig", p.Name)
		checkLink(where, p.InLink)
		errs = append(errs, checkProtoPorts(where, p.Protocol, p.Ports, true)...)
		errs = append(errs, checkPrefixes(where, p.SourceSubnets)...)

		if !p.ToAddress.IsValid() {
			fail("%s: toAddress is required", where)
		}

		if p.ToPort != 0 && (len(p.Ports) != 1 || p.Ports[0].Lo != p.Ports[0].Hi) {
			fail("%s: toPort can only be used with a single port", where)
		}
	}

	// DHCP
	dhcpLinks := map[string]bool{}

	for _, d := range cfg.DHCPServers {
		where := fmt.Sprintf("DHCPServerConfig %q", d.Name)
		checkName("DHCPServerConfig", d.Name)
		checkLink(where, d.Link)

		if dhcpLinks[d.Link] {
			fail("%s: link %q already has a DHCP server", where, d.Link)
		}

		dhcpLinks[d.Link] = true

		if !d.RangeStart.Is4() || !d.RangeEnd.Is4() {
			fail("%s: rangeStart and rangeEnd must be IPv4 addresses", where)
		} else if d.RangeEnd.Less(d.RangeStart) {
			fail("%s: rangeEnd is before rangeStart", where)
		}

		if d.Subnet.IsValid() {
			errs = append(errs, checkPrefixes(where, []netip.Prefix{d.Subnet})...)

			if !d.Subnet.Addr().Is4() || !d.Subnet.Contains(d.RangeStart) || !d.Subnet.Contains(d.RangeEnd) {
				fail("%s: subnet %s must be IPv4 and contain the range", where, d.Subnet)
			}
		}

		for _, l := range d.StaticLeases {
			if _, err := net.ParseMAC(l.MAC); err != nil {
				fail("%s: static lease: invalid mac %q", where, l.MAC)
			}

			if !l.Address.Is4() {
				fail("%s: static lease %s: address must be IPv4", where, l.MAC)
			} else if d.Subnet.IsValid() && !d.Subnet.Contains(l.Address) {
				fail("%s: static lease %s: %s is outside subnet %s", where, l.MAC, l.Address, d.Subnet)
			}

			if l.Hostname != "" && !hostRe.MatchString(l.Hostname) {
				fail("%s: static lease %s: invalid hostname %q", where, l.MAC, l.Hostname)
			}
		}
	}

	if n := cfg.NTPServer; n != nil {
		if len(n.ListenLinks) == 0 {
			fail("NTPServerConfig: listenLinks must not be empty")
		}

		for _, l := range n.ListenLinks {
			checkLink("NTPServerConfig", l)
		}

		for _, u := range n.Upstreams {
			if !hostRe.MatchString(u) {
				fail("NTPServerConfig: invalid upstream %q", u)
			}
		}
	}

	if m := cfg.MDNSReflector; m != nil {
		if len(m.Links) < 2 {
			fail("MDNSReflectorConfig: links needs at least two links to reflect between")
		}

		for _, l := range m.Links {
			checkLink("MDNSReflectorConfig", l)
		}
	}

	if u := cfg.UPnP; u != nil {
		checkLink("UPnPConfig externalLink", u.ExternalLink)

		if len(u.InternalLinks) == 0 {
			fail("UPnPConfig: internalLinks must not be empty")
		}

		for _, l := range u.InternalLinks {
			checkLink("UPnPConfig", l)

			if l == u.ExternalLink {
				fail("UPnPConfig: link %q is both external and internal", l)
			}
		}

		for _, p := range u.ExternalPorts {
			if p.Lo <= 50001 && p.Hi >= 50000 {
				fail("UPnPConfig: externalPorts %s include the Talos API ports 50000-50001", p)
			}
		}

		errs = append(errs, checkPrefixes("UPnPConfig", u.InternalSubnets)...)
	}

	if d := cfg.DNSServer; d != nil {
		if len(d.ListenLinks) == 0 {
			fail("DNSServerConfig: listenLinks must not be empty")
		}

		for _, l := range d.ListenLinks {
			checkLink("DNSServerConfig", l)
		}

		if d.LocalDomain != "" && !hostRe.MatchString(d.LocalDomain) {
			fail("DNSServerConfig: invalid localDomain %q", d.LocalDomain)
		}

		for _, r := range d.LocalRecords {
			if !hostRe.MatchString(r.Name) || len(r.Addresses) == 0 {
				fail("DNSServerConfig: local record %q needs a valid name and addresses", r.Name)
			}
		}
	}

	raLinks := map[string]bool{}

	for _, ra := range cfg.RouterAdvertisements {
		where := fmt.Sprintf("RouterAdvertisementConfig %q", ra.Name)
		checkName("RouterAdvertisementConfig", ra.Name)
		checkLink(where, ra.Link)

		if raLinks[ra.Link] {
			fail("%s: link %q already has router advertisements", where, ra.Link)
		}

		raLinks[ra.Link] = true

		if ra.Managed {
			fail("%s: managed (stateful DHCPv6) is not supported yet; use SLAAC (default) or other: true", where)
		}

		if ra.Other && len(ra.Prefixes) == 0 {
			fail("%s: other: true (DHCPv6 options) needs explicit prefixes", where)
		}

		for _, p := range ra.Prefixes {
			if !p.Addr().Is6() {
				fail("%s: prefix %s is not IPv6", where, p)
			}
		}
	}

	return errors.Join(errs...)
}

func checkProtoPorts(where string, proto Protocol, ports []PortRange, required bool) []error {
	var errs []error

	switch proto {
	case "":
		if required {
			errs = append(errs, fmt.Errorf("%s: protocol is required (tcp or udp)", where))
		}
	case ProtocolTCP, ProtocolUDP, ProtocolICMP, ProtocolICMPv6:
	default:
		errs = append(errs, fmt.Errorf("%s: unknown protocol %q", where, proto))
	}

	if required && !proto.HasPorts() && proto != "" {
		errs = append(errs, fmt.Errorf("%s: protocol must be tcp or udp", where))
	}

	if len(ports) > 0 && !proto.HasPorts() {
		errs = append(errs, fmt.Errorf("%s: ports need protocol tcp or udp", where))
	}

	if required && len(ports) == 0 {
		errs = append(errs, fmt.Errorf("%s: ports must not be empty", where))
	}

	return errs
}

func checkPrefixes(where string, prefixes []netip.Prefix) []error {
	var errs []error

	for _, p := range prefixes {
		if p != p.Masked() {
			errs = append(errs, fmt.Errorf("%s: subnet %s has host bits set (did you mean %s?)", where, p, p.Masked()))
		}
	}

	return errs
}
