# Routal design

## Decisions
| Topic | Decision |
|---|---|
| Base | **Stock Talos Linux** (>= 1.15, Kubernetes-free mode `--skip-k8s-etcd`), not a fork. The fork (kept locally, unpublished) was dropped on 2026-11-13: same result for a router, without carrying 2000 files of merge conflicts. |
| Router logic | `routald`, a Talos **system extension** (baked into the image, starts at boot without network, versioned with the OS and rolled back with it). |
| Plugins | Talos `ContainerConfig` declarative containers (UI, adblock, monitoring...), pulled at runtime. |
| Config | Talos documents for the machine + Routal documents (`router.yaml`), delivered through `ExtensionServiceConfig`. |
| Safety | Talos `apply-config --mode try --timeout` (auto-rollback) + A/B OS slots. |
| Data plane | Kernel forwarding + nftables. routald owns the `inet routal` table; Talos owns `inet talos`. |
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
- Link aliases (`LinkAliasConfig`) are kernel `ifalias` values, not names; routald resolves them from
  `/sys/class/net/*/ifalias` and re-applies when they change (5 s reconcile loop).
- routald leaves its rules in place when it stops, so a restart never opens the firewall.

## Verified (2026-11-13, Talos v1.15.0-beta.0, arm64 tart VM)
- Extension loads; the service writes the forwarding sysctls (needs `readonlyPaths` without `/proc/sys`).
- nft accepts the rendered ruleset alongside `inet talos`; Talos API still reachable with ingress `block`.
- `patch mc --mode try`: Talos restarts routald with the new router.yaml, then reverts it after the timeout.

## Next
1. DHCP server + DNS (embed Go libs vs. bundle dnsmasq in the extension) + RA.
2. Own installer image (Image Factory schematic or imager `installer` output) so install/upgrade keeps the extension.
3. GitOps: CI workflow (render + `--mode try` + health check + confirm), then an on-device pull agent.
4. Stale `NetworkRuleConfig` cleanup when a `to: self` rule is removed (patches only add).
5. VRRP for router HA; flowtables offload; `routalctl` wrapper (validate, diff, apply) if `talosctl` + `routald` aren't enough.
