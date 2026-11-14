# Shared by the network namespace runner (test/integration/run.sh) and the containerlab runner
# (test/clab/run.sh): what Talos would configure (addresses, VLAN, WireGuard, link aliases), the
# router config, then the checks. The runner defines:
#   nsx <node> <cmd...>   run a command in a node
#   bg <node> "<shell>"   run a shell command in the background in a node
#   put <node> <file>     write stdin to a file in a node
# and the interface names: R_WAN R_LAN (router), ISP_R ISP_V (isp), REMOTE_IF, CL_IF (clients).
fails=0
passes=0
pass() { passes=$((passes + 1)); echo "  ok    $*"; }
fail() { fails=$((fails + 1)); echo "  FAIL  $*"; }
# expect <ok|blocked> <description> <node> <cmd...>: the command prints the reply, empty when blocked
expect() {
	want=$1 desc=$2; shift 2
	out=$(nsx "$@" 2>/dev/null | grep -v '^;')
	if [ "$want" = ok ] && [ -n "$out" ]; then pass "$desc -> $out"
	elif [ "$want" = blocked ] && [ -z "$out" ]; then pass "$desc -> blocked"
	else fail "$desc (wanted $want, got '${out}')"; fi
}
wait_for() { # wait_for <seconds> <cmd...>
	end=$(($(date +%s) + $1)); shift
	while [ "$(date +%s)" -lt "$end" ]; do "$@" >/dev/null 2>&1 && return 0; sleep 1; done
	return 1
}
serve() { # serve <node> <port> <name>: answers "<name> <peer address>", IPv4 and IPv6
	bg "$1" "socat TCP4-LISTEN:$2,fork,reuseaddr SYSTEM:'echo $3 \$SOCAT_PEERADDR'"
	bg "$1" "socat TCP6-LISTEN:$2,fork,reuseaddr,ipv6only=1 SYSTEM:'echo $3 \$SOCAT_PEERADDR'"
}
q() { nsx "$1" dig +short +time=2 +tries=1 @"$2" "$3" ${4:-}; }
D="dig +short +time=2 +tries=1"
lease() { nsx "$1" cat "/tmp/dhcp-$2-$1" 2>/dev/null; } # lease <node> <ifname>
dhcp() { # dhcp <node> <ifname> [hostname]
	nsx "$1" udhcpc -i "$2" -q -n -t 3 -T 1 ${3:+-x hostname:$3} -s /test/udhcpc.sh >/dev/null 2>&1
}

echo "== addresses, VLAN, WireGuard (what Talos would configure)"
# client MACs: the static leases of examples/nas-vpn
for c in laptop=52:54:00:00:00:20 phone=52:54:00:00:00:30 nas=52:54:00:00:00:10; do
	nsx "${c%%=*}" ip link set "$CL_IF" down
	nsx "${c%%=*}" ip link set "$CL_IF" address "${c#*=}"
	nsx "${c%%=*}" ip link set "$CL_IF" up
done
# the NAS is on VLAN 30 only (an access port in real life): no IPv6 on the untagged LAN
nsx nas sysctl -qw "net.ipv6.conf.$CL_IF.disable_ipv6=1"
nsx nas ip link add link "$CL_IF" name "$CL_IF.30" type vlan id 30
nsx nas ip link set "$CL_IF.30" up

# isp: the Internet side; routes to the router's inside networks so that WAN attacks get to it
nsx isp sysctl -qw net.ipv4.ip_forward=1 net.ipv6.conf.all.forwarding=1
nsx isp ip addr add 203.0.113.1/24 dev "$ISP_R"
nsx isp ip addr add 2001:db8:ff::1/64 dev "$ISP_R"
nsx isp ip addr add 198.51.100.1/24 dev "$ISP_V"
nsx isp ip route add 192.168.30.0/24 via 203.0.113.2
nsx isp ip -6 route add fd00:30::/64 via 2001:db8:ff::2
nsx remote ip addr add 198.51.100.2/24 dev "$REMOTE_IF"
nsx remote ip route add default via 198.51.100.1

# router: LinkAliasConfig = ifalias, addresses, VLAN, routes
nsx router ip link set "$R_WAN" alias wan0
nsx router ip link set "$R_LAN" alias lan0
nsx router ip addr add 203.0.113.2/24 dev "$R_WAN"
nsx router ip addr add 2001:db8:ff::2/64 dev "$R_WAN"
nsx router ip route add default via 203.0.113.1
nsx router ip -6 route add default via 2001:db8:ff::1
nsx router ip addr add 192.168.1.1/24 dev "$R_LAN"
nsx router ip addr add fd00:1::1/64 dev "$R_LAN"
nsx router ip link add link "$R_LAN" name srv0 type vlan id 30
nsx router ip link set srv0 up
nsx router ip addr add 192.168.30.1/24 dev srv0
nsx router ip addr add fd00:30::1/64 dev srv0

# WireGuard between the router and the remote peer
RKEY=$(nsx router wg genkey); RPUB=$(echo "$RKEY" | nsx router wg pubkey)
VKEY=$(nsx remote wg genkey); VPUB=$(echo "$VKEY" | nsx remote wg pubkey)
echo "$RKEY" | put router /tmp/wg-router.key
echo "$VKEY" | put remote /tmp/wg-remote.key
nsx router ip link add wg0 type wireguard
nsx router wg set wg0 private-key /tmp/wg-router.key listen-port 51820 peer "$VPUB" allowed-ips 10.8.0.2/32,fd00:8::2/128
nsx router ip addr add 10.8.0.1/24 dev wg0
nsx router ip addr add fd00:8::1/64 dev wg0
nsx router ip link set wg0 up
nsx remote ip link add wg0 type wireguard
nsx remote wg set wg0 private-key /tmp/wg-remote.key peer "$RPUB" endpoint 203.0.113.2:51820 \
	allowed-ips 10.8.0.0/24,192.168.30.0/24,fd00:8::/64,fd00:30::/64 persistent-keepalive 5
nsx remote ip addr add 10.8.0.2/24 dev wg0
nsx remote ip addr add fd00:8::2/64 dev wg0
nsx remote ip link set wg0 up
nsx remote ip route add 192.168.30.0/24 dev wg0
nsx remote ip -6 route add fd00:30::/64 dev wg0

# upstream DNS on the isp
put isp /tmp/isp-unbound.conf <<CONF
server:
	interface: 203.0.113.1
	access-control: 0.0.0.0/0 allow
	username: ""
	chroot: ""
	pidfile: "/tmp/isp-unbound.pid"
	do-daemonize: no
	module-config: "iterator"
	local-zone: "example.com." static
	local-data: "example.com. A 198.51.100.10"
CONF
bg isp "unbound -c /tmp/isp-unbound.conf > /tmp/isp-unbound.log 2>&1"

echo "== router config: examples/nas-vpn + test overlay"
nsx router sh -c "sed 's/upstreams: \[9.9.9.9, 2620:fe::fe\]/upstreams: [203.0.113.1]/' /test/nas-vpn.yaml > /tmp/router.yaml && cat /test/overlay.yaml >> /tmp/router.yaml"
nsx router routald validate /tmp/router.yaml || exit 1
bg router "routald run -config /tmp/router.yaml > /tmp/routald.log 2>&1"
wait_for 20 nsx router nft list table inet routal || { nsx router cat /tmp/routald.log; exit 1; }
wait_for 20 nsx router test -f /var/lib/routal/unbound.conf

serve isp 80 isp
serve nas 443 nas
serve nas 8080 nas-admin
serve laptop 8080 laptop
serve phone 9000 phone

echo "== DHCP (Kea)"
wait_for 30 dhcp laptop "$CL_IF" laptop && pass "laptop lease: $(lease laptop "$CL_IF")" || fail "laptop got no lease"
[ "$(lease laptop "$CL_IF" | cut -d' ' -f1)" = 192.168.1.50 ] && pass "laptop static lease 192.168.1.50" || fail "laptop static lease"
lease laptop "$CL_IF" | grep -q "router=192.168.1.1 dns=192.168.1.1 domain=home.arpa ntp=192.168.1.1" && pass "options: router, DNS, domain, NTP" || fail "DHCP options: $(lease laptop "$CL_IF")"
wait_for 30 dhcp phone "$CL_IF" phone && pass "phone lease: $(lease phone "$CL_IF")" || fail "phone got no lease"
PHONE=$(lease phone "$CL_IF" | cut -d' ' -f1)
case $PHONE in 192.168.1.1[0-9][0-9] | 192.168.1.200) pass "phone in the pool" ;; *) fail "phone outside the pool" ;; esac
wait_for 30 dhcp nas "$CL_IF.30" && lease nas "$CL_IF.30" | grep -q "^192.168.30.10 " && pass "NAS static lease on VLAN 30: $(lease nas "$CL_IF.30")" || fail "NAS lease: $(lease nas "$CL_IF.30")"
nsx nas ip addr add fd00:30::10/64 dev "$CL_IF.30" # the NAS's static IPv6 (the address group nas)

echo "== router advertisements (radvd)"
nas_slaac() { nsx nas ip -6 addr show dev "$CL_IF.30" | grep -q "fd00:30::5054:ff:fe00:10/64" && nsx nas ip -6 route | grep -q "default via fe80"; }
laptop_slaac() { nsx laptop ip -6 addr show dev "$CL_IF" | grep -q fd00:1::; }
wait_for 30 nas_slaac && pass "NAS SLAAC address and IPv6 default route" || fail "NAS SLAAC: $(nsx nas ip -6 addr show dev "$CL_IF.30" | grep inet6)"
wait_for 30 laptop_slaac && pass "laptop SLAAC address" || fail "laptop SLAAC"

wait_for 15 nsx remote ping -c1 -W1 10.8.0.1

echo "== DNS (Unbound)"
[ "$(q laptop 192.168.1.1 nas.home.arpa)" = 192.168.30.10 ] && pass "nas.home.arpa = 192.168.30.10" || fail "nas.home.arpa"
phone_name() { [ "$(q laptop 192.168.1.1 phone.home.arpa)" = "$PHONE" ]; }
wait_for 15 phone_name && pass "phone.home.arpa = $PHONE (dynamic lease name)" || fail "phone.home.arpa: $(q laptop 192.168.1.1 phone.home.arpa)"
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
wait_for 15 nsx remote ping -c1 -W1 10.8.0.1 && pass "WireGuard tunnel up" || fail "WireGuard handshake"
expect ok "VPN -> NAS:443 v4" remote tcp 192.168.30.10 443
expect ok "VPN -> NAS:443 v6" remote tcp '[fd00:30::10]' 443 TCP6
expect blocked "VPN -> NAS:8080" remote tcp 192.168.30.10 8080

echo "== NAT"
expect ok "masquerade: LAN -> Internet" laptop tcp 203.0.113.1 80
nsx laptop tcp 203.0.113.1 80 | grep -q "203.0.113.2" && pass "source address is the router's, 203.0.113.2" || fail "masquerade source"
expect ok "port forward: WAN -> router:8080 -> laptop" isp tcp 203.0.113.2 8080
expect ok "reflection: NAS -> router public 203.0.113.2:8080 -> laptop" nas tcp 203.0.113.2 8080
nsx nas tcp 203.0.113.2 8080 | grep -q "laptop 192.168.1.1" && pass "reflected connection comes from the router (answers return through it)" || fail "reflection source"

echo "== UPnP (miniupnpd)"
expect blocked "before mapping: WAN -> router:9000" isp tcp 203.0.113.2 9000
out=$(nsx phone timeout 15 upnpc -m "$CL_IF" -a "$PHONE" 9000 9000 TCP 2>&1) && pass "phone mapped 9000 with UPnP IGD" || fail "upnpc: $(echo "$out" | tail -3)"
expect ok "after mapping: WAN -> router:9000 -> phone" isp tcp 203.0.113.2 9000
nsx phone timeout 15 upnpc -m "$CL_IF" -a 192.168.1.50 9001 8080 TCP >/dev/null 2>&1 && fail "phone mapped a port to another device" || pass "secure mode: phone cannot map to the laptop"

echo "== NTP (chrony)"
out=$(nsx laptop timeout 25 chronyd -Q -t 20 "server 192.168.1.1 iburst maxsamples 1" 2>&1) && pass "laptop got time from the router" || fail "NTP: $(echo "$out" | tail -2)"

echo "== mDNS reflector (Avahi)"
# a service announced on the servers VLAN must be answered on the LAN (legacy unicast query)
put nas /tmp/nas-avahi.conf <<CONF
[server]
allow-interfaces=$CL_IF.30
enable-dbus=no
host-name=nas
[publish]
publish-addresses=yes
CONF
put nas /tmp/smb.service <<CONF
<?xml version="1.0" standalone='no'?><!DOCTYPE service-group SYSTEM "avahi-service.dtd">
<service-group><name>NAS</name><service><type>_smb._tcp</type><port>445</port></service></service-group>
CONF
bg nas "mkdir -p /tmp/nas-services /run/avahi-daemon && cp /tmp/smb.service /tmp/nas-services/ && mount --bind /tmp/nas-services /etc/avahi/services && exec avahi-daemon -f /tmp/nas-avahi.conf --no-drop-root --no-chroot --no-rlimits > /tmp/nas-avahi.log 2>&1"
mdns() { nsx laptop mdns-a | grep -q "c0 a8 1e 0a"; } # 192.168.30.10 in the answer
wait_for 20 mdns && pass "laptop resolves nas.local (announced on VLAN 30) through the reflector" || fail "mDNS reflection: $(nsx laptop mdns-a)"

echo
echo "== $passes passed, $fails failed"
if [ "$fails" -gt 0 ]; then echo "-- routald log:"; nsx router tail -40 /tmp/routald.log; fi
