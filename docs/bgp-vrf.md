# BGP and VRFs

Talos does both itself, in `talos.yaml`:

- `BGPInstanceConfig`: sessions, announced links, learned routes installed in the kernel;
- `VRFConfig`: a routing table of its own for a set of links.

routald does the rest, with nothing to declare in `router.yaml`:

- a `to: self` rule for tcp/179 lets the neighbor in; zone rules decide what transits;
- links in a VRF keep their names in zones, NAT and port forwards (in the firewall hooks the
  kernel reports the VRF as the input interface; routald matches the link behind it);
- DHCP, DNS and NTP serve links in a VRF: routald adds a routing rule for each of the router's
  addresses there and sets the `l3mdev_accept` and `ip_nonlocal_bind` sysctls. The daemons' own
  traffic (DNS upstreams, NTP sources) uses the default routing domain.

Checked on a Talos VM against a bird peer (IPv4); see [`examples/bgp-vrf/`](../examples/bgp-vrf/).
