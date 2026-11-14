# Routal

> [!WARNING]
> **Experiment, not a real project.** This repo is AI-generated code ("AI slop"), written mostly to
> try out an idea: running a router on stock Talos Linux with a system extension. It has only been
> tested in VMs and network namespaces, never on real hardware or a real ISP, and is not maintained.
> Do not put it in front of anything you care about.

A GitOps-managed router built on [Talos Linux](https://www.talos.dev): stock Talos runs the
machine, and the `routald` system extension turns it into a router.

| Talos (unmodified) | routald extension |
|---|---|
| interfaces, VLANs, bonds, bridges, WireGuard, DHCP client, static routes, BGP | IP forwarding |
| ingress firewall for the router itself | zone firewall for forwarded traffic, address/port groups |
| A/B upgrades, `apply-config --mode try` (auto-rollback) | masquerade / SNAT, port forwards (with NAT loopback), UPnP |
| API, `talosctl`, Kubernetes-free mode | DHCP (Kea), caching DNS (Unbound), IPv6 RA (radvd), NTP (chrony), mDNS reflector (Avahi) |

## Configuration

A router is a directory in git:

```
my-router/
├── talos.yaml      Talos documents: links, aliases, addresses, VLANs, WireGuard, ingress rules
├── router.yaml     Routal documents, read by routald
└── secrets/        key files referenced by talos.yaml (git-ignored, or decrypted by CI)
```

| Example | What it shows |
|---|---|
| [`examples/reference/`](examples/reference/) | **every document and every field**, commented, with defaults |
| [`examples/home-router/`](examples/home-router/) | WAN + LAN, DHCP, DNS, a port forward, UPnP |
| [`examples/guest-and-vpn/`](examples/guest-and-vpn/) | guest VLAN isolated from the LAN, WireGuard remote access |
| [`examples/nas-vpn/`](examples/nas-vpn/) | NAS on its own VLAN, reachable from the LAN and the VPN only, dual stack, NTP, mDNS |

All examples are checked by `make check` (routald, the real daemons, and `talosctl validate`).

### Secrets

Talos wants WireGuard keys inline. In `talos.yaml`, reference a file instead (path relative to
`talos.yaml`), like a key path on an EdgeRouter:

```yaml
kind: WireguardConfig
name: wg0
privateKeyFile: secrets/wg0.key        # wg genkey > secrets/wg0.key
peers:
  - publicKey: ...
    presharedKeyFile: secrets/phone.psk  # wg genpsk > secrets/phone.psk
```

`routald talos-render talos.yaml` prints the documents with the keys inlined; apply its output, never
commit it. `secrets/` is in `.gitignore` (the examples hold placeholders).

### Apply

```sh
routald validate router.yaml
routald render router.yaml                       # nftables ruleset routald will apply
routald render-services router.yaml out/         # daemon configs (Kea, Unbound, radvd, chrony...)

# machine (links, addresses, WireGuard...)
routald talos-render talos.yaml > /tmp/talos.yaml
talosctl patch mc --patch @/tmp/talos.yaml --mode try --timeout 2m

# router
talosctl get mc -o yaml > /tmp/current.yaml
routald talos-patch -current /tmp/current.yaml router.yaml > /tmp/router-patch.yaml
talosctl patch mc --patch @/tmp/router-patch.yaml --mode try --timeout 2m   # reverts unless confirmed
```

`talos-patch` wraps router.yaml in an `ExtensionServiceConfig` (Talos restarts routald with the new
file) and turns `to: self` rules, port forwards and UPnP into Talos `NetworkRuleConfig` documents
(Talos filters traffic to the router's addresses before routald's DNAT runs). With `-current`, it
also deletes the `routal-*` rules the config no longer needs (patches otherwise only add).

### Router documents (`apiVersion: v1alpha1`)

Every field is in [`examples/reference/router.yaml`](examples/reference/router.yaml).

| Kind | Purpose | Implemented by |
|---|---|---|
| `ForwardingConfig` | IPv4/IPv6 forwarding (default on) | sysctls |
| `FirewallConfig` | default for forwarded traffic (`block`), `logBlocked` | nftables |
| `FirewallZoneConfig` | named group of links | nftables |
| `FirewallRuleConfig` | stateful rule from a zone to a zone or `self`, first match wins | nftables |
| `FirewallAddressGroupConfig` | named IPv4/IPv6 subnets, used as `sourceGroups`/`destinationGroups` | nftables sets |
| `FirewallPortGroupConfig` | named ports, used as `portGroups` | nftables sets |
| `MasqueradeConfig` | source NAT on an outgoing link | nftables |
| `PortForwardConfig` | destination NAT on an incoming link, reflected for inside clients (`reflection`, default on) | nftables |
| `UPnPConfig` | devices open their own forwards (UPnP IGD, NAT-PMP, PCP), to their own address only | miniupnpd |
| `DHCPServerConfig` | DHCPv4 pools, options and static leases (by MAC) | Kea |
| `DNSServerConfig` | caching DNS, local domain, DHCP client names, static records | Unbound |
| `RouterAdvertisementConfig` | IPv6 SLAAC, optionally DHCPv6 options (`other`) | radvd, Kea |
| `NTPServerConfig` | time for local networks (does not touch the clock, Talos does) | chrony |
| `MDNSReflectorConfig` | mDNS/Bonjour across links (printers, AirPlay, Chromecast across VLANs) | Avahi |

Links are kernel names or Talos `LinkAliasConfig` aliases. routald programs its own `inet routal`
nftables table; Talos keeps `inet talos`. Unknown fields are errors.

## Build and install

Requires Docker (Go builds and Talos tools run in containers).

```sh
make extension ARCH=arm64      # _out/routald-arm64.tar (flat extension for the imager)
make image ARCH=arm64          # _out/metal-arm64.raw, Talos with routald baked in
make installer ARCH=arm64      # _out/installer-arm64.tar, the Talos installer with routald (OCI image)
make push-installer IMAGE=ghcr.io/<you>/routal-installer:v0.1.0
```

Install: boot the image, apply `talosctl gen config --skip-k8s-etcd` plus your `talos.yaml`, with
`UnattendedInstallConfig` pointing at your installer (see the reference; the stock installer would
drop the extension). Upgrade: `talosctl upgrade --image <installer>:<new tag>`, then
`talosctl reboot` (in Kubernetes-free mode Talos does not reboot by itself). Use a new tag per
build: Talos reuses an image it already pulled under the same tag.

## Tests

```sh
make test          # unit tests, gofmt, go vet
make check         # + every example checked by the real tools: nft -c, kea -t, unbound-checkconf,
                   #   radvd -c, chronyd -p, talosctl validate
make integration   # data plane in network namespaces: router, LAN clients, NAS VLAN, WAN host, VPN peer
make clab          # the same checks on a containerlab topology (test/clab/routal.clab.yml)
```

Both run the real routald and daemons with `examples/nas-vpn` and the same 37 checks
(test/integration/checks.sh): DHCP, SLAAC, DNS, the firewall in IPv4 and IPv6, NAT, reflection,
UPnP, NTP, mDNS. `make integration` needs only Docker (one privileged container, network
namespaces). `make clab` runs containerlab in its own container, on Linux or macOS with Docker
Desktop, one container per node; `KEEP=1 make clab` leaves the lab up to poke at it
(`docker exec -it clab-routal-router sh`). Talos itself (boot, upgrades, its ingress firewall) was
tested in UTM VMs, see DESIGN.md.

## License

MIT, see LICENSE. Third-party components in the extension image are listed in NOTICE.
