# Security

## Closed by default

The links to the Internet are closed by default for traffic to the router itself, whatever
`talos.yaml` opens, the Talos API (tcp 50000-50001) included. Those links are the masquerade
`outLink`, PPPoE sessions, prefix delegation links and the UPnP external link.

What gets in:

- answers, ICMP errors, a rate-limited ping;
- IPv6 neighbor discovery, router advertisements and MLD;
- DHCP and DHCPv6 answers;
- what a `to: self` rule from that zone opens (WireGuard, BGP, or the Talos API with
  `sourceSubnets`, which this table now matches too).

With UPnP, the mapping range is closed on every link for whatever has no mapping.

## Daemons

Every daemon but pppd runs under its own account (`kea`, `unbound`, `radvd`, `chrony`, `avahi`,
`miniupnpd`, `inadyn`) with the one or two capabilities it needs (e.g. `CAP_NET_BIND_SERVICE`), and
only writes in its own directory. routald itself and pppd stay root.

## DNS

- A DHCP client cannot register the names `wpad`, `isatap` or `localhost`.
- `dnssec: true` makes Unbound refuse forged answers.
- `tlsUpstreams` encrypts the queries to the upstream resolvers (DNS over TLS; Unbound cannot
  forward over HTTPS).
