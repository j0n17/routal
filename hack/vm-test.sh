#!/bin/sh
# Boots the Talos image with the extension (make image) in a VM, applies test/vm, and checks what
# only a real Talos shows: the extension starts, every daemon runs under its account with its
# capabilities, and the WAN side is closed. On macOS (Apple silicon) the VM is a tart VM; on Linux
# it is QEMU with KVM, with dnsmasq as the ISP and a client behind the router's second NIC (needs
# sudo; this is what CI runs). There it then plays the personas of test/vm/personas, each with its
# own router.yaml and what that user expects to work, or not to.
# The VM, its disk and the image are removed at the end (KEEP=1 keeps the VM and says how to reach it).
# Usage: hack/vm-test.sh <talos version> <out dir> <arch>      (make vm-test)
set -eu
version=$1 out=$2 arch=$3
vm=routal-vmtest
work=$out/vm-test
image=$out/metal-$arch.raw

case $(uname -s) in
Darwin) backend=tart tools="tart talosctl docker nc ping" ;;
*) backend=qemu tools="docker nc ping sudo dnsmasq ip iptables qemu-system-x86_64 busybox dig socat upnpc" ;;
esac
for tool in $tools; do
	command -v "$tool" >/dev/null || { echo "vm-test needs $tool"; exit 1; }
done
[ -f "$image" ] || { echo "$image is missing: make image ARCH=$arch TALOS_VERSION=$version"; exit 1; }

# The image is 3 GB on arm64 and more on amd64 (a BOOT partition for GRUB), and EPHEMERAL (/var)
# needs room after it, or /var/lib never appears and the extension waits for it.
disk_gb=$(($(wc -c <"$image") / 1073741824 + 5))

# --- tart (macOS): shared network, the Mac is the WAN side
tart_start() {
	if tart list | awk '{print $2}' | grep -qx "$vm"; then echo "a VM named $vm exists: tart delete $vm"; exit 1; fi
	tart create --linux --disk-size "$disk_gb" "$vm"
	tart set "$vm" --cpu 2 --memory 2048
	cp "$image" "$HOME/.tart/vms/$vm/disk.img"
	truncate -s "${disk_gb}G" "$HOME/.tart/vms/$vm/disk.img"
	mac=$(sed -n 's/.*"macAddress" *: *"\([^"]*\)".*/\1/p' "$HOME/.tart/vms/$vm/config.json")
	tart run --no-graphics "$vm" >"$work/tart.log" 2>&1 &
	ip=$(tart ip "$vm" --wait 120)
}
tart_stop() {
	tart stop "$vm" >/dev/null 2>&1 || true
	tart delete "$vm" >/dev/null 2>&1 || true
}

# --- QEMU (Linux): a bridge with dnsmasq and NAT plays the ISP, this host is the WAN side
# The VM's second NIC is the LAN: a bridge with one client on it, a network namespace of this host.
# The WAN network is a documentation range, not a private one: miniupnpd refuses to map ports
# behind a private WAN address.
bridge=br-routal tap=tap-routal net=203.0.113
lanbridge=br-routal-lan lantap=tap-routal-lan client=routal-client guest=routal-guest
qemu_start() {
	mac=52:54:00:d4:00:32 lanmac=52:54:00:d4:00:33 ip=$net.50
	cp "$image" "$work/disk.img"
	truncate -s "${disk_gb}G" "$work/disk.img"
	sudo ip link add "$bridge" type bridge
	sudo ip addr add "$net.1/24" dev "$bridge"
	sudo ip link set "$bridge" up
	sudo ip tuntap add "$tap" mode tap
	sudo ip link set "$tap" master "$bridge" up
	sudo ip link add "$lanbridge" type bridge
	sudo ip link set "$lanbridge" up
	sudo ip tuntap add "$lantap" mode tap
	sudo ip link set "$lantap" master "$lanbridge" up
	sudo ip netns add "$client"
	sudo ip link add veth-routal type veth peer name lan0 netns "$client"
	sudo ip link set veth-routal master "$lanbridge" up
	sudo ip netns exec "$client" ip link set lan0 address 52:54:00:d4:00:40 # the static lease of 03-homelab
	sudo ip netns exec "$client" ip link set lo up
	sudo ip netns exec "$client" ip link set lan0 up
	# a visitor: on the same switch, in VLAN 20
	sudo ip netns add "$guest"
	sudo ip link add veth-routal-g type veth peer name trunk0 netns "$guest"
	sudo ip link set veth-routal-g master "$lanbridge" up
	sudo ip netns exec "$guest" ip link add link trunk0 name guest0 type vlan id 20
	sudo ip netns exec "$guest" ip link set lo up
	sudo ip netns exec "$guest" ip link set trunk0 up
	sudo ip netns exec "$guest" ip link set guest0 up
	# Docker's FORWARD policy is drop, and bridged frames go through it when br_netfilter is loaded
	sudo iptables -I FORWARD -i "$lanbridge" -j ACCEPT
	sudo ip6tables -I FORWARD -i "$lanbridge" -j ACCEPT 2>/dev/null || true
	sudo sysctl -q -w net.ipv4.ip_forward=1
	sudo iptables -t nat -I POSTROUTING -s "$net.0/24" ! -o "$bridge" -j MASQUERADE
	sudo iptables -I FORWARD -i "$bridge" -j ACCEPT
	sudo iptables -I FORWARD -o "$bridge" -j ACCEPT
	sudo dnsmasq --interface="$bridge" --bind-interfaces --except-interface=lo --port=0 \
		--dhcp-range="$net.50,$net.60,1h" --dhcp-host="$mac,$ip" --dhcp-option=6,1.1.1.1,9.9.9.9 \
		--dhcp-leasefile="$PWD/$work/dnsmasq.leases" --pid-file="$PWD/$work/dnsmasq.pid"
	firmware=
	for f in /usr/share/OVMF/OVMF_CODE_4M.fd /usr/share/OVMF/OVMF_CODE.fd /usr/share/ovmf/OVMF.fd; do
		if [ -f "$f" ]; then firmware=$f; break; fi
	done
	[ -n "$firmware" ] || { echo "no OVMF firmware found (apt install ovmf)"; exit 1; }
	accel="-accel tcg -cpu max"
	if [ -e /dev/kvm ]; then accel="-accel kvm -cpu host"; fi
	# shellcheck disable=SC2086
	sudo qemu-system-x86_64 $accel -machine q35 -m 2048 -smp 2 \
		-drive if=pflash,format=raw,readonly=on,file="$firmware" \
		-drive if=virtio,format=raw,file="$work/disk.img" \
		-netdev tap,id=n0,ifname="$tap",script=no,downscript=no -device virtio-net-pci,netdev=n0,mac="$mac" \
		-netdev tap,id=n1,ifname="$lantap",script=no,downscript=no -device virtio-net-pci,netdev=n1,mac="$lanmac" \
		-display none -serial file:"$work/console.log" -daemonize -pidfile "$work/qemu.pid"
	# this host is on the WAN side: what it sends to the LANs goes to the router, as from the Internet
	sudo ip route add 192.168.50.0/24 via "$ip"
	sudo ip route add 192.168.60.0/24 via "$ip"
}
qemu_stop() {
	for pidfile in "$work/qemu.pid" "$work/dnsmasq.pid"; do
		if [ -f "$pidfile" ]; then sudo kill "$(sudo cat "$pidfile")" 2>/dev/null || true; fi
	done
	sudo iptables -t nat -D POSTROUTING -s "$net.0/24" ! -o "$bridge" -j MASQUERADE 2>/dev/null || true
	sudo iptables -D FORWARD -i "$bridge" -j ACCEPT 2>/dev/null || true
	sudo iptables -D FORWARD -o "$bridge" -j ACCEPT 2>/dev/null || true
	sudo iptables -D FORWARD -i "$lanbridge" -j ACCEPT 2>/dev/null || true
	sudo ip6tables -D FORWARD -i "$lanbridge" -j ACCEPT 2>/dev/null || true
	for ns in "$client" "$guest"; do
		sudo ip netns pids "$ns" 2>/dev/null | xargs -r sudo kill 2>/dev/null || true
		sudo ip netns del "$ns" 2>/dev/null || true
	done
	sudo ip link del veth-routal-g 2>/dev/null || true
	sudo ip link del veth-routal 2>/dev/null || true
	sudo ip link del "$lantap" 2>/dev/null || true
	sudo ip link del "$lanbridge" 2>/dev/null || true
	sudo ip link del "$tap" 2>/dev/null || true
	sudo ip link del "$bridge" 2>/dev/null || true
}

cleanup() {
	if [ -n "${KEEP:-}" ]; then
		echo "kept: VM $vm, TALOSCONFIG=$work/gen/talosconfig talosctl -n ${ip:-?} -e ${ip:-?} ..."
		return
	fi
	"${backend}_stop"
	sudo_rm=rm
	if [ "$backend" = qemu ]; then sudo_rm="sudo rm"; fi
	$sudo_rm -rf "$work"
	rm -f "$image"
}
trap cleanup EXIT

if [ "$backend" = tart ]; then
	closed() { ! nc -z -G 3 "$ip" "$1" >/dev/null 2>&1; }
	pings() { ping -c 2 -t 3 "$ip" >/dev/null 2>&1; }
else
	closed() { ! nc -z -w 3 "$ip" "$1" >/dev/null 2>&1; }
	pings() { ping -c 2 -W 3 "$ip" >/dev/null 2>&1; }
	# no talosctl on a CI runner: the one of the version under test, from its image
	talosctl() {
		docker run --rm --network host -v "$PWD/$work":"$PWD/$work" -e TALOSCONFIG="${TALOSCONFIG:-}" \
			"ghcr.io/siderolabs/talosctl:$version" "$@"
	}
fi

echo "== VM $vm ($backend, $arch), Talos $version"
"${backend}_stop" 2>/dev/null || true
rm -rf "$work" && mkdir -p "$work"
"${backend}_start"
subnet=${ip%.*}.0/24
echo "   $ip ($mac)"

echo "== config"
lan=test/vm/lan-dummy.yaml
if [ "$backend" = qemu ]; then lan=test/vm/lan-nic.yaml; fi
cat test/vm/talos.yaml "$lan" | sed -e "s|\$MAC|$mac|" -e "s|\$LANMAC|${lanmac:-}|" -e "s|\$SUBNET|$subnet|" >"$work/talos.yaml"
sed -e "s|\$SUBNET|$subnet|" test/vm/router.yaml >"$work/router.yaml"
docker run --rm -v "$PWD":/src -v routal-gocache:/root/.cache/go-build -v routal-gomod:/go/pkg/mod -w /src golang:1.26 sh -c "
	go build -buildvcs=false -o $work/routald ./cmd/routald &&
	$work/routald validate $work/router.yaml &&
	$work/routald talos-render $work/talos.yaml > $work/talos.src.yaml &&
	$work/routald talos-patch $work/router.yaml > $work/router-patch.yaml"
tctl() { docker run --rm -v "$PWD/$work":/out "ghcr.io/siderolabs/talosctl:$version" "$@"; }
tctl gen config routal https://192.0.2.1:6443 --skip-k8s-etcd --output-types controlplane,talosconfig -o /out/gen --force >/dev/null
tctl machineconfig patch /out/gen/controlplane.yaml --patch @/out/talos.src.yaml --patch @/out/router-patch.yaml -o /out/config.yaml
tctl validate --mode metal --config /out/config.yaml

applied=
for _ in $(seq 1 60); do
	if talosctl apply-config --insecure -n "$ip" -e "$ip" -f "$PWD/$work/config.yaml" >/dev/null 2>&1; then applied=1; break; fi
	sleep 4
done
[ -n "$applied" ] || { echo "could not apply the config"; tail -30 "$work/console.log" 2>/dev/null; exit 1; }

export TALOSCONFIG="$PWD/$work/gen/talosconfig"
T() { talosctl -n "$ip" -e "$ip" "$@"; }
for _ in $(seq 1 60); do
	T services 2>/dev/null | grep -q 'ext-routald *Running' && break
	sleep 4
done
sleep 25 # the daemons start, chrony picks a source

echo "== checks"
fails=0 passes=0
pass() { passes=$((passes + 1)); echo "  ok    $*"; }
fail() { fails=$((fails + 1)); echo "  FAIL  $*"; }

T version 2>/dev/null | grep -q "Tag: *$version" && pass "Talos $version answers on its API from the WAN side (to: self rule)" || fail "Talos version: $(T version 2>&1 | tail -3)"
T services 2>/dev/null | grep -q 'ext-routald *Running' && pass "ext-routald is running" || fail "ext-routald: $(T services 2>&1 | grep routald)"
T logs ext-routald >"$work/routald.log" 2>&1 || true
grep -q 'applied: forwarding' "$work/routald.log" && pass "routald applied its ruleset" || fail "no ruleset applied: $(tail -3 "$work/routald.log")"
denied=$(grep -E 'Permission denied|config rejected|keeps exiting' "$work/routald.log" | head -3 || true)
[ -z "$denied" ] && pass "no daemon was denied a file, rejected its config or kept exiting" || fail "daemons in trouble: $denied"

T processes >"$work/processes.txt" 2>/dev/null || true
# daemon:pattern in the process list:CapEff (10 NET_BIND_SERVICE, 12 NET_ADMIN, 13 NET_RAW)
for d in kea-dhcp4:/usr/sbin/kea-dhcp4:0000000000002400 unbound:/usr/sbin/unbound:0000000000000400 \
	radvd:/usr/sbin/radvd:0000000000002000 chronyd:/usr/sbin/chronyd:0000000000000400 \
	avahi-daemon:avahi-daemon:0000000000000000 miniupnpd:/usr/sbin/miniupnpd:0000000000003000; do
	name=${d%%:*} rest=${d#*:}
	pattern=${rest%%:*} want=${rest#*:}
	pid=$(awk -v p="$pattern" 'index($0, p) { print $2; exit }' "$work/processes.txt")
	if [ -z "$pid" ]; then fail "$name is not running"; continue; fi
	status=$(T read "/proc/$pid/status" 2>/dev/null || true)
	uid=$(echo "$status" | awk '/^Uid:/ { print $2 }')
	caps=$(echo "$status" | awk '/^CapEff:/ { print $2 }')
	if [ -n "$uid" ] && [ "$uid" != 0 ] && [ "$caps" = "$want" ]; then pass "$name: uid $uid, CapEff $caps"
	else fail "$name: uid '$uid', CapEff '$caps' (wanted non-root, $want)"; fi
done

pings && pass "WAN side: ping answers" || fail "WAN side: no ping"
for port in 53 60123; do
	closed "$port" && pass "WAN side: tcp $port is closed" || fail "WAN side: tcp $port is open"
done

if [ "$backend" = qemu ]; then
	echo "== a client behind the router (network namespace on the LAN bridge)"
	C() { sudo ip netns exec "$client" "$@"; }
	G() { sudo ip netns exec "$guest" "$@"; }
	D() { C dig +time=5 +tries=2 @192.168.50.1 "$@"; }
	cat >"$work/udhcpc.sh" <<'SCRIPT'
#!/bin/sh
# udhcpc event script: configures the lease and records it
[ "$1" = bound ] || exit 0
ip addr add "$ip/${mask:-24}" dev "$interface"
ip route replace default via "$router"
echo "ip=$ip router=$router dns=$dns domain=$domain ntp=$ntpsrv" >"$(dirname "$0")/lease"
SCRIPT
	chmod +x "$work/udhcpc.sh"
	# dhcp <netns> <ifname>: gives up the address it has, asks for a lease, prints its address
	dhcp() {
		sudo ip netns exec "$1" ip -4 addr flush dev "$2"
		sudo rm -f "$work/lease"
		sudo ip netns exec "$1" busybox udhcpc -i "$2" -n -q -t 8 -T 2 -O ntpsrv -O domain -s "$PWD/$work/udhcpc.sh" >"$work/udhcpc.log" 2>&1 || true
		sed -n 's/^ip=\([^ ]*\) .*/\1/p' "$work/lease" 2>/dev/null || true
	}
	# ask <address> <port>: what a TCP server answers to this host (the WAN side), nothing when blocked
	ask() { timeout 8 socat -t 3 -T 5 - "TCP4:$1:$2,connect-timeout=3" </dev/null 2>/dev/null || true; }
	# every device runs a small server on 8080 that says who it is
	C socat TCP4-LISTEN:8080,fork,reuseaddr SYSTEM:'echo device' >/dev/null 2>&1 &
	G socat TCP4-LISTEN:8080,fork,reuseaddr SYSTEM:'echo visitor' >/dev/null 2>&1 &
	dhcp "$client" lan0 >/dev/null
	lease=$(cat "$work/lease" 2>/dev/null || true)
	case $lease in
	"ip=192.168.50.1"[0-9][0-9]" router=192.168.50.1 dns=192.168.50.1 domain=home.arpa ntp=192.168.50.1") pass "DHCP lease from Kea: $lease" ;;
	*) fail "DHCP lease: '$lease' $(tail -2 "$work/udhcpc.log" | tr '\n' ' ')" ;;
	esac
	slaac=
	for _ in $(seq 1 30); do
		slaac=$(C ip -6 -o addr show dev lan0 scope global | awk '{ print $4 }' | grep '^fd00:50:' || true)
		[ -n "$slaac" ] && break
		sleep 2
	done
	[ -n "$slaac" ] && pass "SLAAC address from radvd: $slaac" || fail "no SLAAC address: $(C ip -6 -o addr show dev lan0)"
	answer=$(D +short example.com A | head -1)
	[ -n "$answer" ] && pass "DNS through Unbound (DNS over TLS upstream): example.com -> $answer" || fail "DNS: $(D example.com A | tail -5 | tr '\n' ' ')"
	D +dnssec example.com A | grep -q 'flags:[^;]* ad[ ;]' && pass "DNSSEC: a signed name is validated (ad)" || fail "DNSSEC: no ad flag: $(D +dnssec example.com A | grep flags)"
	D dnssec-failed.org A | grep -q 'status: SERVFAIL' && pass "DNSSEC: a name with a broken signature is refused" || fail "DNSSEC: dnssec-failed.org: $(D dnssec-failed.org A | grep status)"
	C nc -z -w 8 1.1.1.1 443 >/dev/null 2>&1 && pass "client -> Internet through the router (masquerade)" || fail "client cannot reach 1.1.1.1:443 through the router"
	C nc -z -w 3 192.168.50.1 53 >/dev/null 2>&1 && pass "LAN side: tcp 53 is open" || fail "LAN side: tcp 53 is closed"
	C nc -z -w 3 192.168.50.1 50000 >/dev/null 2>&1 && fail "LAN side: the Talos API is open (no rule opens it there)" || pass "LAN side: the Talos API is closed (no rule opens it there)"

	# Personas: who uses such a router, and what each of them expects from it. Each one is a complete
	# router.yaml, applied the way a user would (talos-patch, then talosctl patch mc).
	for dir in test/vm/personas/*/; do
		name=$(basename "$dir")
		echo "== persona ${name#*-}: $(sed -n '1s/^# *//p' "$dir/router.yaml")"
		sed -e "s|\$SUBNET|$subnet|" "$dir/router.yaml" >"$work/persona.yaml"
		T get mc -o yaml >"$work/current.yaml" 2>/dev/null
		starts=$(T logs ext-routald 2>/dev/null | grep -c 'routald starting' || true)
		docker run --rm -v "$PWD":/src -w /src golang:1.26 sh -c "
			$work/routald validate $work/persona.yaml >/dev/null &&
			$work/routald talos-patch -current $work/current.yaml $work/persona.yaml > $work/persona-patch.yaml"
		if ! T patch mc --patch "@$PWD/$work/persona-patch.yaml" >"$work/patch.log" 2>&1; then
			fail "${name#*-}: the config was not applied: $(tail -2 "$work/patch.log" | tr '\n' ' ')"
			continue
		fi
		# Talos restarts routald with the new router.yaml
		for _ in $(seq 1 40); do
			[ "$(T logs ext-routald 2>/dev/null | grep -c 'routald starting' || true)" -gt "$starts" ] && break
			sleep 3
		done
		sleep 15 # the daemons start
		T logs ext-routald >"$work/routald.log" 2>&1 || true
		# shellcheck disable=SC1090
		. "$dir/checks.sh"
	done
fi

echo "== $passes passed, $fails failed"
[ "$fails" = 0 ] || {
	echo "-- volumes:"
	T get volumestatus 2>&1 | grep -E 'EPHEMERAL|STATE' || true
	echo "-- routald log:"
	tail -40 "$work/routald.log"
}
[ "$fails" = 0 ]
