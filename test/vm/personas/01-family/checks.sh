# shellcheck shell=sh
# Sourced by hack/vm-test.sh (helpers: C, D, dhcp, ask, pass, fail; $ip is the router's WAN address).
lease=$(dhcp "$client" lan0)
case $lease in
192.168.50.1[0-9][0-9]) pass "a device gets an address: $lease" ;;
*) fail "no lease in the pool: '$lease'" ;;
esac
answer=$(D +short example.com A | head -1)
[ -n "$answer" ] && pass "the device resolves example.com -> $answer" || fail "DNS: $(D example.com A | grep status)"
C nc -z -w 8 1.1.1.1 443 >/dev/null 2>&1 && pass "the device reaches the Internet" || fail "the device cannot reach 1.1.1.1:443"
D doubleclick.net A | grep -q 'status: NXDOMAIN' && pass "an ad domain is blocked" || fail "doubleclick.net: $(D doubleclick.net A | grep status)"
D ad.doubleclick.net A | grep -q 'status: NXDOMAIN' && pass "its subdomains too" || fail "ad.doubleclick.net: $(D ad.doubleclick.net A | grep status)"
D ok.doubleclick.net A | grep -q 'status: NXDOMAIN' && fail "the allowed name is blocked" || pass "an allowed name inside it is not blocked"
[ -z "$(ask "$lease" 8080)" ] && pass "from outside: the device's own server is unreachable" || fail "from outside: the device answers on 8080"
[ -z "$(ask "$ip" 53)" ] && pass "from outside: the router's DNS is closed" || fail "from outside: the router's DNS answers"
