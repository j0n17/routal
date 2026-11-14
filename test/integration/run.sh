#!/bin/sh
# Data-plane tests of routald in network namespaces, without Talos (make integration). Runs
# examples/nas-vpn (plus a port forward and UPnP) with the real nftables, Kea, Unbound, radvd,
# chrony, Avahi and miniupnpd. The same checks run on containerlab: test/clab/run.sh.
#
#   remote (VPN peer) ── isp (WAN host, upstream DNS) ── router ── br0 ─┬─ laptop (static lease)
#   198.51.100.2         203.0.113.1                    eth0=wan0     ├─ phone  (dynamic lease, UPnP)
#                                                        eth1=lan0     └─ nas    (VLAN 30 = srv0)
#                                                        srv0, wg0
#
# Talos' own pieces are emulated by checks.sh: addresses, routes, VLAN, WireGuard, and link aliases
# as kernel ifalias values like LinkAliasConfig sets them. Its ingress firewall is not.
set -u

nsx() { ns=$1; shift; ip netns exec "$ns" "$@"; }
# every background process gets a private /run and mounts, like a container
bg() { ip netns exec "$1" unshare -m sh -c "mount -t tmpfs tmpfs /run && $2" >/dev/null 2>&1 & }
put() { ip netns exec "$1" sh -c "cat > $2"; }

R_WAN=eth0 R_LAN=eth1 ISP_R=eth-r ISP_V=eth-v REMOTE_IF=eth0 CL_IF=eth0

echo "== topology (network namespaces)"
for ns in isp remote router laptop phone nas; do ip netns add $ns; nsx $ns ip link set lo up; done
ip link add br0 type bridge && ip link set br0 up
link() { # link <ns> <ifname> <bridge-port|peer-ns:ifname>
	ip link add tmp0 type veth peer name tmp1
	ip link set tmp0 netns "$1" && nsx "$1" ip link set tmp0 name "$2"
	case $3 in
	*:*) ip link set tmp1 netns "${3%%:*}" && nsx "${3%%:*}" ip link set tmp1 name "${3#*:}" && nsx "${3%%:*}" ip link set "${3#*:}" up ;;
	*) ip link set tmp1 name "$3" && ip link set "$3" master br0 up ;;
	esac
	nsx "$1" ip link set "$2" up
}
link router eth0 isp:eth-r
link isp eth-v remote:eth0
link router eth1 p-router
link laptop eth0 p-laptop
link phone eth0 p-phone
link nas eth0 p-nas

. /test/checks.sh

[ -n "${KEEP:-}" ] && { echo "KEEP set: sleeping for debugging"; sleep 3600; }
exit $((fails > 0))
