# shellcheck shell=sh
# Sourced by hack/vm-test.sh (helpers: C, G, D, dhcp, ask, pass, fail; $ip is the router's WAN address).
lease=$(dhcp "$client" lan0)
[ -n "$lease" ] && pass "a device of the house gets an address: $lease" || fail "the device got no lease"
glease=$(dhcp "$guest" guest0)
case $glease in
192.168.60.1[0-9][0-9]) pass "a visitor gets an address on the guest VLAN: $glease" ;;
*) fail "no guest lease: '$glease'" ;;
esac
answer=$(G dig +time=5 +tries=2 +short @192.168.60.1 example.com A | head -1)
[ -n "$answer" ] && pass "the visitor resolves example.com -> $answer" || fail "guest DNS failed"
G nc -z -w 8 1.1.1.1 443 >/dev/null 2>&1 && pass "the visitor reaches the Internet" || fail "the visitor cannot reach 1.1.1.1:443"
C nc -z -w 3 "$glease" 8080 >/dev/null 2>&1 && fail "the house reaches the visitor (no rule allows it)" || pass "the house does not reach the visitor"
G nc -z -w 3 "$lease" 8080 >/dev/null 2>&1 && fail "the visitor reaches a device of the house" || pass "the visitor cannot reach a device of the house"
G nc -z -w 3 192.168.50.1 53 >/dev/null 2>&1 && fail "the visitor reaches the router's LAN address" || pass "the visitor cannot use the router's LAN address"
G nc -z -w 3 192.168.60.1 50000 >/dev/null 2>&1 && fail "the visitor reaches the Talos API" || pass "the visitor cannot reach the Talos API"
