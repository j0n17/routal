#!/bin/sh
# Data-plane tests of routald in network namespaces, without Talos. Runs examples/nas-vpn (plus a
# port forward and UPnP) with the real nftables, Kea, Unbound, radvd, chrony, Avahi and miniupnpd.
#
#   remote (VPN peer) ── isp (WAN host, upstream DNS) ── router ── br0 ─┬─ laptop (static lease)
#   198.51.100.2         203.0.113.1                    eth0=wan0     ├─ phone  (dynamic lease, UPnP)
#                                                        eth1=lan0     └─ nas    (VLAN 30 = srv0)
#                                                        srv0, wg0
#
# Talos' own pieces are emulated: links, addresses, routes and WireGuard are set up here, and link
# aliases are kernel ifalias values like LinkAliasConfig sets them. Talos' ingress firewall is not.
set -u
T=/test
W=/tmp/it
mkdir -p $W
fails=0
passes=0

nsx() { ns=$1; shift; ip netns exec "$ns" "$@"; }
pass() { passes=$((passes + 1)); echo "  ok    $*"; }
fail() { fails=$((fails + 1)); echo "  FAIL  $*"; }
# expect <ok|blocked> <description> <ns> <cmd...>: the command prints the reply, empty when blocked
expect() {
	want=$1 desc=$2; shift 2
	out=$(nsx "$@" 2>/dev/null | grep -v '^;')
	if [ "$want" = ok ] && [ -n "$out" ]; then pass "$desc -> $out"
	elif [ "$want" = blocked ] && [ -z "$out" ]; then pass "$desc -> blocked"
	else fail "$desc (wanted $want, got '${out}')"; fi
}
export PATH=$T/bin:$PATH # tcp: runs inside `ip netns exec`, so it is a script, not a function
wait_for() { # wait_for <seconds> <cmd...>
	end=$(($(date +%s) + $1)); shift
	while [ "$(date +%s)" -lt "$end" ]; do "$@" >/dev/null 2>&1 && return 0; sleep 1; done
	return 1
}
serve() { # serve <ns> <port> <name>: answers "<name> <peer address>", IPv4 and IPv6
	nsx "$1" socat TCP4-LISTEN:"$2",fork,reuseaddr SYSTEM:"echo $3 \$SOCAT_PEERADDR" &
	nsx "$1" socat TCP6-LISTEN:"$2",fork,reuseaddr,ipv6only=1 SYSTEM:"echo $3 \$SOCAT_PEERADDR" &
}

echo "== topology"
for ns in isp remote router laptop phone nas; do ip netns add $ns; nsx $ns ip link set lo up; done
ip link add br0 type bridge && ip link set br0 up
link() { # link <ns> <ifname> <bridge-port|peer-ns:ifname> [mac]
	ip link add tmp0 type veth peer name tmp1
	ip link set tmp0 netns "$1" && nsx "$1" ip link set tmp0 name "$2"
	[ -n "${4:-}" ] && nsx "$1" ip link set "$2" address "$4"
	case $3 in
	*:*) ip link set tmp1 netns "${3%%:*}" && nsx "${3%%:*}" ip link set tmp1 name "${3#*:}" && nsx "${3%%:*}" ip link set "${3#*:}" up ;;
	*) ip link set tmp1 name "$3" && ip link set "$3" master br0 up ;;
	esac
	nsx "$1" ip link set "$2" up
}
link router eth0 isp:eth-r
link isp eth-v remote:eth0
link router eth1 p-router
link laptop eth0 p-laptop 52:54:00:00:00:20
link phone eth0 p-phone 52:54:00:00:00:30
link nas eth0 p-nas 52:54:00:00:00:10
# the NAS is on VLAN 30 only (an access port in real life): no IPv6 on the untagged LAN
nsx nas sysctl -qw net.ipv6.conf.eth0.disable_ipv6=1

# isp: the Internet side; routes to the router's inside networks so that WAN attacks get to it
nsx isp sysctl -qw net.ipv4.ip_forward=1 net.ipv6.conf.all.forwarding=1
nsx isp ip addr add 203.0.113.1/24 dev eth-r
nsx isp ip addr add 2001:db8:ff::1/64 dev eth-r
nsx isp ip addr add 198.51.100.1/24 dev eth-v
nsx isp ip route add 192.168.30.0/24 via 203.0.113.2
nsx isp ip -6 route add fd00:30::/64 via 2001:db8:ff::2
nsx remote ip addr add 198.51.100.2/24 dev eth0
nsx remote ip route add default via 198.51.100.1

# router: what Talos would configure (LinkAliasConfig = ifalias, addresses, VLAN, WireGuard, routes)
nsx router ip link set eth0 alias wan0
nsx router ip link set eth1 alias lan0
nsx router ip addr add 203.0.113.2/24 dev eth0
nsx router ip addr add 2001:db8:ff::2/64 dev eth0
nsx router ip route add default via 203.0.113.1
nsx router ip -6 route add default via 2001:db8:ff::1
nsx router ip addr add 192.168.1.1/24 dev eth1
nsx router ip addr add fd00:1::1/64 dev eth1
nsx router ip link add link eth1 name srv0 type vlan id 30
nsx router ip link set srv0 up
nsx router ip addr add 192.168.30.1/24 dev srv0
nsx router ip addr add fd00:30::1/64 dev srv0
nsx nas ip link add link eth0 name eth0.30 type vlan id 30
nsx nas ip link set eth0.30 up

wg genkey > $W/r.key; wg pubkey < $W/r.key > $W/r.pub
wg genkey > $W/v.key; wg pubkey < $W/v.key > $W/v.pub
nsx router ip link add wg0 type wireguard
nsx router wg set wg0 private-key $W/r.key listen-port 51820 peer "$(cat $W/v.pub)" allowed-ips 10.8.0.2/32,fd00:8::2/128
nsx router ip addr add 10.8.0.1/24 dev wg0
nsx router ip addr add fd00:8::1/64 dev wg0
nsx router ip link set wg0 up
nsx remote ip link add wg0 type wireguard
nsx remote wg set wg0 private-key $W/v.key peer "$(cat $W/r.pub)" endpoint 203.0.113.2:51820 \
	allowed-ips 10.8.0.0/24,192.168.30.0/24,fd00:8::/64,fd00:30::/64 persistent-keepalive 5
nsx remote ip addr add 10.8.0.2/24 dev wg0
nsx remote ip addr add fd00:8::2/64 dev wg0
nsx remote ip link set wg0 up
nsx remote ip route add 192.168.30.0/24 dev wg0
nsx remote ip -6 route add fd00:30::/64 dev wg0

# upstream DNS on the isp
cat > $W/isp-unbound.conf <<EOF
server:
	interface: 203.0.113.1
	access-control: 0.0.0.0/0 allow
	username: ""
	chroot: ""
	pidfile: "$W/isp-unbound.pid"
	do-daemonize: no
	module-config: "iterator"
	local-zone: "example.com." static
	local-data: "example.com. A 198.51.100.10"
EOF
nsx isp unbound -c $W/isp-unbound.conf &

echo "== router config: examples/nas-vpn + test overlay"
sed 's/upstreams: \[9.9.9.9, 2620:fe::fe\]/upstreams: [203.0.113.1]/' /test/nas-vpn.yaml > $W/router.yaml
cat $T/overlay.yaml >> $W/router.yaml
routald validate $W/router.yaml || exit 1

# the extension's /run is a tmpfs: give routald a private one (pid files, sockets)
nsx router unshare -m sh -c "mount -t tmpfs tmpfs /run && exec routald run -config $W/router.yaml -state-dir $W/state" > $W/routald.log 2>&1 &

wait_for 20 nsx router nft list table inet routal || { cat $W/routald.log; exit 1; }
wait_for 20 test -S $W/state/../state/unbound.conf -o -f $W/state/unbound.conf

serve isp 80 isp
serve nas 443 nas
serve nas 8080 nas-admin
serve laptop 8080 laptop
serve phone 9000 phone

echo "== DHCP (Kea)"
dhcp() { # dhcp <ns> <ifname> [hostname]
	nsx "$1" udhcpc -i "$2" -q -n -t 3 -T 1 ${3:+-x hostname:$3} -s $T/udhcpc.sh >/dev/null 2>&1
}
wait_for 30 dhcp laptop eth0 laptop && pass "laptop lease: $(cat /tmp/dhcp-eth0-laptop 2>/dev/null)" || fail "laptop got no lease"
[ "$(cut -d' ' -f1 /tmp/dhcp-eth0-laptop 2>/dev/null)" = 192.168.1.50 ] && pass "laptop static lease 192.168.1.50" || fail "laptop static lease"
grep -q "router=192.168.1.1 dns=192.168.1.1 domain=home.arpa ntp=192.168.1.1" /tmp/dhcp-eth0-laptop && pass "options: router, DNS, domain, NTP" || fail "DHCP options: $(cat /tmp/dhcp-eth0-laptop)"
wait_for 30 dhcp phone eth0 phone && pass "phone lease: $(cat /tmp/dhcp-eth0-phone)" || fail "phone got no lease"
case $(cut -d' ' -f1 /tmp/dhcp-eth0-phone 2>/dev/null) in 192.168.1.1[0-9][0-9] | 192.168.1.200) pass "phone in the pool" ;; *) fail "phone outside the pool" ;; esac
wait_for 30 dhcp nas eth0.30 && grep -q "^192.168.30.10 " /tmp/dhcp-eth0.30-nas && pass "NAS static lease on VLAN 30: $(cat /tmp/dhcp-eth0.30-nas)" || fail "NAS lease: $(cat /tmp/dhcp-eth0.30-nas 2>/dev/null)"
nsx nas ip addr add fd00:30::10/64 dev eth0.30 # the NAS's static IPv6 (the address group nas)
PHONE=$(cut -d' ' -f1 /tmp/dhcp-eth0-phone)

echo "== router advertisements (radvd)"
slaac() { nsx nas ip -6 addr show dev eth0.30 | grep -q "fd00:30::5054:ff:fe00:10/64" && nsx nas ip -6 route | grep -q "default via fe80"; }
wait_for 30 slaac && pass "NAS SLAAC address and IPv6 default route" || fail "NAS SLAAC: $(nsx nas ip -6 addr show dev eth0.30 | grep inet6)"
wait_for 30 sh -c "ip netns exec laptop ip -6 addr show dev eth0 | grep -q fd00:1::" && pass "laptop SLAAC address" || fail "laptop SLAAC"

wait_for 15 nsx remote ping -c1 -W1 10.8.0.1

echo "== DNS (Unbound)"
q() { nsx "$1" dig +short +time=2 +tries=1 @"$2" "$3" ${4:-}; }
D="dig +short +time=2 +tries=1"
[ "$(q laptop 192.168.1.1 nas.home.arpa)" = 192.168.30.10 ] && pass "nas.home.arpa = 192.168.30.10" || fail "nas.home.arpa"
wait_for 15 sh -c "[ \"\$(ip netns exec laptop dig +short @192.168.1.1 phone.home.arpa)\" = $PHONE ]" && pass "phone.home.arpa = $PHONE (dynamic lease name)" || fail "phone.home.arpa: $(q laptop 192.168.1.1 phone.home.arpa)"
[ "$(q laptop 192.168.1.1 -x 192.168.30.10)" = nas.home.arpa. ] && pass "reverse 192.168.30.10 = nas.home.arpa." || fail "reverse lookup"
[ "$(q laptop 192.168.1.1 example.com)" = 198.51.100.10 ] && pass "forwarded to the upstream: example.com" || fail "upstream forwarding"
expect ok "LAN over IPv6: laptop.home.arpa" laptop $D @fd00:1::1 laptop.home.arpa
expect ok "servers VLAN: laptop.home.arpa" nas $D @192.168.30.1 laptop.home.arpa
expect ok "VPN: nas.home.arpa through the tunnel" remote $D @10.8.0.1 nas.home.arpa
expect blocked "WAN: no DNS on the router's public address" isp $D @203.0.113.2 nas.home.arpa

echo "== firewall: the NAS"
expect ok "LAN -> NAS:443 v4" laptop tcp 192.168.30.10 443
expect ok "LAN -> NAS:443 v6" laptop tcp '[fd00:30::10]' 443 TCP6
expect blocked "LAN -> NAS:8080 v4" laptop tcp 192.168.30.10 8080
expect blocked "LAN -> NAS:8080 v6" laptop tcp '[fd00:30::10]' 8080 TCP6
expect blocked "WAN -> NAS:443 v4" isp tcp 192.168.30.10 443
expect blocked "WAN -> NAS:443 v6" isp tcp '[fd00:30::10]' 443 TCP6
wait_for 15 sh -c "ip netns exec remote ping -c1 -W1 10.8.0.1" && pass "WireGuard tunnel up" || fail "WireGuard handshake"
expect ok "VPN -> NAS:443 v4" remote tcp 192.168.30.10 443
expect ok "VPN -> NAS:443 v6" remote tcp '[fd00:30::10]' 443 TCP6
expect blocked "VPN -> NAS:8080" remote tcp 192.168.30.10 8080
nsx router dmesg 2>/dev/null | grep -q "routal wan-to-servers" && pass "WAN attempts logged" || echo "  skip  kernel log not readable here"

echo "== NAT"
expect ok "masquerade: LAN -> Internet seen as the router" laptop tcp 203.0.113.1 80
nsx laptop sh -c 'socat -T2 - TCP:203.0.113.1:80 </dev/null' | grep -q "203.0.113.2" && pass "source address is 203.0.113.2" || fail "masquerade source"
expect ok "port forward: WAN -> router:8080 -> laptop" isp tcp 203.0.113.2 8080
expect ok "reflection: NAS -> router public 203.0.113.2:8080 -> laptop" nas tcp 203.0.113.2 8080
nsx nas sh -c 'socat -T2 - TCP:203.0.113.2:8080 </dev/null' | grep -q "laptop 192.168.1.1" && pass "reflected connection comes from the router (answers return through it)" || fail "reflection source"

echo "== UPnP (miniupnpd)"
expect blocked "before mapping: WAN -> router:9000" isp tcp 203.0.113.2 9000
nsx phone timeout 15 upnpc -m eth0 -a "$PHONE" 9000 9000 TCP > $W/upnpc.log 2>&1 && pass "phone mapped 9000 with UPnP IGD" || fail "upnpc: $(tail -3 $W/upnpc.log)"
expect ok "after mapping: WAN -> router:9000 -> phone" isp tcp 203.0.113.2 9000
nsx phone timeout 15 upnpc -m eth0 -a 192.168.1.50 9001 8080 TCP > $W/upnpc2.log 2>&1 && fail "phone mapped a port to another device" || pass "secure mode: phone cannot map to the laptop"

echo "== NTP (chrony)"
nsx laptop chronyd -Q -t 20 "server 192.168.1.1 iburst maxsamples 1" > $W/ntp.log 2>&1 && pass "laptop got time from the router: $(grep -i 'offset' $W/ntp.log | head -1)" || fail "NTP: $(tail -2 $W/ntp.log)"

echo "== mDNS reflector (Avahi)"
# a service announced on the servers VLAN must be answered on the LAN (legacy unicast query)
mkdir -p $W/nas-avahi/services
cat > $W/nas-avahi/avahi-daemon.conf <<EOF
[server]
allow-interfaces=eth0.30
enable-dbus=no
host-name=nas
[publish]
publish-addresses=yes
EOF
cat > $W/nas-avahi/services/smb.service <<EOF
<?xml version="1.0" standalone='no'?><!DOCTYPE service-group SYSTEM "avahi-service.dtd">
<service-group><name>NAS</name><service><type>_smb._tcp</type><port>445</port></service></service-group>
EOF
nsx nas unshare -m sh -c "mount -t tmpfs tmpfs /run && mount --bind $W/nas-avahi/services /etc/avahi/services && exec avahi-daemon -f $W/nas-avahi/avahi-daemon.conf --no-drop-root --no-chroot --no-rlimits" > $W/nas-avahi.log 2>&1 &
mdns() { nsx laptop mdns-a | grep -q "c0 a8 1e 0a"; } # 192.168.30.10 in the answer
wait_for 20 mdns && pass "laptop resolves nas.local (announced on VLAN 30) through the reflector" || fail "mDNS reflection: $(nsx laptop mdns-a)"

echo
echo "== $passes passed, $fails failed"
if [ "$fails" -gt 0 ]; then echo "-- routald log:"; tail -40 $W/routald.log; fi
[ -n "${KEEP:-}" ] && { echo "KEEP set: sleeping for debugging"; sleep 3600; }
exit $((fails > 0))
