# Routal design

## Decisions
| Topic | Decision |
|---|---|
| Base | **Stock Talos Linux** (>= 1.15, Kubernetes-free mode `--skip-k8s-etcd`), not a fork. The fork (kept locally, unpublished) was dropped on 2026-11-13: same result for a router, without carrying 2000 files of merge conflicts. |
| Router logic | `routald`, a Talos **system extension** (baked into the image, starts at boot without network, versioned with the OS and rolled back with it). |
| Plugins | Talos `ContainerConfig` declarative containers (UI, adblock, monitoring...), pulled at runtime. |
| Config | Talos documents for the machine + Routal documents (`router.yaml`), delivered through `ExtensionServiceConfig`. |
| Secrets | File references in talos.yaml (`privateKeyFile`, `presharedKeyFile`), resolved by `routald talos-render` where the config is applied (laptop or CI); the files stay out of git. Talos has no file indirection of its own, and the machine config keeps the inline key. |
| Safety | Talos `apply-config --mode try --timeout` (auto-rollback) + A/B OS slots. |
| Data plane | Kernel forwarding + nftables. routald owns the `inet routal` table (and `inet routal_upnp` for miniupnpd's mappings); Talos owns `inet talos`. |
| Services | Kea (DHCPv4, stateless DHCPv6), Unbound (caching DNS), radvd (RA), chrony (NTP server), Avahi (mDNS reflector), miniupnpd (UPnP, built with nftables). Supervised by routald; each config is checked by the daemon before it is installed. dnsmasq was used first and replaced: Kea/Unbound are what OPNsense/pfSense run and scale further, at the cost of more daemons. |
| Hardware | x86_64 and arm64. |
| License | MIT (no Talos code is included; reusing a Talos file later would keep that file MPL-2.0 with its header). |

## How it fits together
```
git ──> talosctl patch mc --mode try (CI or on-device agent, later)
           │
        Talos machined ── ExtensionServiceConfig (router.yaml) ──> restarts ext-routald
           │                                                          │
     links/VLAN/WG/ingress fw (inet talos)          forwarding sysctls, inet routal (zones, NAT)
```

- `to: self` rules: Talos' ingress firewall must accept the packet, so `routald talos-patch` emits a
  `NetworkRuleConfig` for each; routald's `input` chain then drops it on links outside the zone.
- Port forwards: Talos' `prerouting` filter (priority -110) runs before routald's DNAT (-100) and drops
  new connections to the router's addresses, so `talos-patch` also opens each forwarded port there.
- Link aliases (`LinkAliasConfig`) are kernel `ifalias` values, not names; routald resolves them from
  `/sys/class/net/*/ifalias` and re-applies when they change (5 s reconcile loop).
- routald leaves its rules in place when it stops, so a restart never opens the firewall.
- DHCP client names reach DNS without restarting Unbound: routald reads Kea's lease file and pushes
  `local_data` through unbound-control (labels are validated: clients choose their names).
- Kea >= 2.6.3 only accepts lease files under its data directory: routald sets `KEA_DHCP_DATA_DIR`.
- UPnP: the mapping port range (default 1024-49151, never the Talos API) is opened in Talos; routald
  drops it on the external link unless miniupnpd DNATed it. Dropping it on every link broke replies
  to router daemons (the mDNS reflector's legacy unicast answers use ephemeral ports).
- Port forward reflection needs the router's public address: routald renders it from the link's
  current addresses and re-applies when they change.
- Talos creates every extension mount source, so `/run` in the container is a bind of the host's
  `/run/routal` (already a tmpfs), not a tmpfs mount.

## Verified (2026-11-13, Talos v1.15.0-beta.0, arm64 tart VM)
- Extension loads; the service writes the forwarding sysctls (needs `readonlyPaths` without `/proc/sys`).
- nft accepts the rendered ruleset alongside `inet talos`; Talos API still reachable with ingress `block`.
- `patch mc --mode try`: Talos restarts routald with the new router.yaml, then reverts it after the timeout.

Real-traffic tests (Mac as the WAN-side client, arm64 VM):
- Lockout: a `lan -> self tcp 50000` rule applied with `--mode try` cut talosctl after about 22 s.
  Talos reverted at 60 s and the API came back on its own.
- Port forward router:8080 -> Mac:8000: the page was served, and the Mac saw the request coming from
  the router (DNAT + forward + SNAT). Without the forward rule, the default policy blocks the request.
- tart's shared network isolates VMs from each other and drops 802.1Q frames, so the client-behind-router
  test runs on UTM instead.

Full topology on UTM (QEMU, arm64): router with WAN (shared) and LAN (host-only) NICs, and a client on
the LAN only. Both use the unmodified `examples/home-router` files; the MACs match its LinkAliasConfigs.
- The aliases resolve (`lan0=enp0s2 wan0=enp0s1`), and the client reaches the Internet only through the
  router. A capture shows `192.168.1.10:37668` on the LAN leaving the WAN as `192.168.64.41:37668`
  (masquerade).
- Port forward `nas-https`: `nc <router WAN> 443` from the Mac gets the client's answer.
- Removing `lan-to-wan` with `--mode try`: the client lost the Internet after about 13 s and got it back
  when Talos reverted at 60 s.
- Remote access over WireGuard (the `guest-and-vpn` pattern): a peer on the WAN side, a Docker container
  on the Mac, cannot reach 192.168.1.10 on its own. Through wg0 (zone `vpn`, rule `vpn-to-lan`, rule
  `wan -> self udp 51820`), it gets the NAS's answer and ping works. Without `vpn-to-lan` the tunnel still
  comes up but the NAS is blocked.
- NAS scenario (`examples/nas-vpn`, unmodified except the WireGuard keys), dual stack. The NAS sits on VLAN
  30 (zone `servers`). There is a LAN laptop VM, a WAN host VM with real routes to the NAS networks via
  the router, and a WireGuard peer. Results:

  | from | NAS v4:443 | NAS v6:443 | NAS v4:8080 | NAS v6:8080 |
  |---|---|---|---|---|
  | LAN laptop | OK | OK | blocked | blocked |
  | VPN peer | OK | OK | blocked | blocked |
  | VPN peer, tunnel down | blocked | blocked | - | - |
  | WAN host | blocked | blocked | blocked | blocked |

  WAN attempts show up in the router's kernel log as `routal wan-to-servers: IN=enp0s1 OUT=srv0 ...` for
  both families, so they reach the router and the rule drops them.
  In the test only, the VLAN MTU is 1496: the macOS virtual switch drops 1518-byte tagged frames.
- dnsmasq (same VMs; DHCP/RA only on the servers VLAN, because macOS already serves DHCP on the
  host-only network):
  - Static lease: `DHCPACK(srv0) 192.168.30.10 52:54:00:00:00:10 nas`, with the IPv4 default route
    coming from DHCP.
  - RA: the NAS got a SLAAC address (fd00:30::5054:ff:fe00:10) and an IPv6 default route to the router.
  - DNS from the LAN: `nas.home.arpa` -> 192.168.30.10 (DHCP name), and external names resolve.
  - The lease file under /var/lib/routal survives a router reboot.
  - Bugs found: Talos can start an extension service before the /var/lib volume exists (fixed by
    `depends: path: /var/lib`). The input chain dropped DNS from the LAN once another zone also had a
    `to: self` rule on port 53 (fixed: accepts per rule, then one drop per protocol on the merged ports).
- Kea/Unbound/radvd/chrony/Avahi on the same router VM, installed with `talosctl upgrade` to an
  installer built by `make installer` (pushed to a local registry): the extension survives the
  upgrade, all daemons start, Kea serves the NAS its static lease (hostname kept in the lease file),
  chrony runs next to Talos' NTP client. `talos-patch -current` removing a rule produced a
  `$patch: delete` that Talos applied.
  Gotchas: a failed extension service keeps the boot sequence in "booting", which blocks reboot and
  upgrade sequences (`talosctl reboot --mode force`, `upgrade --stage`); and Talos reuses an installer
  image already pulled under the same tag.
- How to reproduce: create the VMs with UTM AppleScript (`make new virtual machine`, QEMU backend,
  `network interfaces: {{mode:shared, address:"52:54:00:00:00:01"}, {mode:host, address:"52:54:00:00:00:02"}}`).
  Use an 8 GiB qcow2 converted from `make image`.

## Network namespace and containerlab tests (`make integration`, `make clab`)
Router, two LAN clients, a NAS on VLAN 30, a WAN host and a WireGuard peer, each in its own network
namespace in one privileged container; the router runs routald with `examples/nas-vpn` plus a port
forward and UPnP. 37 checks: DHCP (static and dynamic leases, options), SLAAC, DNS (static and
dynamic names, reverse, forwarding), the NAS matrix in IPv4 and IPv6, masquerade, port forward,
reflection, UPnP mapping and secure mode, NTP, mDNS across the VLAN. Talos itself is emulated
(links, addresses, aliases); its ingress firewall is not.
The checks (test/integration/checks.sh) are shared: `make integration` runs them in network
namespaces, `make clab` on test/clab/routal.clab.yml (one container per node, a Linux bridge as
the LAN switch, containerlab's management network with its default routes removed). Next nodes
for the lab: an ISP with PPPoE (accel-ppp) and DHCPv6-PD once routald has the clients.

## WAN (2026-11-14)
- PPPoE: pppd with rp-pppoe's user-space `pppoe` through pppd's pty (the Talos kernel has PPP and
  PPP_ASYNC, not PPPOE). One supervised pppd per PPPoEConfig; its options file holds the password
  and is written 0600. TCP MSS is clamped to the route MTU on SYNs leaving the session.
- Prefix delegation: a DHCPv6 client in routald (pkg/pd for the protocol, cmd/routald/pd.go for the
  loop): Solicit/Request, Renew at T1, Rebind at T2, start over on expiry. DUID-LL from the link's
  MAC (stable without state), or a persisted DUID-UUID on PPP links. Each subnet link gets
  `<prefix>:<id>::1/64` with the lease lifetimes (the kernel drops it if routald is gone); the rest
  of the prefix is routed `unreachable` so it never loops back to the ISP. accept_ra=2 on the WAN
  link keeps the ISP's default route with forwarding on. RouterAdvertisementConfig without
  prefixes renders the /64s present on the link, so radvd restarts with a new delegated prefix and
  announces it with the address lifetimes. talos-patch opens udp 546 from fe80::/10 in Talos.
- Dynamic DNS: inadyn, one provider section per DynamicDNSConfig; the address comes from the link
  (`routald addr`) rather than a web service when `link` is set.
- Secrets of router.yaml (`passwordFile`) are relative to it; talos-patch adds each file as another
  ExtensionServiceConfig configFile next to /etc/routal/router.yaml.
- Tested in the namespace and containerlab labs against a simulated ISP, then on Talos (below).

## On Talos VMs (UTM, 2026-11-14)
Router: the image from `make image`, upgraded twice with `talosctl upgrade --stage` to installers
from `make installer` (local registry). ISP: an Alpine VM (Kea DHCPv6 with a prefix pool,
pppoe-server, a blocklist and a dyndns2 server, NAT). Client: an Alpine VM on the servers VLAN
running its checks at boot and serving them over HTTP (no SSH needed).
- Prefix delegation: the /56 is obtained and renewed every 30 s; when the ISP VM was replaced, renew
  and rebind failed and a new Solicit got the prefix back. Talos leaves routald's addresses and the
  unreachable route alone. The client gets a SLAAC address in the delegated /64.
- PPPoE: Talos gives extension services no host devices, so routald now creates /dev/ppp
  (mknod 108:0, allowed). The session comes up, pppd's default route replaces Talos' DHCP one and
  Talos does not put it back; the client reaches the Internet through the session.
- Dynamic DNS reaches the provider; a provider error made inadyn exit and routald restart it every
  5 s, hence the restart backoff (5 s doubling to 5 min).
- Blocking (list from the ISP, inline names, subdomains), Kea leases and names, NTP, UPnP mapping,
  port forward from the WAN, mDNS across the VLAN: all work.
- Talos' ingress firewall drops the unicast answers to a "legacy" mDNS query (a query from a random
  port, as dig sends): conntrack cannot match them with the reflector's multicast query. Devices
  query from port 5353 in multicast, which works.
- Talos' strict reverse path filter drops replies that come back on another interface than the
  route to their source (seen with a test client having addresses on two LAN segments).

## DNS blocking (2026-11-14)
`DNSServerConfig.blocking`: lists by URL (hosts, domain lists, simple AdBlock `||name^` rules),
inline domains, an allow list, NXDOMAIN or null answers. routald downloads the lists in the
background (cached in the state directory, a failed refresh keeps the previous copy, Talos' host
DNS when the container has no resolv.conf) and pushes `local_zones` through unbound-control, so a
refresh does not flush the cache. Subdomains of blocked names are folded into their parent's zone;
an allowed name inside a blocked zone becomes a transparent zone.

## Test coverage
`make cover` merges the unit tests' coverage with a routald built with `-cover` running the whole
`make integration` scenario: 91% of statements. What is left is mostly error paths of the DHCPv6
client (server gone during renewal, prefix change).

## Talos kernel (v1.15.0-beta.0), checked on the VM
| Feature | Kernel | Consequence |
|---|---|---|
| PPPoE | `CONFIG_PPP=y`, `CONFIG_PPP_ASYNC=y`, `CONFIG_PPPOE` not set | user-space PPPoE (rp-pppoe through pppd's pty) works from the extension; kernel PPPoE needs the option upstream |
| Flowtables offload | `CONFIG_NF_FLOW_TABLE` not set | not possible on stock Talos |
| cake | `CONFIG_NET_SCH_CAKE` not set | SQM possible with HTB + fq_codel (both built in, `ifb` as a module) |
| Own modules | `module.sig_enforce=1` | a module extension must be signed with the Talos kernel key: kernel options must come from siderolabs/pkgs |

## Next
1. Kernel options upstream (siderolabs/pkgs, as modules): `PPPOE`, `NF_FLOW_TABLE(_INET)` + `NFT_FLOW_OFFLOAD`, `NET_SCH_CAKE`.
2. WAN: test PPPoE and prefix delegation on Talos (VM); prefix delegation over PPPoE in the lab;
   ISP DHCP options (e.g. option 90, VLAN priority); deprecate the old prefix (lifetime 0) when the
   ISP changes it.
3. SQM: `SQMConfig` with HTB + fq_codel on the WAN (egress) and an ifb (ingress); cake when available.
4. Flowtables offload once the kernel has it (`flowtable` + `flow add @ft` for established flows).
5. FQDN-based rules: Unbound has no nftset; options are a dnsmasq forwarder with nftset (custom build)
   or routald reading Unbound's answers (dnstap) and filling nftables sets.
6. GitOps: CI workflow (render + `--mode try` + health check + confirm), then an on-device pull agent.
7. Stateful DHCPv6 (`managed: true`, Kea), DHCPv6-PD server for downstream routers, Kea HA.
8. End-to-end Talos test in CI: QEMU VM (Linux runner with KVM) booting the built image, config
   applied, upgrade to the installer, routald and the Talos ingress firewall checked. Containerlab
   (`make clab`) covers routald and the daemons; Talos in a container shares the host kernel and
   has no system extensions, so it needs the VM.
9. VRRP for router HA; `routalctl` wrapper (validate, diff, apply) if `talosctl` + `routald` aren't enough.
