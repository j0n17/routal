<p align="center"><img src="assets/logo.svg" width="300" alt="routal"></p>

# Routal

> [!WARNING]
> **A toy experiment more than a product.** Routal is AI-generated code ("AI slop"), written to try
> out an idea: running a router on stock Talos Linux with a system extension. It has only been
> tested in VMs and network namespaces, never on real hardware or with a real ISP. It is
> maintained, at its own pace: there is no release schedule and no promise of support. Here be
> dragons: do not put it in front of anything you care about unless you have reviewed the code or
> accept unknown risks.

A GitOps-managed router built on [Talos Linux](https://www.talos.dev): stock Talos runs the
machine, and the `routald` system extension turns it into a router.

| Talos (unmodified) | routald extension |
|---|---|
| interfaces, VLANs, bonds, bridges, WireGuard, DHCP client | IP forwarding, zone firewall, NAT, port forwards, UPnP |
| BGP, VRFs, policy routing | WAN: PPPoE, IPv6 prefix delegation, dynamic DNS |
| ingress firewall for the router itself | DHCP, caching DNS with blocklists, IPv6 RA, NTP, mDNS |
| A/B upgrades, `apply-config --mode try` | health report, apply with automatic rollback |

## Quickstart: a router in a VM

Needs Docker and `talosctl`, plus [tart](https://tart.run) on an Apple silicon Mac, or QEMU with
KVM and sudo on Linux.

```sh
make vm          # builds Talos with routald, boots it in a VM, configures it, leaves it running
```

It prints the VM's address. Then:

```sh
export TALOSCONFIG=$PWD/_out/vm-test/gen/talosconfig
talosctl -n <ip> -e <ip> logs -f ext-routald

$EDITOR _out/vm-test/router.yaml
make vm-apply    # rolled back by Talos unless the router reports healthy
make vm-stop     # removes the VM
```

On a Mac the VM has one NIC, the WAN, and the Mac is on the Internet side of it. On Linux it also
has a LAN with a client behind it: `sudo ip netns exec routal-client sh`.

## A router is a directory in git

```
my-router/
├── talos.yaml      Talos documents: links, addresses, VLANs, WireGuard...
├── router.yaml     Routal documents, read by routald
└── secrets/        key files referenced by talos.yaml (git-ignored)
```

```sh
routald validate router.yaml
routald talos-render talos.yaml > /tmp/talos.yaml
talosctl patch mc --patch @/tmp/talos.yaml --mode try --timeout 2m
bin/routal-apply router.yaml     # rolled back unless the router reports healthy
```

## Examples

| Example | What it shows |
|---|---|
| [`reference`](examples/reference/) | **every document and every field**, commented |
| [`home-router`](examples/home-router/) | WAN + LAN, DHCP, DNS, a port forward, UPnP |
| [`guest-and-vpn`](examples/guest-and-vpn/) | isolated guest VLAN, WireGuard remote access |
| [`bgp-vrf`](examples/bgp-vrf/) | a network in its own VRF, announced over BGP |
| [`nas-vpn`](examples/nas-vpn/) | NAS on its own VLAN, reachable from LAN and VPN only |

More on the [web page](https://j0n17.github.io/routal/): field reference, examples, and seven
tested personas (family, gamer, homelab, hotel, company...).

## Documentation

- [Configuration](docs/configuration.md): documents, link names, secrets, applying, health, editor support
- [Security](docs/security.md): what is closed by default, daemon accounts
- [BGP and VRFs](docs/bgp-vrf.md)
- [Build, install, upgrade](docs/install.md)
- [Tests](docs/testing.md): unit, fuzzing, network namespaces, real Talos VMs, personas
- [Design](DESIGN.md)

## Quick build

Requires Docker.

```sh
make installer ARCH=arm64      # Talos installer with routald
make check                     # every test that needs no VM
```

## License

MIT, see LICENSE. Third-party components in the extension image are listed in NOTICE.
