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
- UPnP: the mapping port range (default 1024-49151, never the Talos API) is opened in Talos for
  every source (its rules cannot name a link); routald drops, on every link, what reaches the
  router itself there without a mapping or a rule. One exception, which conntrack cannot match:
  unicast answers (from port 5353, to an ephemeral port) to the mDNS reflector's queries.
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
forward and UPnP. 90 checks: DHCP (static and dynamic leases, options), SLAAC, DNS (static and
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

## BGP and VRFs (2026-11-14)
Talos has both: `BGPInstanceConfig` (embedded GoBGP: sessions, links to announce, learned routes
installed as routes of the instance's table, optional `vrf`) and `VRFConfig`; also
`RoutingRuleConfig`. Nothing to supervise in the extension. What routald had to learn:
- nftables with a VRF: prerouting sees the link, then the VRF takes the packet, and the input and
  forward hooks see the VRF device as `iifname` and the link as `sdifname` (only valid in those two
  hooks). Output interfaces are always the links. So: the forward chain jumps to one chain per
  source zone through two verdict maps (`iifname vmap`, `meta sdifname vmap`); `to: self` accepts
  are rendered for both; reflected port forwards are marked in the forward chain (bit 0x10000000)
  and masqueraded on that mark in postrouting, where `sdifname` is not available.
- Services: the daemons run in the default routing domain. A link's addresses in a VRF are local in
  the VRF's table only: Kea could not bind 192.168.30.1 ("Address not available"), and UDP answers
  would be routed by the main table. routald adds a routing rule per address (`from <addr> lookup
  <vrf table>`, priority 900) and sets `tcp_l3mdev_accept`, `udp_l3mdev_accept` and
  `ip_nonlocal_bind`. One instance of each daemon then serves default and VRF links alike.
- Verified on a Talos VM (UTM), with an Alpine VM holding a bird peer (AS 65002, announcing
  198.51.100.0/24) and a client in two network namespaces on VLANs of the trunk; uplink and LAN in
  `vrf-blue`, the BGP instance in the VRF, management in the default domain. Session established;
  the peer learns the LAN, Talos installs the peer's route in table 100; client -> announced
  network passes, peer -> client only on the allowed port; port forward from the uplink and its
  reflection from the LAN; DHCP (static lease), DNS over UDP and TCP including upstream lookups,
  NTP: all from inside the VRF. Talos leaves routald's routing rules alone.
- Not tested: IPv6 in a VRF, RA / mDNS / UPnP in a VRF, BFD, several VRFs with overlapping
  addresses (the per-address rules assume the router's addresses are unique across VRFs).

## Code review (2026-11-14)
Fixed after a full read of the code:
- No rules and a config that cannot be applied (after a boot): routald now loads a table that drops
  all forwarding, and applies the ruleset before the forwarding sysctls. Rules of an earlier config
  are kept as before.
- Port forwards only translate what is addressed to the router on the link (`fib daddr . iif type
  local`): an IPv6 forward no longer captures the same port of every host behind the router. Their
  Talos rule is limited to the target's family, and the forwarded port is dropped on the router
  itself unless a rule opens it.
- DHCPv6 client: an answer without a server identifier no longer panics; a refused renewal
  (NoBinding, lifetimes 0) or a recreated link (PPPoE session back) solicits again instead of
  retrying until the lease expires; a renewal is never sooner than 30 s (deprecated prefix);
  addresses of links that did not exist yet are retried.
- Unbound without `upstreams` forwards to Talos' resolver on localhost, which Unbound refuses by
  default: `do-not-query-localhost: no`.
- Named sets have `auto-merge` (overlapping subnets or ports in a group made nft reject the ruleset).
- AdBlock cosmetic rules (`example.com##.ad`) were read as blocked domains.
- Validation: `to: self` rules with `action: block` (they were ignored), PPPoE `serviceName`
  (reached a shell), interface names over 15 characters, duplicate static leases, address families
  of DHCP and RA options, RA prefixes with host bits.
- `render-services` writes configs 0600 (they may hold secrets).

Known and left as is:
- `MasqueradeConfig` without `sourceSubnets` also translates IPv6.
- A daemon whose new config is rejected keeps running the old one, but is not restarted with it if
  it then exits.
- The delegated prefix is not reloaded from its state file after a routald restart; a prefix that
  changes meanwhile leaves the old addresses until they expire.

## DNS blocking (2026-11-14)
`DNSServerConfig.blocking`: lists by URL (hosts, domain lists, simple AdBlock `||name^` rules),
inline domains, an allow list, NXDOMAIN or 0.0.0.0 answers (`response: zero`; not called
`null`, which YAML reads as "nothing"). routald downloads the lists in the
background (cached in the state directory, a failed refresh keeps the previous copy, Talos' host
DNS when the container has no resolv.conf) and pushes `local_zones` through unbound-control, so a
refresh does not flush the cache. Subdomains of blocked names are folded into their parent's zone;
an allowed name inside a blocked zone becomes a transparent zone.

## Test coverage
`make cover` merges the unit tests' coverage with a routald built with `-cover` running the whole
`make integration` scenario: 96.8% of statements (2026-11-14).

| Package | Coverage |
|---|---|
| pkg/blocklist, pkg/nft | 100% |
| pkg/config | 99.8% |
| pkg/services | 99.7% |
| pkg/pd | 98.6% |
| pkg/talospatch | 97.3% |
| pkg/leases, pkg/talosrender | 96% |
| cmd/routald | 91.0% |
| pkg/env | 90.0% |

Every validation error has an invalid variant (test/variants/invalid, 93 files). What is left:
- the netlink side of the VRF support (the lab's kernel has no VRF; its logic is unit-tested
  against a fake kernel, the real thing was checked on a Talos VM);
- DHCPv6 client paths the lab's ISP does not produce: a refused renewal (Kea hands out a new prefix
  instead), a lease that expires, a WAN link that disappears, kernel errors when adding addresses;
- errors that cannot happen (YAML encoders writing to memory) or need a broken file system.

## Fuzzing (2026-11-17)
`make fuzz` runs five Go fuzz targets in the daemons image:

| Target | Input | What must hold |
|---|---|---|
| blocklist `FuzzParse` | a downloaded list | every name is normalized and cannot break out of an Unbound line |
| pd `FuzzParse` | a DHCPv6 answer from the ISP | an accepted lease has a usable prefix, T1 ≥ 30 s, T2 ≥ T1, and its Renew parses |
| leases `FuzzKea` | Kea's lease file | reading ends; hostnames are plain DNS labels |
| variants `FuzzConfig` | the bytes of router.yaml | loading never panics |
| variants `FuzzField` | one value replaced in a valid config | see below |

For a config that validation accepts, both config targets check it against the JSON schema, render
everything twice (same output), parse the Kea configs as JSON, generate the Talos patch, and hand
the ruleset to `nft -c` and the daemon configs to `unbound-checkconf`, `radvd -c` and `chronyd -p`. A config that is accepted and
that a tool refuses is a hole in validation. `FuzzField` is the one that finds them: random bytes
rarely get past the YAML parser, so it keeps the document valid and replaces one value.

Found and fixed, each with an invalid variant or a unit test:
- a link name of more than 15 characters, or starting with a digit: nft and radvd refuse it. Links
  and aliases are now a letter first, at most 15 characters;
- `""` in a list of addresses decodes to the zero address without an error (`forward-addr: invalid IP`
  in unbound.conf): rejected in every address and prefix list;
- an address with a zone (`fe80::1%eth0`) in a port forward: rejected everywhere;
- a domain whose last label is a number (`localDomain: 0`): radvd reads it as a number. Host and
  domain names now need a letter in their last label;
- a null entry in a list (`ports: [~]`, or a lone `-`) decodes to nothing: the rule had no ports and
  matched them all. Found by comparing with the JSON schema; any null list entry is now an error;
- a value made of line breaks only came back empty from the strict decoder (it re-encodes each
  document), and an empty value is a default: such values are now re-encoded in quotes.

After these, `FuzzField` ran 8 minutes (230,000 configs) without a finding. Failing inputs are not
kept in testdata (they name a seed by its index); each finding is an invalid variant instead.

Not covered: Kea and inadyn check their config against the system (interfaces, network), too slow
and too stateful for a fuzz loop, so their configs are only checked as JSON or not at all; make
check runs them on the examples and variants. The progress lines of `go test -fuzz` show 0 execs/s
for long stretches on the config targets: the workers only report when they return.

## JSON schema of router.yaml (2026-11-17)
`pkg/schema/router.schema.json` (draft-07) is for editors: completion, documentation on hover and
errors while typing, through yaml-language-server. `routald schema` prints it; the examples point
at it on their first line.

- It is generated, by `make schema`, from the types of pkg/config (reflection) and their comments
  (go/parser): the comment of a field is what the editor shows. Tags add what the type does not
  say: `schema:"link"` (the link name pattern of validation) and `schema:"enum=a|b"`.
- `make test` fails when the committed file is not what the types generate, when the schema and
  the loader do not know the same kinds, or when an example or valid variant does not match.
- routald's validation stays the authority. The schema alone refuses 26 of the 93 invalid
  variants: it cannot see references between documents, values that must agree, or files.
- It is never stricter than routald, and the fuzzer holds it to that: every config routald accepts
  must match the schema. That is why a string also takes a number or a boolean (`username: 12345`
  needs no quotes for routald). Known exceptions, where the YAML decoder is lax and the schema is
  not: YAML 1.1 booleans (`yes`, `on`, `y`...) where the schema wants `true` or `false`, a float
  for an integer (`mtu: 1492.5` is truncated), and `""` for an optional value.
- Not tested in an editor: the schema is checked with a Go validator (santhosh-tekuri/jsonschema,
  a test-only dependency), not with yaml-language-server itself.

## Hardening (2026-11-17)
| Topic | What routald does |
|---|---|
| Daemon accounts | The supervisor starts each daemon as its own account, with ambient capabilities and no supplementary group: kea-dhcp4 `NET_RAW` + `NET_BIND_SERVICE`, kea-dhcp6, Unbound and chrony `NET_BIND_SERVICE`, radvd `NET_RAW`, miniupnpd `NET_ADMIN` + `NET_RAW` (it writes nftables), Avahi and inadyn none. pppd stays root (it creates the interface and the default route), and so does routald. A daemon whose account is missing is not started. |
| Files | `/var/lib/routal` and `/run/routal` belong to root. A daemon writes in `<dir>/<account>` only (mode 0750); its config is `root:<group>` 0640. A daemon taken over cannot plant a file where routald writes as root. Leases move to `kea/`: the first start after an upgrade hands out new leases. |
| WAN closed by default | In `inet routal`, input from the WAN links (`Config.WANLinks`) is dropped after the `to: self` accepts (which match `sourceSubnets`/`sourceGroups` too) and a fixed list: ICMP errors, ping at 10/s, the ICMPv6 of RFC 4890 (ND, RA, MLD, errors), DHCP (67 to 68) and DHCPv6 (fe80::/10 to 546) answers. Talos' own rules match source addresses only: this holds when talos.yaml has no `NetworkDefaultActionConfig` or a rule that is too wide, and covers the Talos API (50000-50001). A router with no WAN link (no masquerade, PPPoE, prefix delegation or UPnP) gets no such drop. |
| Directories on Talos | Talos creates the extension's mount sources (`/var/lib/routal`, and the container's `/run`) with mode 0700 root. routald opens them (0755, search bit on the ancestors) so that the daemons' accounts reach their config and their own directory, and creates Avahi's compiled-in `/run/avahi-daemon` for its account. Found on a VM on 2026-11-27: every daemon failed with "Permission denied"; the lab now starts routald with the same modes. |
| UPnP range | Closed on every link, see above. |
| DHCP names | `wpad`, `isatap` and `localhost` are never published from a lease: a device named wpad would be the proxy of every browser with proxy auto-discovery. Static leases and local records can use them. |
| DNSSEC | `dnssec: true`: Unbound's validator with the root keys of the image (`dnssec-root`, both KSK-2017 and KSK-2024). Off by default: an upstream that drops DNSSEC records (many ISP boxes; not checked for Talos' resolver, the default upstream) makes every answer fail. |
| DNS over TLS | `tlsUpstreams` (address, certificate name, port 853): `forward-tls-upstream` with the image's CA bundle. Not mixed with `upstreams`. Unbound cannot forward over HTTPS; DoH would need another forwarder (e.g. dnsproxy or cloudflared) in front. |

Checked: the lab (accounts and capabilities read from /proc, the three listeners) and, by hand,
the rendered Unbound config against Quad9 and Cloudflare (a signed name has the `ad` flag,
dnssec-failed.org is SERVFAIL). Not checked on a Talos VM yet: the accounts rely on the extension
container having all capabilities, as it does for nftables today.

## Verified (2026-11-27, tart VM, arm64, Talos v1.15.0-beta.0 and v1.14.7)
- One NIC as the WAN, two dummy links as LANs, `ingress: block`. Kea, Unbound (DNSSEC, DNS over TLS),
  radvd, chrony, Avahi and miniupnpd run under their accounts with the expected `CapEff`
  (e.g. Unbound 0x400, Avahi 0); routald stays root.
- From the WAN side: ping answers, tcp 50000 opens (a `to: self` rule with `sourceSubnets`), 53 and
  an unused port do not.
- v1.14.7 behaves the same, with the manifest's `>= v1.15.0-alpha.0` constraint lifted for the test:
  the constraint is what keeps 1.14 out, not a missing feature this test exercises.
- The VM disk needs about 6 GB: with 3 GB, EPHEMERAL has no room and `/var/lib` never appears.
- Not checked there: DNS answers and DHCP leases (no client behind a dummy link).
- Scripted since as `make vm-test` (hack/vm-test.sh, test/vm): 13 checks.

## Talos kernel (v1.15.0-beta.0), checked on the VM
| Feature | Kernel | Consequence |
|---|---|---|
| PPPoE | `CONFIG_PPP=y`, `CONFIG_PPP_ASYNC=y`, `CONFIG_PPPOE` not set | user-space PPPoE (rp-pppoe through pppd's pty) works from the extension; kernel PPPoE needs the option upstream |
| Flowtables offload | `CONFIG_NF_FLOW_TABLE` not set | not possible on stock Talos |
| cake | `CONFIG_NET_SCH_CAKE` not set | SQM possible with HTB + fq_codel (both built in, `ifb` as a module) |
| Own modules | `module.sig_enforce=1` | a module extension must be signed with the Talos kernel key: kernel options must come from siderolabs/pkgs |

## Next
1. Kernel options upstream (siderolabs/pkgs, as modules): `PPPOE`, `NF_FLOW_TABLE(_INET)` + `NFT_FLOW_OFFLOAD`, `NET_SCH_CAKE`.
2. WAN: prefix delegation over PPPoE in the lab;
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
9. BGP/VRF: IPv6 and the other services in a VRF, BFD, a containerlab or QEMU test (the Docker
   Desktop kernel has no VRF, so `make integration` cannot cover it).
10. VRRP for router HA; `routalctl` wrapper (validate, diff, apply) if `talosctl` + `routald` aren't enough.
