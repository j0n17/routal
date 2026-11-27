#!/bin/sh
# Boots the Talos image with the extension (make image) in a VM, applies test/vm, and checks what
# only a real Talos shows: the extension starts, every daemon runs under its account with its
# capabilities, and the WAN side is closed. On macOS (Apple silicon) the VM is a tart VM; on Linux
# it is QEMU with KVM on a tap device, with dnsmasq as the ISP (needs sudo; this is what CI runs).
# The VM, its disk and the image are removed at the end (KEEP=1 keeps the VM and says how to reach it).
# Usage: hack/vm-test.sh <talos version> <out dir> <arch>      (make vm-test)
set -eu
version=$1 out=$2 arch=$3
vm=routal-vmtest
work=$out/vm-test
image=$out/metal-$arch.raw

case $(uname -s) in
Darwin) backend=tart tools="tart talosctl docker nc ping" ;;
*) backend=qemu tools="docker nc ping sudo dnsmasq ip iptables qemu-system-x86_64" ;;
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
bridge=br-routal tap=tap-routal net=192.168.212
qemu_start() {
	mac=52:54:00:d4:00:32 ip=$net.50
	cp "$image" "$work/disk.img"
	truncate -s "${disk_gb}G" "$work/disk.img"
	sudo ip link add "$bridge" type bridge
	sudo ip addr add "$net.1/24" dev "$bridge"
	sudo ip link set "$bridge" up
	sudo ip tuntap add "$tap" mode tap
	sudo ip link set "$tap" master "$bridge" up
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
		-display none -serial file:"$work/console.log" -daemonize -pidfile "$work/qemu.pid"
}
qemu_stop() {
	for pidfile in "$work/qemu.pid" "$work/dnsmasq.pid"; do
		if [ -f "$pidfile" ]; then sudo kill "$(sudo cat "$pidfile")" 2>/dev/null || true; fi
	done
	sudo iptables -t nat -D POSTROUTING -s "$net.0/24" ! -o "$bridge" -j MASQUERADE 2>/dev/null || true
	sudo iptables -D FORWARD -i "$bridge" -j ACCEPT 2>/dev/null || true
	sudo iptables -D FORWARD -o "$bridge" -j ACCEPT 2>/dev/null || true
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
sed -e "s|\$MAC|$mac|" -e "s|\$SUBNET|$subnet|" test/vm/talos.yaml >"$work/talos.yaml"
sed -e "s|\$SUBNET|$subnet|" test/vm/router.yaml >"$work/router.yaml"
docker run --rm -v "$PWD":/src -v routal-gocache:/root/.cache/go-build -v routal-gomod:/go/pkg/mod -w /src golang:1.26 sh -c "
	go build -buildvcs=false -o /tmp/routald ./cmd/routald &&
	/tmp/routald validate $work/router.yaml &&
	/tmp/routald talos-render $work/talos.yaml > $work/talos.src.yaml &&
	/tmp/routald talos-patch $work/router.yaml > $work/router-patch.yaml"
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
denied=$(grep -E 'Permission denied|config rejected|keeps exiting' "$work/routald.log" | grep -v CurHopLimit | head -3 || true)
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

echo "== $passes passed, $fails failed"
[ "$fails" = 0 ] || {
	echo "-- volumes:"
	T get volumestatus 2>&1 | grep -E 'EPHEMERAL|STATE' || true
	echo "-- routald log:"
	tail -40 "$work/routald.log"
}
[ "$fails" = 0 ]
