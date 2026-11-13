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
	Log                bool           `yaml:"log,omitempty"`
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
}

// DHCPServer serves IPv4 leases on a link (kind: DHCPServerConfig).
type DHCPServer struct {
	Name         string            `yaml:"name"`
	Link         string            `yaml:"link"`
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
