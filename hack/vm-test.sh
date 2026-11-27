#!/bin/sh
# Boots the Talos image with the extension (make image) in a tart VM on an Apple silicon Mac, applies
# test/vm, and checks what only a real Talos shows: the extension starts, every daemon runs under
# its account with its capabilities, and the WAN side is closed. The VM, its disk and the image are
# removed at the end (KEEP=1 keeps the VM running and prints how to reach it).
# Usage: hack/vm-test.sh <talos version> <out dir>      (make vm-test)
set -eu
version=$1 out=$2
vm=routal-vmtest
work=$out/vm-test
image=$out/metal-arm64.raw

for tool in tart talosctl docker nc ping; do
	command -v "$tool" >/dev/null || { echo "vm-test needs $tool"; exit 1; }
done
[ -f "$image" ] || { echo "$image is missing: make image ARCH=arm64 TALOS_VERSION=$version"; exit 1; }
if tart list | awk '{print $2}' | grep -qx "$vm"; then echo "a VM named $vm exists: tart delete $vm"; exit 1; fi

cleanup() {
	if [ -n "${KEEP:-}" ]; then
		echo "kept: VM $vm, TALOSCONFIG=$work/gen/talosconfig talosctl -n ${ip:-?} -e ${ip:-?} ..."
		return
	fi
	tart stop "$vm" >/dev/null 2>&1 || true
	tart delete "$vm" >/dev/null 2>&1 || true
	rm -rf "$work"
	rm -f "$image"
}
trap cleanup EXIT

echo "== VM $vm, Talos $version"
rm -rf "$work" && mkdir -p "$work"
# 6 GB: the image is 3 GB and EPHEMERAL (/var) needs room, or /var/lib never appears
tart create --linux --disk-size 6 "$vm"
tart set "$vm" --cpu 2 --memory 2048
cp "$image" "$HOME/.tart/vms/$vm/disk.img"
truncate -s 6G "$HOME/.tart/vms/$vm/disk.img"
mac=$(sed -n 's/.*"macAddress" *: *"\([^"]*\)".*/\1/p' "$HOME/.tart/vms/$vm/config.json")
tart run --no-graphics "$vm" >"$work/tart.log" 2>&1 &
ip=$(tart ip "$vm" --wait 120)
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
for _ in $(seq 1 30); do
	if talosctl apply-config --insecure -n "$ip" -e "$ip" -f "$work/config.yaml" >/dev/null 2>&1; then applied=1; break; fi
	sleep 4
done
[ -n "$applied" ] || { echo "could not apply the config"; exit 1; }

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

ping -c 2 -t 3 "$ip" >/dev/null 2>&1 && pass "WAN side: ping answers" || fail "WAN side: no ping"
for port in 53 60123; do
	nc -z -G 3 "$ip" "$port" >/dev/null 2>&1 && fail "WAN side: tcp $port is open" || pass "WAN side: tcp $port is closed"
done

echo "== $passes passed, $fails failed"
[ "$fails" = 0 ] || { echo "-- routald log:"; tail -40 "$work/routald.log"; }
[ "$fails" = 0 ]
