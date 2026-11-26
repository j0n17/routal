// Package config defines the Routal router documents (the contents of /etc/routal/router.yaml).
package config

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// APIVersion is the only supported document apiVersion.
const APIVersion = "v1alpha1"

// SelfZone is the reserved zone name for traffic addressed to the router itself.
const SelfZone = "self"

// Action is a firewall verdict.
type Action string

// Firewall verdicts.
const (
	ActionAccept Action = "accept"
	ActionBlock  Action = "block"
)

// Protocol is an IP protocol matched by firewall rules and port forwards.
type Protocol string

// Supported protocols.
const (
	ProtocolTCP    Protocol = "tcp"
	ProtocolUDP    Protocol = "udp"
	ProtocolICMP   Protocol = "icmp"
	ProtocolICMPv6 Protocol = "icmpv6"
)

// HasPorts reports whether the protocol carries ports.
func (p Protocol) HasPorts() bool { return p == ProtocolTCP || p == ProtocolUDP }

// PortRange is an inclusive port range.
type PortRange struct {
	Lo, Hi uint16
}

func (r PortRange) String() string {
	if r.Lo == r.Hi {
		return strconv.Itoa(int(r.Lo))
	}

	return fmt.Sprintf("%d-%d", r.Lo, r.Hi)
}

// UnmarshalYAML accepts `443`, `"443"` or `"8000-9000"`.
func (r *PortRange) UnmarshalYAML(node *yaml.Node) error {
	parse := func(s string) (uint16, error) {
		v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 16)
		if err != nil || v == 0 {
			return 0, fmt.Errorf("invalid port %q", s)
		}

		return uint16(v), nil
	}

	lo, hi, isRange := strings.Cut(node.Value, "-")

	var err error

	if r.Lo, err = parse(lo); err != nil {
		return err
	}

	r.Hi = r.Lo

	if isRange {
		if r.Hi, err = parse(hi); err != nil {
			return err
		}

		if r.Hi < r.Lo {
			return fmt.Errorf("invalid port range %q", node.Value)
		}
	}

	return nil
}

// Forwarding enables IP forwarding between interfaces (kind: ForwardingConfig).
type Forwarding struct {
	// IPv4 forwards IPv4 between links (default true).
	IPv4 *bool `yaml:"ipv4,omitempty"`
	// IPv6 forwards IPv6 between links (default true).
	IPv6 *bool `yaml:"ipv6,omitempty"`
}

// Firewall holds global settings of the forward firewall (kind: FirewallConfig).
type Firewall struct {
	// ForwardDefault applies to forwarded traffic no rule matched (default: block).
	ForwardDefault Action `yaml:"forwardDefault,omitempty"`
	// LogBlocked logs packets dropped by the default policy.
	LogBlocked bool `yaml:"logBlocked,omitempty"`
}

// FirewallZone groups links (kind: FirewallZoneConfig).
type FirewallZone struct {
	// Name of the zone, used by rules (from, to); "self" is reserved.
	Name string `yaml:"name"`
	// Links of the zone (kernel names or Talos aliases); a link is in one zone at most.
	Links []string `yaml:"links" schema:"link"`
}

// FirewallRule matches traffic from one zone to another; `to: self` is traffic to the router (kind: FirewallRuleConfig).
type FirewallRule struct {
	// Name of the rule; it shows in the ruleset and in logs.
	Name string `yaml:"name"`
	// From is the zone the traffic comes from.
	From string `yaml:"from"`
	// To is the zone the traffic goes to, or "self" for the router itself (then protocol and ports
	// are required).
	To string `yaml:"to"`
	// Action is accept (default) or block. Rules are evaluated in order; replies are always allowed.
	Action Action `yaml:"action,omitempty"`
	// Protocol is tcp, udp, icmp or icmpv6 (default: any).
	Protocol Protocol `yaml:"protocol,omitempty"`
	// Ports are destination ports (tcp and udp only): 443, "443" or "8000-9000".
	Ports []PortRange `yaml:"ports,omitempty"`
	// SourceSubnets limits the rule to these sources.
	SourceSubnets []netip.Prefix `yaml:"sourceSubnets,omitempty"`
	// DestinationSubnets limits the rule to these destinations (for a port forward: the target,
	// after DNAT).
	DestinationSubnets []netip.Prefix `yaml:"destinationSubnets,omitempty"`
	// SourceGroups, DestinationGroups and PortGroups reference FirewallAddressGroupConfig and
	// FirewallPortGroupConfig documents; they replace the literal lists in the same direction.
	SourceGroups []string `yaml:"sourceGroups,omitempty"`
	// DestinationGroups are FirewallAddressGroupConfig names; not with destinationSubnets.
	DestinationGroups []string `yaml:"destinationGroups,omitempty"`
	// PortGroups are FirewallPortGroupConfig names; not with ports.
	PortGroups []string `yaml:"portGroups,omitempty"`
	// Log logs the packets the rule matches (kernel log, prefix "routal <name>:").
	Log bool `yaml:"log,omitempty"`
}

// AddressGroup is a named set of subnets, IPv4 and/or IPv6 (kind: FirewallAddressGroupConfig).
type AddressGroup struct {
	// Name of the group: letters, digits and _ (it becomes an nftables set name).
	Name string `yaml:"name"`
	// Subnets of the group; IPv4 and IPv6 can be mixed.
	Subnets []netip.Prefix `yaml:"subnets"`
}

// PortGroup is a named set of ports (kind: FirewallPortGroupConfig); the rule sets the protocol.
type PortGroup struct {
	// Name of the group: letters, digits and _ (it becomes an nftables set name).
	Name string `yaml:"name"`
	// Ports of the group: 443, "443" or "8000-9000".
	Ports []PortRange `yaml:"ports"`
}

// Masquerade source-NATs traffic leaving a link (kind: MasqueradeConfig).
type Masquerade struct {
	// Name of the NAT rule.
	Name string `yaml:"name"`
	// OutLink is the link the traffic leaves through (the WAN link).
	OutLink string `yaml:"outLink" schema:"link"`
	// SourceSubnets limits NAT to these sources (default: everything leaving the link).
	SourceSubnets []netip.Prefix `yaml:"sourceSubnets,omitempty"`
	// SNATAddress uses a fixed source address instead of the link address.
	SNATAddress netip.Addr `yaml:"snatAddress,omitempty"`
}

// PortForward destination-NATs traffic arriving on a link (kind: PortForwardConfig).
type PortForward struct {
	// Name of the port forward.
	Name string `yaml:"name"`
	// InLink is the link the traffic arrives on (the WAN link).
	InLink string `yaml:"inLink" schema:"link"`
	// Protocol is tcp or udp.
	Protocol Protocol `yaml:"protocol" schema:"enum=tcp|udp"`
	// Ports on the router: one port, a range, or several.
	Ports []PortRange `yaml:"ports"`
	// ToAddress is the host the traffic is sent to. A FirewallRuleConfig must also allow it.
	ToAddress netip.Addr `yaml:"toAddress"`
	// ToPort is the port on the target, with a single port only (default: the same port).
	ToPort uint16 `yaml:"toPort,omitempty"`
	// SourceSubnets limits who may use the forward (default: anyone).
	SourceSubnets []netip.Prefix `yaml:"sourceSubnets,omitempty"`
	// Reflection (default true) makes the forward work from inside too, when clients use the
	// router's public address ("NAT loopback" / hairpin NAT).
	Reflection *bool `yaml:"reflection,omitempty"`
}

// DHCPServer serves IPv4 leases on a link (kind: DHCPServerConfig).
type DHCPServer struct {
	// Name of the DHCP server.
	Name string `yaml:"name"`
	// Link the leases are served on.
	Link string `yaml:"link" schema:"link"`
	// Subnet defaults to the router's address on the link that contains rangeStart.
	Subnet netip.Prefix `yaml:"subnet,omitempty"`
	// RangeStart is the first address of the pool.
	RangeStart netip.Addr `yaml:"rangeStart"`
	// RangeEnd is the last address of the pool.
	RangeEnd netip.Addr `yaml:"rangeEnd"`
	// LeaseTime of the leases (default 12h, at least 1m).
	LeaseTime time.Duration `yaml:"leaseTime,omitempty"`
	// Gateway given to clients (default: the router's address on the link).
	Gateway netip.Addr `yaml:"gateway,omitempty"`
	// DNSServers given to clients (default: the router when DNSServerConfig listens on the link).
	DNSServers []netip.Addr `yaml:"dnsServers,omitempty"`
	// Domain given to clients (default: the localDomain of DNSServerConfig).
	Domain string `yaml:"domain,omitempty"`
	// StaticLeases pin addresses to devices.
	StaticLeases []DHCPStaticLease `yaml:"staticLeases,omitempty"`
}

// DHCPStaticLease pins an address to a MAC.
type DHCPStaticLease struct {
	// MAC address of the device (aa:bb:cc:dd:ee:ff).
	MAC string `yaml:"mac"`
	// Address the device always gets; inside the subnet.
	Address netip.Addr `yaml:"address"`
	// Hostname published in DNS as <hostname>.<localDomain>.
	Hostname string `yaml:"hostname,omitempty"`
}

// DNSServer is the caching DNS forwarder for local networks (kind: DNSServerConfig).
type DNSServer struct {
	// ListenLinks are the links the server answers on (the router's addresses there).
	ListenLinks []string `yaml:"listenLinks" schema:"link"`
	// Upstreams are the resolvers queries are forwarded to (default: Talos' resolver, 127.0.0.53).
	Upstreams []netip.Addr `yaml:"upstreams,omitempty"`
	// TLSUpstreams are resolvers reached over DNS over TLS, so that the ISP cannot read or change
	// the queries; instead of Upstreams.
	TLSUpstreams []TLSUpstream `yaml:"tlsUpstreams,omitempty"`
	// DNSSEC validates signed answers and refuses forged ones (default false). The upstreams must
	// return DNSSEC records: public resolvers do, the resolver of an ISP box often does not.
	DNSSEC bool `yaml:"dnssec,omitempty"`
	// CacheSize in entries (default 10000).
	CacheSize int `yaml:"cacheSize,omitempty"`
	// LocalDomain is answered locally and never forwarded (e.g. home.arpa).
	LocalDomain string `yaml:"localDomain,omitempty"`
	// RegisterDHCPLeases publishes DHCP hostnames as <name>.<localDomain> (default true).
	RegisterDHCPLeases *bool `yaml:"registerDHCPLeases,omitempty"`
	// LocalRecords are static names.
	LocalRecords []DNSRecord `yaml:"localRecords,omitempty"`
	// Blocking answers blocked names with NXDOMAIN (ads, trackers): lists by URL and inline names.
	Blocking *DNSBlocking `yaml:"blocking,omitempty"`
}

// DNSBlocking is an AdGuard/Pi-hole style blocklist for the DNS server.
// TLSUpstream is a resolver reached over DNS over TLS.
type TLSUpstream struct {
	// Address of the resolver.
	Address netip.Addr `yaml:"address"`
	// Name the resolver's certificate must hold (e.g. dns.quad9.net).
	Name string `yaml:"name"`
	// Port (default 853).
	Port uint16 `yaml:"port,omitempty"`
}

type DNSBlocking struct {
	// Lists are URLs of hosts files, domain lists or AdBlock-style lists (||domain^), downloaded
	// by routald and refreshed every Refresh.
	Lists   []string `yaml:"lists,omitempty"`
	Domains []string `yaml:"domains,omitempty"` // blocked, with their subdomains
	Allow   []string `yaml:"allow,omitempty"`   // never blocked (even inside a blocked domain)
	// Response is nxdomain (default) or zero (the answer is 0.0.0.0 / ::).
	Response string        `yaml:"response,omitempty" schema:"enum=nxdomain|zero"`
	Refresh  time.Duration `yaml:"refresh,omitempty"` // default 24h
}

// DNSRecord is a static local DNS record.
type DNSRecord struct {
	// Name of the record; a single label gets the local domain.
	Name string `yaml:"name"`
	// Addresses of the name, IPv4 (A) and IPv6 (AAAA).
	Addresses []netip.Addr `yaml:"addresses"`
}

// RouterAdvertisement sends IPv6 router advertisements on a link (kind: RouterAdvertisementConfig).
type RouterAdvertisement struct {
	// Name of the document.
	Name string `yaml:"name"`
	// Link the advertisements are sent on.
	Link string `yaml:"link" schema:"link"`
	// Prefixes announced (default: every /64 on the link, which follows a delegated prefix).
	Prefixes []netip.Prefix `yaml:"prefixes,omitempty"`
	// Managed is stateful DHCPv6: not supported yet.
	Managed bool `yaml:"managed,omitempty"`
	// Other also serves DNS options with stateless DHCPv6; needs prefixes.
	Other bool `yaml:"other,omitempty"`
	// DNSServers announced (default: the router when DNSServerConfig listens on the link).
	DNSServers []netip.Addr `yaml:"dnsServers,omitempty"`
	// RouterLifetime of the default route on clients (default 30m).
	RouterLifetime time.Duration `yaml:"routerLifetime,omitempty"`
}

// NTPServer serves time to local networks (kind: NTPServerConfig). It does not set the router's
// clock (Talos does); it serves the time it tracks from its upstreams.
type NTPServer struct {
	// ListenLinks are the links the server answers on; DHCP there advertises the router as NTP
	// server.
	ListenLinks []string `yaml:"listenLinks" schema:"link"`
	// Upstreams are the time sources, names or addresses (default: time.cloudflare.com).
	Upstreams []string `yaml:"upstreams,omitempty"`
}

// MDNSReflector repeats mDNS (Bonjour/Avahi) between links, so that services such as printers,
// AirPlay or Chromecast are found across VLANs (kind: MDNSReflectorConfig).
type MDNSReflector struct {
	// Links mDNS is repeated between: at least two. The firewall must allow udp 5353 from their
	// zones.
	Links []string `yaml:"links" schema:"link"`
}

// UPnP lets devices on the internal links open port forwards themselves, with UPnP IGD, NAT-PMP
// and PCP (kind: UPnPConfig).
type UPnP struct {
	// ExternalLink is the WAN link the forwards are opened on.
	ExternalLink string `yaml:"externalLink" schema:"link"`
	// InternalLinks are the links devices may ask from.
	InternalLinks []string `yaml:"internalLinks" schema:"link"`
	// ExternalPorts limits the ports devices may open (default 1024-49151, which keeps the Talos API
	// on 50000 out of reach).
	ExternalPorts []PortRange `yaml:"externalPorts,omitempty"`
	// InternalSubnets limits the devices allowed to ask (default: any address on the internal links).
	InternalSubnets []netip.Prefix `yaml:"internalSubnets,omitempty"`
}

// PrefixDelegation asks the ISP for an IPv6 prefix with DHCPv6 (IA_PD) on a WAN link and gives a
// /64 of it to each LAN link (kind: PrefixDelegationConfig). RouterAdvertisementConfig without
// prefixes then announces it.
type PrefixDelegation struct {
	// Name of the document.
	Name string `yaml:"name"`
	// Link is the WAN link the DHCPv6 client runs on (an Ethernet link, or the PPPoE interface).
	Link string `yaml:"link" schema:"link"`
	// PrefixLength is a hint for the size wanted (e.g. 56); the ISP decides.
	PrefixLength int `yaml:"prefixLength,omitempty"`
	// Subnets gives /64s of the delegated prefix to links; the router takes ::1 on each.
	Subnets []DelegatedSubnet `yaml:"subnets"`
	// AcceptRA (default true) keeps accepting router advertisements on the WAN link although
	// forwarding is on (the ISP's default route usually comes from them).
	AcceptRA *bool `yaml:"acceptRA,omitempty"`
}

// DelegatedSubnet is a /64 of the delegated prefix given to a link.
type DelegatedSubnet struct {
	// Link that gets the /64.
	Link string `yaml:"link" schema:"link"`
	// ID is the /64 index in the delegated prefix (a /56 has 256 of them, 0-255; default 0).
	ID uint32 `yaml:"id,omitempty"`
}

// PPPoE dials the ISP over Ethernet with pppd (kind: PPPoEConfig). The interface it creates
// (default ppp0) is used like any link: zones, masquerade, prefix delegation.
type PPPoE struct {
	// Name of the session.
	Name string `yaml:"name"`
	Link string `yaml:"link" schema:"link"` // Ethernet link (a VLAN if the ISP wants one, e.g. wan0.832)
	// Interface is the name of the PPP interface (default ppp0): use it in zones, NAT and prefix
	// delegation.
	Interface string `yaml:"interface,omitempty" schema:"link"`
	// Username of the ISP account.
	Username     string `yaml:"username"`
	PasswordFile string `yaml:"passwordFile"` // relative to router.yaml
	// MTU of the session (default 1492); TCP MSS is clamped to it.
	MTU int `yaml:"mtu,omitempty"`
	// ServiceName, only if the ISP asks for one.
	ServiceName string `yaml:"serviceName,omitempty"`
	// DefaultRoute (default true) routes the Internet through the session.
	DefaultRoute *bool `yaml:"defaultRoute,omitempty"`
	// IPv6 (default true) negotiates IPv6 on the session (then PrefixDelegationConfig can run on it).
	IPv6 *bool `yaml:"ipv6,omitempty"`

	Password string `yaml:"-"` // read from PasswordFile
}

// DynamicDNS keeps a public DNS name pointing at the router's WAN address (kind: DynamicDNSConfig).
type DynamicDNS struct {
	// Name of the document.
	Name string `yaml:"name"`
	// Provider is an inadyn provider (cloudflare.com, duckdns.org, default@dyndns.org, ...) or
	// "custom" for any dyndns2-style server (server and path).
	Provider string `yaml:"provider"`
	// Hostnames to keep up to date.
	Hostnames []string `yaml:"hostnames"`
	// Username of the account (Cloudflare: the zone).
	Username     string `yaml:"username,omitempty"`
	PasswordFile string `yaml:"passwordFile,omitempty"` // relative to router.yaml; the API token for most providers
	Server       string `yaml:"server,omitempty"`       // custom: host[:port]
	Path         string `yaml:"path,omitempty"`         // custom: e.g. /nic/update?hostname=%h&myip=%i
	// Link takes the address from this link (default: ask the provider what it sees; needed behind
	// another NAT).
	Link string `yaml:"link,omitempty" schema:"link"`
	// IPv6 updates the AAAA record instead of the A record.
	IPv6 bool `yaml:"ipv6,omitempty"`
	// SSL (default true) talks HTTPS to the provider.
	SSL *bool `yaml:"ssl,omitempty"`
	// Period between checks (default 5m).
	Period time.Duration `yaml:"period,omitempty"`

	Password string `yaml:"-"` // read from PasswordFile
}
