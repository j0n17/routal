#!/bin/sh
# udhcpc hook: configure the address and default route, record the options (never touches /etc/resolv.conf).
ns=$(ip netns identify $$)
case $1 in
bound|renew)
	ip addr flush dev "$interface" scope global 2>/dev/null
	ip addr add "$ip/${mask:-24}" dev "$interface"
	[ -n "${router:-}" ] && ip route replace default via "${router%% *}" dev "$interface"
	echo "$ip router=${router:-} dns=${dns:-} domain=${domain:-} ntp=${ntpsrv:-}" > "/tmp/dhcp-$interface-$ns"
	;;
esac
exit 0
