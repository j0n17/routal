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

		errs = append(errs, checkProtoPorts(where, r.Protocol, r.Ports, false)...)

		if r.To == SelfZone && r.Action == ActionAccept && len(r.Ports) == 0 {
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

		for _, l := range d.StaticLeases {
			if _, err := net.ParseMAC(l.MAC); err != nil {
				fail("%s: static lease: invalid mac %q", where, l.MAC)
			}

			if !l.Address.Is4() {
				fail("%s: static lease %s: address must be IPv4", where, l.MAC)
			}
		}
	}

	if d := cfg.DNSServer; d != nil {
		if len(d.ListenLinks) == 0 {
			fail("DNSServerConfig: listenLinks must not be empty")
		}

		for _, l := range d.ListenLinks {
			checkLink("DNSServerConfig", l)
		}

		for _, r := range d.LocalRecords {
			if r.Name == "" || len(r.Addresses) == 0 {
				fail("DNSServerConfig: local records need a name and addresses")
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
