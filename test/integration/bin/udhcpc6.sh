#!/bin/sh
# udhcpc6 hook: record what the DHCPv6 server answered (stateless: options only).
ns=$(ip netns identify $$ 2>/dev/null)
[ -n "$ns" ] || ns=$(hostname)
case $1 in
bound | renew) echo "dns=${dns:-} domain=${search:-}" > "/tmp/dhcp6-$interface-$ns" ;;
esac
exit 0
