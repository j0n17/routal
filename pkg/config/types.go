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
	IPv4 *bool `yaml:"ipv4,omitempty"`
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
	Name  string   `yaml:"name"`
	Links []string `yaml:"links"`
}

// FirewallRule matches traffic from one zone to another; `to: self` is traffic to the router (kind: FirewallRuleConfig).
type FirewallRule struct {
	Name               string         `yaml:"name"`
	From               string         `yaml:"from"`
	To                 string         `yaml:"to"`
	Action             Action         `yaml:"action,omitempty"`
	Protocol           Protocol       `yaml:"protocol,omitempty"`
	Ports              []PortRange    `yaml:"ports,omitempty"`
	SourceSubnets      []netip.Prefix `yaml:"sourceSubnets,omitempty"`
	DestinationSubnets []netip.Prefix `yaml:"destinationSubnets,omitempty"`
	// SourceGroups, DestinationGroups and PortGroups reference FirewallAddressGroupConfig and
	// FirewallPortGroupConfig documents; they replace the literal lists in the same direction.
	SourceGroups      []string `yaml:"sourceGroups,omitempty"`
	DestinationGroups []string `yaml:"destinationGroups,omitempty"`
	PortGroups        []string `yaml:"portGroups,omitempty"`
	Log               bool     `yaml:"log,omitempty"`
}

// AddressGroup is a named set of subnets, IPv4 and/or IPv6 (kind: FirewallAddressGroupConfig).
type AddressGroup struct {
	Name    string         `yaml:"name"`
	Subnets []netip.Prefix `yaml:"subnets"`
}

// PortGroup is a named set of ports (kind: FirewallPortGroupConfig); the rule sets the protocol.
type PortGroup struct {
	Name  string      `yaml:"name"`
	Ports []PortRange `yaml:"ports"`
}

// Masquerade source-NATs traffic leaving a link (kind: MasqueradeConfig).
type Masquerade struct {
	Name          string         `yaml:"name"`
	OutLink       string         `yaml:"outLink"`
	SourceSubnets []netip.Prefix `yaml:"sourceSubnets,omitempty"`
	// SNATAddress uses a fixed source address instead of the link address.
	SNATAddress netip.Addr `yaml:"snatAddress,omitempty"`
}

// PortForward destination-NATs traffic arriving on a link (kind: PortForwardConfig).
type PortForward struct {
	Name          string         `yaml:"name"`
	InLink        string         `yaml:"inLink"`
	Protocol      Protocol       `yaml:"protocol"`
	Ports         []PortRange    `yaml:"ports"`
	ToAddress     netip.Addr     `yaml:"toAddress"`
	ToPort        uint16         `yaml:"toPort,omitempty"`
	SourceSubnets []netip.Prefix `yaml:"sourceSubnets,omitempty"`
	// Reflection (default true) makes the forward work from inside too, when clients use the
	// router's public address ("NAT loopback" / hairpin NAT).
	Reflection *bool `yaml:"reflection,omitempty"`
}

// DHCPServer serves IPv4 leases on a link (kind: DHCPServerConfig).
type DHCPServer struct {
	Name string `yaml:"name"`
	Link string `yaml:"link"`
	// Subnet defaults to the router's address on the link that contains rangeStart.
	Subnet       netip.Prefix      `yaml:"subnet,omitempty"`
	RangeStart   netip.Addr        `yaml:"rangeStart"`
	RangeEnd     netip.Addr        `yaml:"rangeEnd"`
	LeaseTime    time.Duration     `yaml:"leaseTime,omitempty"`
	Gateway      netip.Addr        `yaml:"gateway,omitempty"`
	DNSServers   []netip.Addr      `yaml:"dnsServers,omitempty"`
	Domain       string            `yaml:"domain,omitempty"`
	StaticLeases []DHCPStaticLease `yaml:"staticLeases,omitempty"`
}

// DHCPStaticLease pins an address to a MAC.
type DHCPStaticLease struct {
	MAC      string     `yaml:"mac"`
	Address  netip.Addr `yaml:"address"`
	Hostname string     `yaml:"hostname,omitempty"`
}

// DNSServer is the caching DNS forwarder for local networks (kind: DNSServerConfig).
type DNSServer struct {
	ListenLinks        []string     `yaml:"listenLinks"`
	Upstreams          []netip.Addr `yaml:"upstreams,omitempty"`
	CacheSize          int          `yaml:"cacheSize,omitempty"`
	LocalDomain        string       `yaml:"localDomain,omitempty"`
	RegisterDHCPLeases *bool        `yaml:"registerDHCPLeases,omitempty"`
	LocalRecords       []DNSRecord  `yaml:"localRecords,omitempty"`
}

// DNSRecord is a static local DNS record.
type DNSRecord struct {
	Name      string       `yaml:"name"`
	Addresses []netip.Addr `yaml:"addresses"`
}

// RouterAdvertisement sends IPv6 router advertisements on a link (kind: RouterAdvertisementConfig).
type RouterAdvertisement struct {
	Name           string         `yaml:"name"`
	Link           string         `yaml:"link"`
	Prefixes       []netip.Prefix `yaml:"prefixes,omitempty"`
	Managed        bool           `yaml:"managed,omitempty"`
	Other          bool           `yaml:"other,omitempty"`
	DNSServers     []netip.Addr   `yaml:"dnsServers,omitempty"`
	RouterLifetime time.Duration  `yaml:"routerLifetime,omitempty"`
}

// NTPServer serves time to local networks (kind: NTPServerConfig). It does not set the router's
// clock (Talos does); it serves the time it tracks from its upstreams.
type NTPServer struct {
	ListenLinks []string `yaml:"listenLinks"`
	Upstreams   []string `yaml:"upstreams,omitempty"`
}

// MDNSReflector repeats mDNS (Bonjour/Avahi) between links, so that services such as printers,
// AirPlay or Chromecast are found across VLANs (kind: MDNSReflectorConfig).
type MDNSReflector struct {
	Links []string `yaml:"links"`
}

// UPnP lets devices on the internal links open port forwards themselves, with UPnP IGD, NAT-PMP
// and PCP (kind: UPnPConfig).
type UPnP struct {
	ExternalLink  string   `yaml:"externalLink"`
	InternalLinks []string `yaml:"internalLinks"`
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
	Name string `yaml:"name"`
	// Link is the WAN link the DHCPv6 client runs on (an Ethernet link, or the PPPoE interface).
	Link string `yaml:"link"`
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
	Link string `yaml:"link"`
	// ID is the /64 index in the delegated prefix (a /56 has 256 of them, 0-255).
	ID uint32 `yaml:"id"`
}

// PPPoE dials the ISP over Ethernet with pppd (kind: PPPoEConfig). The interface it creates
// (default ppp0) is used like any link: zones, masquerade, prefix delegation.
type PPPoE struct {
	Name         string `yaml:"name"`
	Link         string `yaml:"link"` // Ethernet link (a VLAN if the ISP wants one, e.g. wan0.832)
	Interface    string `yaml:"interface,omitempty"`
	Username     string `yaml:"username"`
	PasswordFile string `yaml:"passwordFile"` // relative to router.yaml
	MTU          int    `yaml:"mtu,omitempty"`
	ServiceName  string `yaml:"serviceName,omitempty"`
	// DefaultRoute (default true) routes the Internet through the session.
	DefaultRoute *bool `yaml:"defaultRoute,omitempty"`
	// IPv6 (default true) negotiates IPv6 on the session (then PrefixDelegationConfig can run on it).
	IPv6 *bool `yaml:"ipv6,omitempty"`

	Password string `yaml:"-"` // read from PasswordFile
}

// DynamicDNS keeps a public DNS name pointing at the router's WAN address (kind: DynamicDNSConfig).
type DynamicDNS struct {
	Name string `yaml:"name"`
	// Provider is an inadyn provider (cloudflare.com, duckdns.org, default@dyndns.org, ...) or
	// "custom" for any dyndns2-style server (server and path).
	Provider     string   `yaml:"provider"`
	Hostnames    []string `yaml:"hostnames"`
	Username     string   `yaml:"username,omitempty"`
	PasswordFile string   `yaml:"passwordFile,omitempty"` // relative to router.yaml; the API token for most providers
	Server       string   `yaml:"server,omitempty"`       // custom: host[:port]
	Path         string   `yaml:"path,omitempty"`         // custom: e.g. /nic/update?hostname=%h&myip=%i
	// Link takes the address from this link (default: ask the provider what it sees; needed behind
	// another NAT).
	Link string `yaml:"link,omitempty"`
	IPv6 bool   `yaml:"ipv6,omitempty"`
	// SSL (default true) talks HTTPS to the provider.
	SSL *bool `yaml:"ssl,omitempty"`
	// Period between checks (default 5m).
	Period time.Duration `yaml:"period,omitempty"`

	Password string `yaml:"-"` // read from PasswordFile
}
