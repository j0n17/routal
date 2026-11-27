<p align="center"><img src="assets/logo.svg" width="300" alt="routal"></p>

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
| interfaces, VLANs, bonds, bridges, WireGuard, DHCP client, static routes | IP forwarding |
| BGP (`BGPInstanceConfig`), VRFs (`VRFConfig`), policy routing (`RoutingRuleConfig`) | firewall, NAT and services that follow links into a VRF |
| ingress firewall for the router itself | zone firewall for forwarded traffic, address/port groups |
| A/B upgrades, `apply-config --mode try` (auto-rollback) | masquerade / SNAT, port forwards (with NAT loopback), UPnP |
| | WAN: PPPoE, IPv6 prefix delegation, dynamic DNS |
| API, `talosctl`, Kubernetes-free mode | DHCP (Kea), caching DNS with blocklists (Unbound), IPv6 RA (radvd), NTP (chrony), mDNS reflector (Avahi) |

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
| [`examples/bgp-vrf/`](examples/bgp-vrf/) | a network in its own VRF, announced to a core router over BGP, with DHCP/DNS/NTP inside the VRF |
| [`examples/nas-vpn/`](examples/nas-vpn/) | NAS on its own VLAN, reachable from the LAN and the VPN only, dual stack, NTP, mDNS |

All examples are checked by `make check` (routald, the real daemons, and `talosctl validate`).

### Editor support

`router.yaml` has a JSON schema, generated from routald's types: with the
[YAML language server](https://github.com/redhat-developer/yaml-language-server) (VS Code's YAML
extension, Neovim, Helix, JetBrains...) the editor completes kinds and fields, shows their
documentation and underlines mistakes while you type. Put this on the first line of router.yaml:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/j0n17/routal/main/pkg/schema/router.schema.json
```

or keep the schema of your routald version next to the file (`routald schema > router.schema.json`,
then `$schema=./router.schema.json`). The schema sees one document at a time: types, unknown
fields, allowed values, link names, addresses, ports. What needs the whole file (a rule naming a
zone that does not exist, a DHCP range outside its subnet, a missing secret) is `routald validate`'s.

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

router.yaml does the same for its own secrets (`passwordFile` of PPPoE and dynamic DNS, relative to
router.yaml): `routald talos-patch` ships each file next to router.yaml on the router.

### Apply

```sh
routald validate router.yaml
routald render router.yaml                       # nftables ruleset routald will apply
routald render-services router.yaml out/         # daemon configs (Kea, Unbound, radvd, chrony...)

# machine (links, addresses, WireGuard...)
routald talos-render talos.yaml > /tmp/talos.yaml
talosctl patch mc --patch @/tmp/talos.yaml --mode try --timeout 2m

# router: Talos reverts the config unless the router reports healthy with it
bin/routal-apply router.yaml
```

`bin/routal-apply` validates the file, turns it into a Talos patch (`routald talos-patch -current`),
applies it with `--mode try`, and confirms it only when the router is healthy with it: routald's
own report says so and every `--check 'command'` passes. A config that locks you out is never
confirmed: Talos takes it back after the timeout.

routald reports its health after every reconcile (every 5 s): whether the config is applied, and
for each daemon whether it runs and stays up. Changes are logged (`health: NOT OK: ...`), and the
report can be read at any time:

```sh
talosctl read /run/routal/routal/status.json | routald status -   # exit status 1 when not healthy
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
| `DNSServerConfig` | caching DNS, local domain, DHCP client names, static records, blocklists (AdGuard / Pi-hole style), DNS over TLS upstreams (`tlsUpstreams`), DNSSEC validation (`dnssec`) | Unbound |
| `RouterAdvertisementConfig` | IPv6 SLAAC, optionally DHCPv6 options (`other`) | radvd, Kea |
| `NTPServerConfig` | time for local networks (does not touch the clock, Talos does) | chrony |
| `MDNSReflectorConfig` | mDNS/Bonjour across links (printers, AirPlay, Chromecast across VLANs) | Avahi |
| `PPPoEConfig` | dial the ISP over PPPoE (also on a VLAN); the session is a link (`ppp0`) for zones and NAT | pppd + rp-pppoe |
| `PrefixDelegationConfig` | IPv6 prefix from the ISP (DHCPv6 IA_PD), a /64 per LAN link, followed by router advertisements | routald |
| `DynamicDNSConfig` | keep a public name on the WAN address (Cloudflare, DuckDNS, dyndns2...) | inadyn |

Links are kernel names or Talos `LinkAliasConfig` aliases (a letter first, at most 15 characters). routald programs its own `inet routal`
nftables table; Talos keeps `inet talos`. Unknown fields are errors.

### Hardening

- Every daemon but pppd runs under its own account (`kea`, `unbound`, `radvd`, `chrony`, `avahi`,
  `miniupnpd`, `inadyn`) with the one or two capabilities it needs (e.g. `CAP_NET_BIND_SERVICE`),
  and only writes in its own directory. routald itself and pppd stay root.
- The links to the Internet (masquerade `outLink`, PPPoE sessions, prefix delegation links, the
  UPnP external link) are closed by default for traffic to the router itself, whatever
  `talos.yaml` opens: the Talos API (tcp 50000-50001) included. What gets in: answers, ICMP
  errors, a rate-limited ping, IPv6 neighbor discovery, router advertisements and MLD, DHCP and
  DHCPv6 answers, and what a `to: self` rule from that zone opens (WireGuard, BGP, or the Talos
  API with `sourceSubnets`, which this table now matches too).
- With UPnP, the mapping range is closed on every link for whatever has no mapping.
- A DHCP client cannot register the names `wpad`, `isatap` or `localhost`.
- `dnssec: true` makes Unbound refuse forged answers; `tlsUpstreams` encrypts the queries to the
  upstream resolvers (DNS over TLS; Unbound cannot forward over HTTPS).

### BGP and VRFs

Talos does both itself: `BGPInstanceConfig` (sessions, announced links, learned routes installed
in the kernel) and `VRFConfig` (a routing table of its own for a set of links), in `talos.yaml`.
routald does the rest, with nothing to declare in `router.yaml`:

- a `to: self` rule for tcp/179 lets the neighbor in; zone rules decide what transits;
- links in a VRF keep their names in zones, NAT and port forwards (in the firewall hooks the
  kernel reports the VRF as the input interface; routald matches the link behind it);
- DHCP, DNS and NTP serve links in a VRF: routald adds a routing rule for each of the router's
  addresses there and sets the `l3mdev_accept` and `ip_nonlocal_bind` sysctls. The daemons' own
  traffic (DNS upstreams, NTP sources) uses the default routing domain.

Checked on a Talos VM against a bird peer (IPv4); see [`examples/bgp-vrf/`](examples/bgp-vrf/).

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

### Releases

A tag `vX.Y.Z` publishes (`.github/workflows/release.yml`) the installer with the extension for
every tested Talos version, amd64 and arm64, as
`ghcr.io/<owner>/routal-installer:<X.Y.Z>-talos-<talos version>`, and the extension tarballs on a
GitHub release. Supported Talos: 1.14 and later.

## Tests

```sh
make test          # unit tests, gofmt, go vet, config variants (valid and invalid)
make check         # + every example and variant checked by the real tools: nft -c, kea -t,
                   #   unbound-checkconf, radvd -c, chronyd -p, inadyn --check-config, talosctl validate
make cover         # coverage of the unit tests and of an instrumented routald in make integration
make schema        # regenerate the JSON schema of router.yaml after changing pkg/config (make test fails when it is stale)
make fuzz          # fuzz the parsers and the config (FUZZTIME=1m per target); the real tools check what is rendered
make fuzz-firewall # random rules in a real kernel, probed from every zone over IPv4 and IPv6, against a model of router.yaml
make lint          # staticcheck and govulncheck (part of make check); make test runs the unit tests with the race detector
make mutation      # mutation testing: deliberate bugs, one by one, that the test suite must notice (slow; weekly in CI)
make site          # the web page (reference, examples, personas), published by GitHub Pages
make integration   # data plane in network namespaces: router, LAN clients, NAS VLAN, WAN host, VPN peer
make clab          # the same checks on a containerlab topology (test/clab/routal.clab.yml)
```

GitHub Actions (`.github/workflows/ci.yml`) runs `make check`, `make integration`, `make fuzz
FUZZTIME=30s` and builds the extension for amd64 and arm64 on every push and pull request.
It also validates the examples, builds the installer and boots it for every Talos version listed
by `hack/talos-versions.sh` (the last three releases of each supported line, today 1.14 and 1.15),
on every push and every Monday. For one version locally:
`make check-talos installer TALOS_VERSION=v1.14.7`.

`make vm-test` boots that Talos with the extension in a VM (tart on an Apple silicon Mac; QEMU
with KVM and sudo on Linux, which is what CI runs for every version), applies `test/vm`, and
checks what only a real Talos shows: the extension starts, every daemon runs under its account
with its capabilities, and the WAN side is closed. On Linux the VM has a second NIC with a client
behind it: DHCP lease, SLAAC, DNS over TLS with DNSSEC, and the Internet through the router.
It then plays personas (`test/vm/personas`): each is a complete `router.yaml` for one kind of
user, applied the way a user would, with what that user expects to work and not to work.

| Persona | Expects |
|---|---|
| `01-family` | Internet and DNS for every device, ad domains blocked, nothing reachable from outside |
| `02-family-parental` | sites blocked by name; the children's devices cannot use another resolver, the parents' can |
| `03-gamer` | a game opens its port with UPnP and is reachable on it; it cannot open one for another device |
| `04-homelab` | a NAS and a web server with fixed addresses and names, each published on its port; the whole LAN over WireGuard |
| `05-guests` | visitors on their own VLAN get the Internet, and reach neither the house nor the router's API |
| `06-hotel` | guests get tcp 80 and 443 through the hotel's DNS and nothing else; the staff network is unrestricted and out of reach |

On the newest Talos version CI goes further (`ADVANCED=1`): after each persona every TCP port is
scanned from outside over IPv4 and IPv6; the homelab goes through a reboot and a WAN outage; then
`test/vm/advanced.sh` boots the router with no Internet, after a power cut with damaged files, with
a full disk and with its clock in the year 2000, runs `bin/routal-apply` with a good config and
with one that locks the admin out, and exhausts the DHCP pool and the connection table.
`make vm-upgrade-test` boots an older build and upgrades it to this one.

It removes the VM and the image when it ends.

Both run the real routald and daemons with `examples/nas-vpn` and the same 91 checks
(test/integration/checks.sh): DHCP, SLAAC, DNS, DNS blocking, the firewall in IPv4 and IPv6, NAT,
reflection, UPnP, NTP, mDNS, the hardening above (daemon accounts, UPnP range, WAN closed by default), options applied on the running router (SNAT to a fixed address, an
IPv6 port forward with `toPort`, `forwardDefault: accept`, blocking with 0.0.0.0 answers, DHCPv6
options, IPv6 forwarding off), then the WAN against a simulated ISP: IPv6 prefix delegation (Kea)
with renewal, an ISP that is down at start, goes down during the lease and comes back with
another prefix, dynamic DNS, and a PPPoE session (pppoe-server).

`test/variants/` holds router configs that exercise the options the examples do not, and invalid
ones with the error they must produce (`# error:` on the first line). `make integration` needs only Docker (one privileged container, network
namespaces). `make clab` runs containerlab in its own container, on Linux or macOS with Docker
Desktop, one container per node; `KEEP=1 make clab` leaves the lab up to poke at it
(`docker exec -it clab-routal-router sh`). Talos itself (boot, upgrades, its ingress firewall) was
tested in UTM VMs, see DESIGN.md.

## License

MIT, see LICENSE. Third-party components in the extension image are listed in NOTICE.
