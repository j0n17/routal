# shellcheck shell=sh
# Sourced by hack/vm-test.sh with ADVANCED=1 (Linux, QEMU), after the life-cycle checks: the homelab
# is the router's config. What a router meets sooner or later: booting with no Internet, a power
# cut that leaves its files half written, a full disk, a clock that starts in the past; then the
# safe apply, and resources that run out.
# Helpers of hack/vm-test.sh: qemu_boot, qemu_halt, wan_link, with_var, apply, running, play, healthy,
# C, G, D, dhcp, ask, closed, T, pass, fail.

# up [seconds]: waits for the Talos API after a boot, then for routald and its daemons
up() {
	starts=0
	for _ in $(seq 1 "$((${1:-240} / 4))"); do T version >/dev/null 2>&1 && break; sleep 4; done
	running
}
again() {
	echo "   $1, the homelab again:"
	# shellcheck disable=SC1090
	. "$homelab/checks.sh"
}
problems() { tr '\n' ' ' <"$work/status.txt" | cut -c1-400; }

echo "== boot without the Internet: the ISP's link is down when the router starts"
qemu_halt
qemu_boot
wan_link off
sleep 100
lease=$(dhcp "$client" lan0)
[ "$lease" = 192.168.50.10 ] && pass "no Internet at boot: a device still gets its address" || fail "no Internet at boot: no DHCP lease ('$lease')"
answer=$(D +short nas.home.arpa A | head -1)
[ "$answer" = 192.168.50.10 ] && pass "no Internet at boot: local names resolve" || fail "no Internet at boot: nas.home.arpa -> '$answer'"
wan_link on
up
healthy && pass "the link comes back: routald reports healthy" || fail "after the link came back: $(problems)"
again "after the link came back"

echo "== power cut: the router loses power, and its files are found half written"
qemu_halt
damage() {
	state=$work/var/lib/routal
	# Kea's leases: cut in the middle of a line, then garbage
	sudo sh -c "head -c 300 $state/kea/kea-leases4.csv > $state/kea/leases.cut && cat $state/kea/leases.cut > $state/kea/kea-leases4.csv && rm $state/kea/leases.cut"
	sudo sh -c "head -c 200 /dev/urandom >> $state/kea/kea-leases4.csv"
	# config files cut short, and a half-written one left beside
	sudo sh -c "head -c 100 $state/unbound.conf > $state/unbound.conf.new; head -c 40 $state/kea-dhcp4.json > $state/kea-dhcp4.json.cut && mv $state/kea-dhcp4.json.cut $state/kea-dhcp4.json"
	sudo sh -c "head -c 64 /dev/urandom > $state/unbound.conf"
	sudo sh -c "for f in $state/*.json $state/chrony/* $state/miniupnpd/*; do [ -f \"\$f\" ] && head -c 50 /dev/urandom > \"\$f\"; done; true"
	sudo ls -la "$state" "$state/kea" | head -30
}
with_var damage
qemu_boot
up
healthy && pass "after a power cut with damaged files: routald reports healthy" || fail "after a power cut with damaged files: $(problems)"
trouble=$(grep -E 'keeps exiting|config rejected' "$work/routald.log" | head -2 || true)
[ -z "$trouble" ] && pass "no daemon is stuck on a damaged file" || fail "after a power cut: $trouble"
again "after a power cut"

echo "== full disk: /var has no space left when the router starts"
qemu_halt
fill() { sudo fallocate -l "$(df --output=avail -B1 "$work/var" | tail -1)" "$work/var/fill" 2>/dev/null || sudo dd if=/dev/zero of="$work/var/fill" bs=1M 2>/dev/null; df -h "$work/var" | tail -1; }
with_var fill
qemu_boot
up
T version >/dev/null 2>&1 && pass "full disk: Talos starts and its API answers" || fail "full disk: no Talos API"
healthy
echo "   routald says: $(head -3 "$work/status.txt" | tr '\n' ' ' | cut -c1-300)"
[ -s "$work/status.json" ] && pass "full disk: routald still reports its state" || fail "full disk: no status report"
closed 53 && pass "full disk: the firewall is in place (the WAN side is closed)" || fail "full disk: the router's DNS is open from outside"
# a device that keeps the address it had: is traffic still routed?
C ip -4 addr flush dev lan0 scope global
C ip addr add 192.168.50.10/24 dev lan0
C ip route replace default via 192.168.50.1
C nc -z -w 8 1.1.1.1 443 >/dev/null 2>&1 && pass "full disk: a device that has an address still reaches the Internet" || fail "full disk: nothing is routed"
lease=$(dhcp "$client" lan0)
answer=$(D +short nas.home.arpa A | head -1)
echo "   with a full disk: DHCP lease '$lease', nas.home.arpa -> '$answer' (reported, not required)"
qemu_halt
empty() { sudo rm -f "$work/var/fill"; }
with_var empty
qemu_boot
up
healthy && pass "space is freed: routald reports healthy again" || fail "after the disk was freed: $(problems)"
again "after the disk was freed"

echo "== clock in the past: the router starts in the year 2000 (no battery), with DNSSEC and DNS over TLS"
if apply_full test/vm/router.yaml test/vm/talos.yaml test/vm/lan-nic.yaml; then
	running
	qemu_halt
	qemu_boot -rtc base=2000-01-01T00:00:00
	up 300
	year=$(sed -n 's/^ *"time": *"\([0-9]*\)-.*/\1/p' "$work/status.json" 2>/dev/null)
	healthy
	year=$(sed -n 's/^ *"time": *"\([0-9]*\)-.*/\1/p' "$work/status.json" 2>/dev/null)
	[ "${year:-0}" -ge 2026 ] && pass "the clock was set from the network: the router is in $year" || fail "the router's clock: year '$year' ($(T version 2>&1 | tail -1))"
	lease=$(dhcp "$client" lan0)
	signed=
	for _ in $(seq 1 20); do
		if D +dnssec example.com A | grep -q 'flags:[^;]* ad[ ;]'; then signed=1; break; fi
		sleep 5
	done
	[ -n "$signed" ] && pass "DNSSEC and DNS over TLS work once the clock is right (both need it)" || fail "after a start in the year 2000: $(D +dnssec example.com A | grep -E 'status|flags' | tr '\n' ' ')"
else
	fail "the baseline config was not applied: $(tail -2 "$work/patch.log" | tr '\n' ' ')"
fi

echo "== safe apply (bin/routal-apply): a good config is confirmed, one that locks the admin out is not"
cat >"$work/talosctl.sh" <<WRAP
#!/bin/sh
exec docker run --rm --network host -v "$PWD/$work":"$PWD/$work" -e TALOSCONFIG="$TALOSCONFIG" ghcr.io/siderolabs/talosctl:$version -n $ip -e $ip "\$@"
WRAP
cat >"$work/routald.sh" <<WRAP
#!/bin/sh
exec docker run --rm -v "$PWD":"$PWD" -w "$PWD" golang:1.26 "$PWD/$work/routald" "\$@"
WRAP
chmod +x "$work/talosctl.sh" "$work/routald.sh"
mkdir -p "$work/apply"
safe_apply() {
	TALOSCTL="$PWD/$work/talosctl.sh" ROUTALD="$PWD/$work/routald.sh" ROUTAL_APPLY_DIR="$PWD/$work/apply" \
		bin/routal-apply --timeout 90s "$@" >"$work/apply.log" 2>&1
}
sed -e "s|\$SUBNET|$subnet|" "$homelab/router.yaml" >"$PWD/$work/apply/homelab.yaml"
if safe_apply --check "nc -z -w 3 $ip 50000" "$PWD/$work/apply/homelab.yaml"; then
	pass "routal-apply confirms a good config: $(tail -1 "$work/apply.log")"
else
	fail "routal-apply refused a good config: $(tail -4 "$work/apply.log" | tr '\n' ' ' | cut -c1-400)"
fi
sleep 100 # past the try timeout: a config that was not confirmed would be gone
T logs ext-routald >"$work/routald.log" 2>&1 || true
again "past the try timeout, the confirmed config is still there"

grep -v '^#' test/vm/personas/01-family/router.yaml | awk 'BEGIN { RS = "---\n"; ORS = "---\n" } !/admin-from-host/' |
	sed -e "s|\$SUBNET|$subnet|" >"$PWD/$work/apply/lockout.yaml"
if safe_apply "$PWD/$work/apply/lockout.yaml"; then
	fail "routal-apply confirmed a config that locks the admin out: $(tr '\n' ' ' <"$work/apply.log" | cut -c1-400)"
else
	pass "routal-apply does not confirm a config that locks the admin out: $(grep -m1 'NOT healthy' "$work/apply.log" | cut -c1-120)"
fi
back=
for _ in $(seq 1 40); do
	if T version >/dev/null 2>&1; then back=1; break; fi
	sleep 4
done
[ -n "$back" ] && pass "Talos took that config back on its own: the API answers again" || fail "the Talos API did not come back after the try timeout"
sleep 20
T logs ext-routald >"$work/routald.log" 2>&1 || true
again "after the rollback"

echo "== exhaustion: more devices than addresses, then more connections than the router can track"
sed -e "s|\$SUBNET|$subnet|" -e 's/^rangeEnd: 192.168.50.200/rangeEnd: 192.168.50.109/' test/vm/personas/01-family/router.yaml >"$work/smallpool.yaml"
if apply "$work/smallpool.yaml"; then
	running
	mine=$(dhcp "$client" lan0)
	cat >"$work/lease-only.sh" <<'SCRIPT'
#!/bin/sh
# udhcpc event script: records the address, configures nothing
[ "$1" = bound ] && echo "$ip" >>"$(dirname "$0")/pool.txt"
exit 0
SCRIPT
	chmod +x "$work/lease-only.sh"
	sudo rm -f "$work/pool.txt"
	for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14; do
		C ip link add "dev$i" link lan0 type macvlan mode bridge
		C ip link set "dev$i" up
		C busybox udhcpc -i "dev$i" -n -q -t 3 -T 2 -s "$PWD/$work/lease-only.sh" >/dev/null 2>&1 || true
	done
	got=$(sort -u "$work/pool.txt" 2>/dev/null | wc -l | tr -d ' ')
	twice=$(sort "$work/pool.txt" 2>/dev/null | uniq -d | tr '\n' ' ')
	# 10 addresses, one held by the first device and maybe one by the second machine of the test
	[ "$got" -ge 7 ] && [ "$got" -le 9 ] && pass "DHCP pool of 10 with 15 devices: $got newcomers are served, the others get nothing" || fail "DHCP pool of 10: $got of 14 newcomers got an address: $(sort -u "$work/pool.txt" 2>/dev/null | tr '\n' ' ')"
	[ -z "$twice" ] && ! grep -qx "$mine" "$work/pool.txt" 2>/dev/null && pass "pool exhausted: no address was given to two devices" || fail "pool exhausted: addresses given twice: $twice (the first device has $mine)"
	again=$(dhcp "$client" lan0)
	[ -n "$mine" ] && [ "$again" = "$mine" ] && pass "pool exhausted: a device that has a lease keeps it ($mine)" || fail "pool exhausted: the first device had '$mine' and now gets '$again'"
	healthy && pass "pool exhausted: routald reports healthy" || fail "pool exhausted: $(problems)"
	for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14; do C ip link del "dev$i" 2>/dev/null || true; done

	# connections that never complete: four addresses of the Internet side that swallow every packet
	for n in 201 202 203 204; do
		sudo ip addr add "$net.$n/32" dev "$bridge"
		sudo iptables -I INPUT -d "$net.$n" -j DROP
	done
	echo "   $(T read /proc/sys/net/netfilter/nf_conntrack_max 2>/dev/null | tr -d '\n') connections can be tracked; a device opens 262140 that never complete"
	C nmap -n -Pn -sS -p- --min-rate 8000 --max-retries 0 "$net.201" "$net.202" "$net.203" "$net.204" >/dev/null 2>&1 || true
	during="answers"
	T version >/dev/null 2>&1 || during="does not answer"
	echo "   right after the burst, with $(T read /proc/sys/net/netfilter/nf_conntrack_count 2>/dev/null | tr -d '\n') connections tracked: the Talos API $during (reported, not required)"
	for n in 201 202 203 204; do
		sudo iptables -D INPUT -d "$net.$n" -j DROP
		sudo ip addr del "$net.$n/32" dev "$bridge"
	done
	recovered=
	for _ in $(seq 1 45); do
		if T version >/dev/null 2>&1 && C nc -z -w 4 1.1.1.1 443 >/dev/null 2>&1; then recovered=1; break; fi
		sleep 4
	done
	[ -n "$recovered" ] && pass "after the burst: the router recovers on its own (API and new connections)" || fail "after the burst: the router did not recover within 3 minutes"
	answer=$(D +short example.com A | head -1)
	[ -n "$answer" ] && pass "after the burst: DNS answers" || fail "after the burst: no DNS answer"
	healthy && pass "after the burst: routald reports healthy, no daemon died" || fail "after the burst: $(problems)"
else
	fail "the small-pool config was not applied: $(tail -2 "$work/patch.log" | tr '\n' ' ')"
fi
