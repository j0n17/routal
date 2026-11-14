package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the set of router documents.
type Config struct {
	Forwarding           *Forwarding
	Firewall             *Firewall
	Zones                []FirewallZone
	Rules                []FirewallRule
	AddressGroups        []AddressGroup
	PortGroups           []PortGroup
	Masquerades          []Masquerade
	PortForwards         []PortForward
	DHCPServers          []DHCPServer
	DNSServer            *DNSServer
	RouterAdvertisements []RouterAdvertisement
	NTPServer            *NTPServer
	MDNSReflector        *MDNSReflector
	UPnP                 *UPnP
	PrefixDelegations    []PrefixDelegation
	PPPoE                []PPPoE
	DynamicDNS           []DynamicDNS

	// BaseDir is the directory of router.yaml: secret files are relative to it.
	BaseDir string
}

// LoadFile reads, parses and validates a router config file; secret files are relative to it.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	return LoadDir(data, filepath.Dir(path))
}

// Load parses a multi-document YAML router config, applies defaults and validates it. Secret
// files are relative to the current directory.
func Load(data []byte) (*Config, error) {
	return LoadDir(data, ".")
}

// LoadDir is Load with secret files relative to dir.
func LoadDir(data []byte, dir string) (*Config, error) {
	cfg := &Config{BaseDir: dir}
	dec := yaml.NewDecoder(bytes.NewReader(data))

	for i := 1; ; i++ {
		var node yaml.Node

		if err := dec.Decode(&node); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			return nil, fmt.Errorf("document %d: %w", i, err)
		}

		if len(node.Content) == 0 || (node.Content[0].Kind == yaml.MappingNode && len(node.Content[0].Content) == 0) ||
			node.Content[0].Tag == "!!null" {
			continue // empty document
		}

		if err := cfg.add(&node); err != nil {
			return nil, fmt.Errorf("document %d: %w", i, err)
		}
	}

	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if err := cfg.readSecrets(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// SecretFiles returns the secret files the config references, relative to BaseDir.
func (cfg *Config) SecretFiles() []string {
	var out []string

	for _, p := range cfg.PPPoE {
		out = append(out, p.PasswordFile)
	}

	for _, d := range cfg.DynamicDNS {
		if d.PasswordFile != "" {
			out = append(out, d.PasswordFile)
		}
	}

	return out
}

func (cfg *Config) readSecrets() error {
	read := func(where, rel string) (string, error) {
		raw, err := os.ReadFile(filepath.Join(cfg.BaseDir, rel))
		if err != nil {
			return "", fmt.Errorf("%s: %w", where, err)
		}

		// the first line that is not blank or a # comment
		for _, line := range strings.Split(string(raw), "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
				return line, nil
			}
		}

		return "", fmt.Errorf("%s: %s is empty", where, rel)
	}

	var err error

	for i := range cfg.PPPoE {
		p := &cfg.PPPoE[i]
		if p.Password, err = read("PPPoEConfig "+p.Name, p.PasswordFile); err != nil {
			return err
		}
	}

	for i := range cfg.DynamicDNS {
		d := &cfg.DynamicDNS[i]
		if d.PasswordFile != "" {
			if d.Password, err = read("DynamicDNSConfig "+d.Name, d.PasswordFile); err != nil {
				return err
			}
		}
	}

	return nil
}

func (cfg *Config) add(node *yaml.Node) error {
	var header struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
	}

	if err := node.Decode(&header); err != nil {
		return err
	}

	if header.APIVersion != APIVersion {
		return fmt.Errorf("kind %q: unsupported apiVersion %q (want %q)", header.Kind, header.APIVersion, APIVersion)
	}

	switch header.Kind {
	case "ForwardingConfig":
		if cfg.Forwarding != nil {
			return errors.New("duplicate ForwardingConfig")
		}

		cfg.Forwarding = &Forwarding{}

		return decodeStrict(node, cfg.Forwarding)
	case "FirewallConfig":
		if cfg.Firewall != nil {
			return errors.New("duplicate FirewallConfig")
		}

		cfg.Firewall = &Firewall{}

		return decodeStrict(node, cfg.Firewall)
	case "DNSServerConfig":
		if cfg.DNSServer != nil {
			return errors.New("duplicate DNSServerConfig")
		}

		cfg.DNSServer = &DNSServer{}

		return decodeStrict(node, cfg.DNSServer)
	case "NTPServerConfig":
		return singleton(node, &cfg.NTPServer, header.Kind)
	case "MDNSReflectorConfig":
		return singleton(node, &cfg.MDNSReflector, header.Kind)
	case "UPnPConfig":
		return singleton(node, &cfg.UPnP, header.Kind)
	case "PrefixDelegationConfig":
		return appendDoc(node, &cfg.PrefixDelegations)
	case "PPPoEConfig":
		return appendDoc(node, &cfg.PPPoE)
	case "DynamicDNSConfig":
		return appendDoc(node, &cfg.DynamicDNS)
	case "FirewallZoneConfig":
		return appendDoc(node, &cfg.Zones)
	case "FirewallRuleConfig":
		return appendDoc(node, &cfg.Rules)
	case "FirewallAddressGroupConfig":
		return appendDoc(node, &cfg.AddressGroups)
	case "FirewallPortGroupConfig":
		return appendDoc(node, &cfg.PortGroups)
	case "MasqueradeConfig":
		return appendDoc(node, &cfg.Masquerades)
	case "PortForwardConfig":
		return appendDoc(node, &cfg.PortForwards)
	case "DHCPServerConfig":
		return appendDoc(node, &cfg.DHCPServers)
	case "RouterAdvertisementConfig":
		return appendDoc(node, &cfg.RouterAdvertisements)
	default:
		return fmt.Errorf("unknown kind %q", header.Kind)
	}
}

func singleton[T any](node *yaml.Node, dst **T, kind string) error {
	if *dst != nil {
		return fmt.Errorf("duplicate %s", kind)
	}

	*dst = new(T)

	return decodeStrict(node, *dst)
}

func appendDoc[T any](node *yaml.Node, list *[]T) error {
	var v T

	if err := decodeStrict(node, &v); err != nil {
		return err
	}

	*list = append(*list, v)

	return nil
}

// decodeStrict decodes a document rejecting unknown fields (apiVersion and kind are allowed).
func decodeStrict(node *yaml.Node, out any) error {
	body := *node.Content[0]
	body.Content = nil

	for i := 0; i+1 < len(node.Content[0].Content); i += 2 {
		key := node.Content[0].Content[i]
		if key.Value == "apiVersion" || key.Value == "kind" {
			continue
		}

		body.Content = append(body.Content, key, node.Content[0].Content[i+1])
	}

	raw, err := yaml.Marshal(&body)
	if err != nil {
		return err
	}

	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)

	if err := dec.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return err
	}

	return nil
}

func (cfg *Config) applyDefaults() {
	if cfg.Forwarding == nil {
		cfg.Forwarding = &Forwarding{}
	}

	if cfg.Forwarding.IPv4 == nil {
		cfg.Forwarding.IPv4 = new(true)
	}

	if cfg.Forwarding.IPv6 == nil {
		cfg.Forwarding.IPv6 = new(true)
	}

	if cfg.Firewall == nil {
		cfg.Firewall = &Firewall{}
	}

	if cfg.Firewall.ForwardDefault == "" {
		cfg.Firewall.ForwardDefault = ActionBlock
	}

	for i := range cfg.Rules {
		if cfg.Rules[i].Action == "" {
			cfg.Rules[i].Action = ActionAccept
		}
	}

	for i := range cfg.DHCPServers {
		if cfg.DHCPServers[i].LeaseTime == 0 {
			cfg.DHCPServers[i].LeaseTime = 12 * time.Hour
		}
	}

	if cfg.DNSServer != nil {
		if cfg.DNSServer.CacheSize == 0 {
			cfg.DNSServer.CacheSize = 10000
		}

		if cfg.DNSServer.RegisterDHCPLeases == nil {
			cfg.DNSServer.RegisterDHCPLeases = new(true)
		}

		if b := cfg.DNSServer.Blocking; b != nil {
			if b.Response == "" {
				b.Response = "nxdomain"
			}

			if b.Refresh == 0 {
				b.Refresh = 24 * time.Hour
			}
		}
	}

	for i := range cfg.PortForwards {
		if cfg.PortForwards[i].Reflection == nil {
			cfg.PortForwards[i].Reflection = new(true)
		}
	}

	if cfg.NTPServer != nil && len(cfg.NTPServer.Upstreams) == 0 {
		cfg.NTPServer.Upstreams = []string{"time.cloudflare.com"} // the Talos default
	}

	if cfg.UPnP != nil && len(cfg.UPnP.ExternalPorts) == 0 {
		cfg.UPnP.ExternalPorts = []PortRange{{Lo: 1024, Hi: 49151}}
	}

	for i := range cfg.PrefixDelegations {
		if cfg.PrefixDelegations[i].AcceptRA == nil {
			cfg.PrefixDelegations[i].AcceptRA = new(true)
		}
	}

	for i := range cfg.PPPoE {
		p := &cfg.PPPoE[i]
		if p.Interface == "" {
			p.Interface = "ppp0"
		}

		if p.MTU == 0 {
			p.MTU = 1492
		}

		if p.DefaultRoute == nil {
			p.DefaultRoute = new(true)
		}

		if p.IPv6 == nil {
			p.IPv6 = new(true)
		}
	}

	for i := range cfg.DynamicDNS {
		d := &cfg.DynamicDNS[i]
		if d.SSL == nil {
			d.SSL = new(true)
		}

		if d.Period == 0 {
			d.Period = 5 * time.Minute
		}
	}

	for i := range cfg.RouterAdvertisements {
		if cfg.RouterAdvertisements[i].RouterLifetime == 0 {
			cfg.RouterAdvertisements[i].RouterLifetime = 30 * time.Minute
		}
	}
}

// ZoneLinks returns the links of a zone.
func (cfg *Config) ZoneLinks(zone string) []string {
	for _, z := range cfg.Zones {
		if z.Name == zone {
			return z.Links
		}
	}

	return nil
}

// AddressGroup returns the address group with the given name.
func (cfg *Config) AddressGroup(name string) (AddressGroup, bool) {
	for _, g := range cfg.AddressGroups {
		if g.Name == name {
			return g, true
		}
	}

	return AddressGroup{}, false
}

// RulePorts returns the ports of a rule, expanding its port groups.
func (cfg *Config) RulePorts(r FirewallRule) []PortRange {
	ports := slices.Clone(r.Ports)

	for _, name := range r.PortGroups {
		for _, g := range cfg.PortGroups {
			if g.Name == name {
				ports = append(ports, g.Ports...)
			}
		}
	}

	return ports
}

// RuleSources returns the source subnets of a rule, expanding its source groups.
func (cfg *Config) RuleSources(r FirewallRule) []netip.Prefix {
	subnets := slices.Clone(r.SourceSubnets)

	for _, name := range r.SourceGroups {
		if g, ok := cfg.AddressGroup(name); ok {
			subnets = append(subnets, g.Subnets...)
		}
	}

	return subnets
}
