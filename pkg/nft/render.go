// Package nft renders the router config as an nftables ruleset in its own `inet routal` table.
//
// Talos keeps its own `inet talos` table (ingress firewall); a packet must be accepted by both.
// Traffic to the router itself (`to: self`) is opened in Talos (see package talospatch); this
// package only narrows it to the zone's links.
package nft

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/j0n17/routal/pkg/config"
	"github.com/j0n17/routal/pkg/env"
)

// Table is the nftables table owned by routald.
const Table = "routal"

// UPnPTable holds the mappings miniupnpd adds (UPnPConfig).
const UPnPTable = "routal_upnp"

// UPnP ports miniupnpd listens on, on the internal links (SSDP, IGD HTTP, NAT-PMP/PCP).
var (
	UPnPInternalUDP = []config.PortRange{{Lo: 1900, Hi: 1900}, {Lo: 5351, Hi: 5351}}
	UPnPInternalTCP = []config.PortRange{{Lo: 5000, Hi: 5000}}
)

// Render returns an nft script that atomically replaces the routal table. Link addresses from e
// are used for port forward reflection (the router's public address).
func Render(cfg *config.Config, e env.Env) string {
	r := renderer{cfg: cfg, resolve: e.Resolve, env: e}

	var b strings.Builder

	// "add" makes the delete succeed when the table does not exist yet; the whole script is one transaction.
	fmt.Fprintf(&b, "add table inet %s\ndelete table inet %s\n\n", Table, Table)
	fmt.Fprintf(&b, "table inet %s {\n", Table)
	r.sets(&b)
	r.input(&b)
	r.forward(&b)
	r.prerouting(&b)
	r.postrouting(&b)
	b.WriteString("}\n")

	// miniupnpd adds its mappings to its own table, which routald creates and never flushes
	fmt.Fprintf(&b, "\nadd table inet %s\n", UPnPTable)

	if r.cfg.UPnP != nil {
		for _, c := range []string{
			"forward { type filter hook forward priority filter; policy accept; }",
			"prerouting { type nat hook prerouting priority dstnat; policy accept; }",
			"postrouting { type nat hook postrouting priority srcnat; policy accept; }",
		} {
			fmt.Fprintf(&b, "add chain inet %s %s\n", UPnPTable, c)
		}
	} else {
		fmt.Fprintf(&b, "delete table inet %s\n", UPnPTable)
	}

	return b.String()
}

type renderer struct {
	cfg     *config.Config
	resolve func(string) string
	env     env.Env
}

func (r renderer) links(names []string) string {
	resolved := make([]string, 0, len(names))
	for _, n := range names {
		resolved = append(resolved, fmt.Sprintf("%q", r.resolve(n)))
	}

	slices.Sort(resolved)

	return "{ " + strings.Join(slices.Compact(resolved), ", ") + " }"
}

// input narrows the router ports opened in Talos (by `to: self` rules) to the links of the zones
// that rules allow: each rule accepts its zone's links, then whatever else reaches those ports is
// dropped. Rules may share ports (e.g. DNS from several zones), so the drops come last.
func (r renderer) input(b *strings.Builder) {
	b.WriteString("\tchain input {\n\t\ttype filter hook input priority filter; policy accept;\n")

	selfPorts := map[config.Protocol][]config.PortRange{}

	var (
		protos  []config.Protocol
		accepts strings.Builder
	)

	accept := func(links []string, proto config.Protocol, pr []config.PortRange, comment string) {
		if len(selfPorts[proto]) == 0 {
			protos = append(protos, proto)
		}

		selfPorts[proto] = append(selfPorts[proto], pr...)

		if len(links) > 0 {
			fmt.Fprintf(&accepts, "\t\tiifname %s meta l4proto %s th dport %s accept comment %q\n",
				r.links(links), l4proto(proto), ports(mergeRanges(pr)), comment)
		}
	}

	for _, rule := range r.cfg.Rules {
		rulePorts := r.cfg.RulePorts(rule)
		if rule.To != config.SelfZone || rule.Action != config.ActionAccept || len(rulePorts) == 0 {
			continue
		}

		accept(r.cfg.ZoneLinks(rule.From), rule.Protocol, rulePorts, rule.Name)
	}

	// UPnP: miniupnpd's ports on the internal links; the external port range is opened in Talos so
	// that mappings work, and anything that reaches the router itself on it (no mapping) is dropped.
	if u := r.cfg.UPnP; u != nil {
		accept(u.InternalLinks, config.ProtocolUDP, UPnPInternalUDP, "upnp")
		accept(u.InternalLinks, config.ProtocolTCP, UPnPInternalTCP, "upnp")
	}

	var upnpDrops []string

	if u := r.cfg.UPnP; u != nil {
		// only on the external link: router daemons use ephemeral ports in this range for replies
		// conntrack cannot match (e.g. unicast answers to the mDNS reflector's multicast queries)
		for _, p := range []config.Protocol{config.ProtocolTCP, config.ProtocolUDP} {
			upnpDrops = append(upnpDrops, fmt.Sprintf("\t\tiifname %q meta l4proto %s th dport %s drop comment \"upnp: no mapping\"\n",
				r.resolve(u.ExternalLink), p, ports(mergeRanges(u.ExternalPorts))))
		}
	}

	if len(protos) > 0 || len(upnpDrops) > 0 {
		// replies to the router's own connections may use any port
		b.WriteString("\t\tct state established,related accept\n")
		b.WriteString(accepts.String())
		b.WriteString("\t\tiifname \"lo\" accept\n")
	}

	for _, p := range protos {
		fmt.Fprintf(b, "\t\tmeta l4proto %s th dport %s drop\n", l4proto(p), ports(mergeRanges(selfPorts[p])))
	}

	for _, d := range upnpDrops {
		b.WriteString(d)
	}

	b.WriteString("\t}\n")
}

// mergeRanges sorts port ranges and merges overlapping or adjacent ones (nft rejects overlapping
// interval set elements).
func mergeRanges(in []config.PortRange) []config.PortRange {
	sorted := slices.Clone(in)
	slices.SortFunc(sorted, func(a, b config.PortRange) int { return int(a.Lo) - int(b.Lo) })

	var out []config.PortRange

	for _, pr := range sorted {
		if n := len(out); n > 0 && int(pr.Lo) <= int(out[n-1].Hi)+1 {
			out[n-1].Hi = max(out[n-1].Hi, pr.Hi)

			continue
		}

		out = append(out, pr)
	}

	return out
}

func (r renderer) forward(b *strings.Builder) {
	fw := r.cfg.Firewall

	fmt.Fprintf(b, "\tchain forward {\n\t\ttype filter hook forward priority filter; policy %s;\n", verdict(fw.ForwardDefault))
	b.WriteString("\t\tct state established,related accept\n\t\tct state invalid drop\n")

	// UPnP mappings are DNAT rules miniupnpd adds in its own table; accept what they translated.
	if u := r.cfg.UPnP; u != nil {
		fmt.Fprintf(b, "\t\tiifname %q oifname %s ct status dnat accept comment \"upnp\"\n", r.resolve(u.ExternalLink), r.links(u.InternalLinks))
	}

	// Reflected port forwards are accepted from any link but the forward's own.
	for _, pf := range r.cfg.PortForwards {
		if *pf.Reflection {
			fmt.Fprintf(b, "\t\tiifname != %q ct status dnat %s daddr %s meta l4proto %s th dport %s accept comment %q\n",
				r.resolve(pf.InLink), pfFamily(pf), pf.ToAddress, pf.Protocol, ports(pfTargetPorts(pf)), pf.Name+" reflection")
		}
	}

	for _, rule := range r.cfg.Rules {
		if rule.To == config.SelfZone {
			continue
		}

		head := fmt.Sprintf("iifname %s oifname %s", r.links(r.cfg.ZoneLinks(rule.From)), r.links(r.cfg.ZoneLinks(rule.To)))

		tail := []string{}
		if rule.Log {
			tail = append(tail, fmt.Sprintf("log prefix %q", "routal "+rule.Name+": "))
		}

		tail = append(tail, verdict(rule.Action), fmt.Sprintf("comment %q", rule.Name))

		// one nft rule per combination of source/destination/port selector and address family
		for _, src := range r.addrSels(rule.SourceSubnets, rule.SourceGroups) {
			for _, dst := range r.addrSels(rule.DestinationSubnets, rule.DestinationGroups) {
				for _, port := range portSels(rule.Ports, rule.PortGroups) {
					for _, fam := range selFamilies(src, dst) {
						m := []string{head}
						m = append(m, src.match(fam, "saddr")...)
						m = append(m, dst.match(fam, "daddr")...)

						switch {
						case rule.Protocol == "":
						case port == "":
							m = append(m, "meta l4proto "+l4proto(rule.Protocol))
						default:
							m = append(m, fmt.Sprintf("meta l4proto %s th dport %s", l4proto(rule.Protocol), port))
						}

						fmt.Fprintf(b, "\t\t%s\n", strings.Join(append(m, tail...), " "))
					}
				}
			}
		}
	}

	if fw.LogBlocked && fw.ForwardDefault == config.ActionBlock {
		b.WriteString("\t\tlog prefix \"routal blocked: \"\n")
	}

	b.WriteString("\t}\n")
}

// sets declares one named set per address group and family, and one per port group.
func (r renderer) sets(b *strings.Builder) {
	for _, g := range r.cfg.AddressGroups {
		sel := groupSel(g)

		for _, f := range []struct{ name, typ, elems string }{{"v4", "ipv4_addr", sel.v4lit}, {"v6", "ipv6_addr", sel.v6lit}} {
			if f.elems != "" {
				fmt.Fprintf(b, "\tset addr_%s_%s {\n\t\ttype %s; flags interval;\n\t\telements = %s\n\t}\n", g.Name, f.name, f.typ, f.elems)
			}
		}
	}

	for _, g := range r.cfg.PortGroups {
		fmt.Fprintf(b, "\tset port_%s {\n\t\ttype inet_service; flags interval;\n\t\telements = %s\n\t}\n", g.Name, ports(g.Ports))
	}
}

// addrSel matches an address list: the v4/v6 expressions are an anonymous set or a named set
// reference, empty when the list has no address of that family. A nil *addrSel matches anything.
type addrSel struct {
	v4, v6       string
	v4lit, v6lit string
}

func literalSel(prefixes []netip.Prefix) *addrSel {
	var v4, v6 []string

	for _, p := range prefixes {
		if p.Addr().Is4() {
			v4 = append(v4, p.String())
		} else {
			v6 = append(v6, p.String())
		}
	}

	sel := &addrSel{}
	if len(v4) > 0 {
		sel.v4lit = "{ " + strings.Join(v4, ", ") + " }"
	}

	if len(v6) > 0 {
		sel.v6lit = "{ " + strings.Join(v6, ", ") + " }"
	}

	sel.v4, sel.v6 = sel.v4lit, sel.v6lit

	return sel
}

func groupSel(g config.AddressGroup) *addrSel {
	sel := literalSel(g.Subnets)

	if sel.v4 != "" {
		sel.v4 = "@addr_" + g.Name + "_v4"
	}

	if sel.v6 != "" {
		sel.v6 = "@addr_" + g.Name + "_v6"
	}

	return sel
}

func (r renderer) addrSels(literal []netip.Prefix, groups []string) []*addrSel {
	switch {
	case len(groups) > 0:
		sels := make([]*addrSel, 0, len(groups))

		for _, name := range groups {
			if g, ok := r.cfg.AddressGroup(name); ok {
				sels = append(sels, groupSel(g))
			}
		}

		return sels
	case len(literal) > 0:
		return []*addrSel{literalSel(literal)}
	default:
		return []*addrSel{nil}
	}
}

func (s *addrSel) match(fam family, dir string) []string {
	if s == nil || fam == "" {
		return nil
	}

	expr := s.v4
	if fam == "ip6" {
		expr = s.v6
	}

	return []string{fmt.Sprintf("%s %s %s", fam, dir, expr)}
}

// selFamilies returns the families a source/destination pair can match together.
func selFamilies(src, dst *addrSel) []family {
	if src == nil && dst == nil {
		return []family{""}
	}

	var out []family

	if (src == nil || src.v4 != "") && (dst == nil || dst.v4 != "") {
		out = append(out, "ip")
	}

	if (src == nil || src.v6 != "") && (dst == nil || dst.v6 != "") {
		out = append(out, "ip6")
	}

	return out
}

// portSels returns the port expressions of a rule: one per port group, the literal set, or "" (any).
func portSels(literal []config.PortRange, groups []string) []string {
	switch {
	case len(groups) > 0:
		sels := make([]string, 0, len(groups))
		for _, g := range groups {
			sels = append(sels, "@port_"+g)
		}

		return sels
	case len(literal) > 0:
		return []string{ports(literal)}
	default:
		return []string{""}
	}
}

func (r renderer) prerouting(b *strings.Builder) {
	b.WriteString("\tchain prerouting {\n\t\ttype nat hook prerouting priority dstnat; policy accept;\n")

	for _, pf := range r.cfg.PortForwards {
		fam := pfFamily(pf)
		target := pf.ToAddress.String()

		if fam == "ip6" {
			target = "[" + target + "]"
		}

		if pf.ToPort != 0 {
			target += fmt.Sprintf(":%d", pf.ToPort)
		}

		var src []netip.Prefix

		for _, p := range pf.SourceSubnets {
			if p.Addr().Is4() == pf.ToAddress.Is4() {
				src = append(src, p)
			}
		}

		if len(pf.SourceSubnets) > 0 && len(src) == 0 {
			continue // no source subnet of the target's family
		}

		match := fmt.Sprintf("meta nfproto %s %s dport %s", map[family]string{"ip": "ipv4", "ip6": "ipv6"}[fam], pf.Protocol, ports(pf.Ports))
		action := []string{fmt.Sprintf("dnat %s to %s", fam, target), fmt.Sprintf("comment %q", pf.Name)}

		m := []string{fmt.Sprintf("iifname %q", r.resolve(pf.InLink))}
		m = append(m, fam.match("saddr", src)...)
		m = append(m, match)
		fmt.Fprintf(b, "\t\t%s\n", strings.Join(append(m, action...), " "))

		// reflection: from the inside, to the router's address on the forward's link
		if *pf.Reflection {
			var public []string

			for _, a := range r.env.Addrs(pf.InLink) {
				if a.Addr().Is4() == pf.ToAddress.Is4() && !a.Addr().IsLinkLocalUnicast() {
					public = append(public, a.Addr().String())
				}
			}

			if len(public) == 0 {
				continue // address not known yet (offline render, or DHCP pending): re-rendered when it is
			}

			action[1] = fmt.Sprintf("comment %q", pf.Name+" reflection")
			fmt.Fprintf(b, "\t\tiifname != %q %s daddr { %s } %s %s\n",
				r.resolve(pf.InLink), fam, strings.Join(public, ", "), match, strings.Join(action, " "))
		}
	}

	b.WriteString("\t}\n")
}

func pfFamily(pf config.PortForward) family {
	if pf.ToAddress.Is6() {
		return "ip6"
	}

	return "ip"
}

// pfTargetPorts returns the destination ports after DNAT.
func pfTargetPorts(pf config.PortForward) []config.PortRange {
	if pf.ToPort != 0 {
		return []config.PortRange{{Lo: pf.ToPort, Hi: pf.ToPort}}
	}

	return pf.Ports
}

func (r renderer) postrouting(b *strings.Builder) {
	b.WriteString("\tchain postrouting {\n\t\ttype nat hook postrouting priority srcnat; policy accept;\n")

	for _, m := range r.cfg.Masquerades {
		action := "masquerade"

		var fams []family

		switch {
		case m.SNATAddress.IsValid():
			f := family("ip")
			if m.SNATAddress.Is6() {
				f = "ip6"
			}

			fams = []family{f}
			action = fmt.Sprintf("snat %s to %s", f, m.SNATAddress)
		default:
			fams = families(m.SourceSubnets, nil)
		}

		for _, fam := range fams {
			parts := []string{fmt.Sprintf("oifname %q", r.resolve(m.OutLink))}

			if fam != "" && len(m.SourceSubnets) == 0 {
				parts = append(parts, "meta nfproto "+map[family]string{"ip": "ipv4", "ip6": "ipv6"}[fam])
			}

			parts = append(parts, fam.match("saddr", m.SourceSubnets)...)
			parts = append(parts, action, fmt.Sprintf("comment %q", m.Name))
			fmt.Fprintf(b, "\t\t%s\n", strings.Join(parts, " "))
		}
	}

	// Reflected connections come from the inside: without SNAT the server would answer the client
	// directly, from an address the client did not connect to.
	for _, pf := range r.cfg.PortForwards {
		if *pf.Reflection {
			fmt.Fprintf(b, "\t\tiifname != %q ct status dnat %s daddr %s meta l4proto %s th dport %s masquerade comment %q\n",
				r.resolve(pf.InLink), pfFamily(pf), pf.ToAddress, pf.Protocol, ports(pfTargetPorts(pf)), pf.Name+" reflection")
		}
	}

	b.WriteString("\t}\n")
}

// family is an nft address family selector: "ip", "ip6", or "" (both, no address match).
type family string

func (f family) match(dir string, prefixes []netip.Prefix) []string {
	var sel []string

	for _, p := range prefixes {
		if (f == "ip") == p.Addr().Is4() {
			sel = append(sel, p.String())
		}
	}

	if f == "" || len(sel) == 0 {
		return nil
	}

	return []string{fmt.Sprintf("%s %s { %s }", f, dir, strings.Join(sel, ", "))}
}

// families returns the address families a rule must be split into, so that each emitted rule
// only matches addresses of one family.
func families(src, dst []netip.Prefix) []family {
	if len(src) == 0 && len(dst) == 0 {
		return []family{""}
	}

	has := func(list []netip.Prefix, v4 bool) bool {
		return slices.ContainsFunc(list, func(p netip.Prefix) bool { return p.Addr().Is4() == v4 })
	}

	var out []family

	for _, v4 := range []bool{true, false} {
		if (len(src) == 0 || has(src, v4)) && (len(dst) == 0 || has(dst, v4)) {
			out = append(out, map[bool]family{true: "ip", false: "ip6"}[v4])
		}
	}

	return out
}

func l4proto(p config.Protocol) string {
	if p == config.ProtocolICMPv6 {
		return "ipv6-icmp"
	}

	return string(p)
}

func ports(pr []config.PortRange) string {
	s := make([]string, 0, len(pr))
	for _, p := range pr {
		s = append(s, p.String())
	}

	return "{ " + strings.Join(s, ", ") + " }"
}

func verdict(a config.Action) string {
	if a == config.ActionAccept {
		return "accept"
	}

	return "drop"
}
