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

// input narrows Talos-opened router ports to the links of the zones allowed by `to: self` rules.
func (r renderer) input(b *strings.Builder) {
	type key struct {
		proto config.Protocol
		ports string
	}

	allowed := map[key][]string{}

	var order []key

	for _, rule := range r.cfg.Rules {
		if rule.To != config.SelfZone || rule.Action != config.ActionAccept || len(rule.Ports) == 0 {
			continue
		}

		k := key{rule.Protocol, ports(rule.Ports)}
		if _, ok := allowed[k]; !ok {
			order = append(order, k)
		}

		allowed[k] = append(allowed[k], r.cfg.ZoneLinks(rule.From)...)
	}

	b.WriteString("\tchain input {\n\t\ttype filter hook input priority filter; policy accept;\n")

	for _, k := range order {
		fmt.Fprintf(b, "\t\tiifname != %s meta l4proto %s th dport %s drop\n",
			r.links(append([]string{"lo"}, allowed[k]...)), l4proto(k.proto), k.ports)
	}

	b.WriteString("\t}\n")
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

		for _, fam := range families(rule.SourceSubnets, rule.DestinationSubnets) {
			var m []string

			m = append(m, head)
			m = append(m, fam.match("saddr", rule.SourceSubnets)...)
			m = append(m, fam.match("daddr", rule.DestinationSubnets)...)
			m = append(m, protoMatch(rule.Protocol, rule.Ports)...)

			if rule.Log {
				m = append(m, fmt.Sprintf("log prefix %q", "routal "+rule.Name+": "))
			}

			m = append(m, verdict(rule.Action), fmt.Sprintf("comment %q", rule.Name))
			fmt.Fprintf(b, "\t\t%s\n", strings.Join(m, " "))
		}
	}

	if fw.LogBlocked && fw.ForwardDefault == config.ActionBlock {
		b.WriteString("\t\tlog prefix \"routal blocked: \"\n")
	}

	b.WriteString("\t}\n")
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

func protoMatch(proto config.Protocol, pr []config.PortRange) []string {
	switch {
	case proto == "":
		return nil
	case len(pr) > 0:
		return []string{fmt.Sprintf("meta l4proto %s th dport %s", l4proto(proto), ports(pr))}
	default:
		return []string{"meta l4proto " + l4proto(proto)}
	}
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
