// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the set of router documents.
type Config struct {
	Forwarding           *Forwarding
	Firewall             *Firewall
	Zones                []FirewallZone
	Rules                []FirewallRule
	Masquerades          []Masquerade
	PortForwards         []PortForward
	DHCPServers          []DHCPServer
	DNSServer            *DNSServer
	RouterAdvertisements []RouterAdvertisement
}

// LoadFile reads, parses and validates a router config file.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	return Load(data)
}

// Load parses a multi-document YAML router config, applies defaults and validates it.
func Load(data []byte) (*Config, error) {
	cfg := &Config{}
	dec := yaml.NewDecoder(bytes.NewReader(data))

	for i := 1; ; i++ {
		var node yaml.Node

		if err := dec.Decode(&node); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			return nil, fmt.Errorf("document %d: %w", i, err)
		}

		if len(node.Content) == 0 || (node.Content[0].Kind == yaml.MappingNode && len(node.Content[0].Content) == 0) {
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

	return cfg, nil
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
	case "FirewallZoneConfig":
		return appendDoc(node, &cfg.Zones)
	case "FirewallRuleConfig":
		return appendDoc(node, &cfg.Rules)
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
