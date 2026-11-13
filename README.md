# Routal

> [!WARNING]
> **Experiment, not a real project.** This repo is AI-generated code ("AI slop"), written mostly to
> try out an idea: running a router on stock Talos Linux with a system extension. It has only been
> tested in a single VM, has no real-traffic tests, and is not maintained. Do not put it in front of
> anything you care about.

A GitOps-managed router built on [Talos Linux](https://www.talos.dev): stock Talos runs the
machine, and the `routald` system extension turns it into a router.

| Talos (unmodified) | routald extension |
|---|---|
| interfaces, VLANs, bonds, bridges, WireGuard, DHCP client, BGP | IP forwarding |
| ingress firewall for the router itself | zone firewall for forwarded traffic |
| A/B upgrades, `apply-config --mode try` (auto-rollback) | masquerade / SNAT, port forwards |
| API, `talosctl`, Kubernetes-free mode | DHCP server, caching DNS, IPv6 RA (dnsmasq) |

Config is two sets of YAML documents, both kept in git:

- `talos.yaml`: Talos documents (links, addresses, ingress rules), see `examples/*/talos.yaml`
- `router.yaml`: Routal documents read by routald, see `examples/*/router.yaml`

```sh
routald validate router.yaml
routald render router.yaml                       # nftables ruleset it will apply
routald render-dnsmasq router.yaml               # dnsmasq config (DHCP, DNS, RA)
routald talos-patch router.yaml > router-patch.yaml
talosctl patch mc --patch @router-patch.yaml --mode try --timeout 2m   # reverts unless confirmed
```

`talos-patch` wraps router.yaml in an `ExtensionServiceConfig` (Talos restarts routald with the new
file) and turns `to: self` rules and port forwards into Talos `NetworkRuleConfig` documents
(Talos filters traffic to the router's addresses before routald's DNAT runs).

## Router documents (`apiVersion: v1alpha1`)

| Kind | Purpose | Applied |
|---|---|---|
| `ForwardingConfig` | IPv4/IPv6 forwarding (default on) | yes |
| `FirewallConfig` | default for forwarded traffic (`block`), `logBlocked` | yes |
| `FirewallZoneConfig` | named group of links | yes |
| `FirewallRuleConfig` | stateful rule from a zone to a zone or `self` | yes |
| `FirewallAddressGroupConfig` | named IPv4/IPv6 subnets, used as `sourceGroups`/`destinationGroups` | yes (nftables sets) |
| `FirewallPortGroupConfig` | named ports, used as `portGroups` | yes (nftables sets) |
| `MasqueradeConfig` | source NAT on an outgoing link | yes |
| `PortForwardConfig` | destination NAT on an incoming link | yes |
| `DHCPServerConfig` | DHCPv4 pools and static leases (by MAC) | yes (dnsmasq) |
| `DNSServerConfig` | caching DNS, local domain, DHCP names, static records | yes (dnsmasq) |
| `RouterAdvertisementConfig` | IPv6 SLAAC (optionally DHCPv6 options) | yes (dnsmasq) |

Links can be kernel names or Talos `LinkAliasConfig` aliases. routald programs its own
`inet routal` nftables table; Talos keeps `inet talos`.

## Build

Requires Docker (Go builds run in a container).

```sh
make test
make check                     # tests + every example's dnsmasq config checked by the real dnsmasq
make extension ARCH=arm64      # _out/routald-arm64.tar (flat extension for the imager)
make image ARCH=arm64          # _out/metal-arm64.raw, Talos with routald baked in
```

Booting the image: apply `talosctl gen config --skip-k8s-etcd` plus your patches, and delete the
generated `UnattendedInstallConfig` (its stock installer would drop the extension) until Routal
publishes its own installer image.

## License

MIT, see LICENSE. Third-party components in the extension image are listed in NOTICE.
