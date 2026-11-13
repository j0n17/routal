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
)

// Table is the nftables table owned by routald.
const Table = "routal"

// LinkResolver maps a configured link name (kernel name or Talos link alias) to kernel names.
type LinkResolver func(name string) string

// Render returns an nft script that atomically replaces the routal table.
func Render(cfg *config.Config, resolve LinkResolver) string {
	if resolve == nil {
		resolve = func(name string) string { return name }
	}

	r := renderer{cfg: cfg, resolve: resolve}

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

	return b.String()
}

type renderer struct {
	cfg     *config.Config
	resolve LinkResolver
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

	var protos []config.Protocol

	for _, rule := range r.cfg.Rules {
		rulePorts := r.cfg.RulePorts(rule)
		if rule.To != config.SelfZone || rule.Action != config.ActionAccept || len(rulePorts) == 0 {
			continue
		}

		if len(selfPorts[rule.Protocol]) == 0 {
			protos = append(protos, rule.Protocol)
		}

		selfPorts[rule.Protocol] = append(selfPorts[rule.Protocol], rulePorts...)

		fmt.Fprintf(b, "\t\tiifname %s meta l4proto %s th dport %s accept comment %q\n",
			r.links(r.cfg.ZoneLinks(rule.From)), l4proto(rule.Protocol), ports(mergeRanges(rulePorts)), rule.Name)
	}

	if len(protos) > 0 {
		b.WriteString("\t\tiifname \"lo\" accept\n")
	}

	for _, p := range protos {
		fmt.Fprintf(b, "\t\tmeta l4proto %s th dport %s drop\n", l4proto(p), ports(mergeRanges(selfPorts[p])))
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
		fam := "ip"
		target := pf.ToAddress.String()

		if pf.ToAddress.Is6() {
			fam, target = "ip6", "["+target+"]"
		}

		if pf.ToPort != 0 {
			target += fmt.Sprintf(":%d", pf.ToPort)
		}

		m := []string{fmt.Sprintf("iifname %q", r.resolve(pf.InLink))}

		var src []netip.Prefix

		for _, p := range pf.SourceSubnets {
			if p.Addr().Is4() == pf.ToAddress.Is4() {
				src = append(src, p)
			}
		}

		if len(pf.SourceSubnets) > 0 && len(src) == 0 {
			continue // no source subnet of the target's family
		}

		m = append(m, family(fam).match("saddr", src)...)
		m = append(m, fmt.Sprintf("meta nfproto %s %s dport %s", map[string]string{"ip": "ipv4", "ip6": "ipv6"}[fam], pf.Protocol, ports(pf.Ports)))
		m = append(m, fmt.Sprintf("dnat %s to %s", fam, target), fmt.Sprintf("comment %q", pf.Name))
		fmt.Fprintf(b, "\t\t%s\n", strings.Join(m, " "))
	}

	b.WriteString("\t}\n")
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
