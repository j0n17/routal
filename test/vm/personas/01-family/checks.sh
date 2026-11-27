# shellcheck shell=sh
# Sourced by hack/vm-test.sh. Helpers: C runs a command on the first device of the LAN, G on the
# second machine (a LAN device on trunk0, or a visitor on the VLAN guest0), R on the remote laptop;
# dhcp <netns> <link> prints the address of a new lease; ask <address> <port> and askfrom <netns>
# <address> <port> print what a TCP server answers, nothing when blocked; $ip is the router's WAN
# address; pass and fail record a result.
lease=$(dhcp "$client" lan0)
case $lease in
192.168.50.1[0-9][0-9]) pass "a device gets an address: $lease" ;;
*) fail "no lease in the pool: '$lease'" ;;
esac
answer=$(D +short example.com A | head -1)
[ -n "$answer" ] && pass "the device resolves example.com -> $answer" || fail "DNS: $(D example.com A | grep status)"
C nc -z -w 8 1.1.1.1 443 >/dev/null 2>&1 && pass "the device reaches the Internet" || fail "the device cannot reach 1.1.1.1:443"
C nc -z -w 8 9.9.9.9 853 >/dev/null 2>&1 && pass "on any port (no outbound filtering)" || fail "the device cannot reach 9.9.9.9:853"
D doubleclick.net A | grep -q 'status: NXDOMAIN' && pass "an ad domain is blocked" || fail "doubleclick.net: $(D doubleclick.net A | grep status)"
D ad.doubleclick.net A | grep -q 'status: NXDOMAIN' && pass "its subdomains too" || fail "ad.doubleclick.net: $(D ad.doubleclick.net A | grep status)"
D ok.doubleclick.net A | grep -q 'status: NXDOMAIN' && fail "the allowed name is blocked" || pass "an allowed name inside it is not blocked"
[ -z "$(ask "$lease" 8080)" ] && pass "from outside: the device's own server is unreachable" || fail "from outside: the device answers on 8080"
[ -z "$(ask "$ip" 53)" ] && pass "from outside: the router's DNS is closed" || fail "from outside: the router's DNS answers"
