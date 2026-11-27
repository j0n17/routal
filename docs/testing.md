# Tests

## Commands

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
make vm-test       # a real Talos VM with the extension, then the personas
make vm            # the same VM without the personas, left running (make vm-apply, make vm-stop)
```

`test/variants/` holds router configs that exercise the options the examples do not, and invalid
ones with the error they must produce (`# error:` on the first line).

## Data plane: `make integration` and `make clab`

Both run the real routald and daemons with `examples/nas-vpn` and the same 91 checks
(`test/integration/checks.sh`):

- DHCP, SLAAC, DNS, DNS blocking;
- the firewall in IPv4 and IPv6, NAT, reflection, UPnP, NTP, mDNS;
- the hardening (daemon accounts, UPnP range, WAN closed by default);
- options applied on the running router: SNAT to a fixed address, an IPv6 port forward with
  `toPort`, `forwardDefault: accept`, blocking with 0.0.0.0 answers, DHCPv6 options, IPv6
  forwarding off;
- the WAN against a simulated ISP: IPv6 prefix delegation (Kea) with renewal, an ISP that is down
  at start, goes down during the lease and comes back with another prefix, dynamic DNS, and a
  PPPoE session (pppoe-server).

`make integration` needs only Docker (one privileged container, network namespaces).

`make clab` runs containerlab in its own container, on Linux or macOS with Docker Desktop, one
container per node. `KEEP=1 make clab` leaves the lab up to poke at it
(`docker exec -it clab-routal-router sh`).

## Real Talos: `make vm-test`

Boots Talos with the extension in a VM (tart on an Apple silicon Mac; QEMU with KVM and sudo on
Linux, which is what CI runs for every version), applies `test/vm`, and checks what only a real
Talos shows:

- the extension starts;
- every daemon runs under its account with its capabilities;
- the WAN side is closed.

On Linux the VM has a second NIC with a client behind it: DHCP lease, SLAAC, DNS over TLS with
DNSSEC, and the Internet through the router. It removes the VM and the image when it ends.

`make vm-upgrade-test` boots an older build and upgrades it to this one.

Talos itself (boot, upgrades, its ingress firewall) was also tested in UTM VMs, see
[DESIGN.md](../DESIGN.md).

### Personas

The VM test then plays personas (`test/vm/personas`): each is a complete router, `talos.yaml` and
`router.yaml`, for one kind of user, applied as a whole, with what that user expects to work and
not to work. Where the same thing can be done in several ways, each persona uses the one that suits
its user (the [web page](https://j0n17.github.io/routal/#ways) has the table).

| Persona | Interfaces named by | Expects |
|---|---|---|
| `01-family` | the kernel's names | Internet and DNS for every device, ad domains blocked by name, nothing reachable from outside |
| `02-family-parental` | the kernel's names | a downloaded blocklist; the children's devices (an address group) cannot use another resolver |
| `03-gamer` | the kernel's names | a game opens its port with UPnP; it cannot open one for another device |
| `04-homelab` | MAC address | a NAS and a web server published on their ports; the whole LAN over WireGuard |
| `05-guests` | MAC address | visitors on their own VLAN get the Internet, and reach neither the house nor the router's API |
| `06-hotel` | NIC driver, numbered | guests get tcp 80 and 443 through the hotel's DNS and nothing else |
| `07-company` | PCI slot | staff and servers apart, rules with named groups, refusals logged, DNS over TLS with DNSSEC |

`make check-personas` (part of `make check`) validates all of them without a VM.

### Advanced (`ADVANCED=1`)

On the newest Talos version CI goes further:

- after each persona, every TCP port is scanned from outside over IPv4 and IPv6;
- the homelab goes through a reboot and a WAN outage;
- `test/vm/advanced.sh` boots the router with no Internet, after a power cut with damaged files,
  with a full disk and with its clock in the year 2000;
- it runs `bin/routal-apply` with a good config and with one that locks the admin out;
- it exhausts the DHCP pool and the connection table.

## CI

GitHub Actions (`.github/workflows/ci.yml`), on every push and pull request:

- `make check`, `make integration`, `make fuzz FUZZTIME=30s`;
- builds the extension for amd64 and arm64.

On every push and every Monday, for every Talos version listed by `hack/talos-versions.sh` (the
last three releases of each supported line, today 1.14 and 1.15): validates the examples, builds
the installer and boots it. For one version locally:

```sh
make check-talos installer TALOS_VERSION=v1.14.7
```
