# shellcheck shell=sh
# Sourced by hack/vm-test.sh. Helpers: C runs a command on the first device of the LAN, G on the
# second machine (a LAN device on trunk0, or a visitor on the VLAN guest0), R on the remote laptop;
# dhcp <netns> <link> prints the address of a new lease; ask <address> <port> and askfrom <netns>
# <address> <port> print what a TCP server answers, nothing when blocked; $ip is the router's WAN
# address; pass and fail record a result.
kid=$(dhcp "$client" lan0)
[ "$kid" = 192.168.50.20 ] && pass "the child's tablet gets its fixed address" || fail "the tablet got '$kid', not 192.168.50.20"
parent=$(dhcp "$guest" trunk0)
[ -n "$parent" ] && pass "a parent's laptop gets an address: $parent" || fail "the laptop got no lease"
answer=$(D +short example.com A | head -1)
[ -n "$answer" ] && pass "the tablet resolves example.com -> $answer" || fail "DNS: $(D example.com A | grep status)"
D www.tiktok.com A | grep -q 'status: NXDOMAIN' && pass "a blocked site does not resolve" || fail "www.tiktok.com: $(D www.tiktok.com A | grep status)"
C nc -z -w 8 1.1.1.1 443 >/dev/null 2>&1 && pass "the tablet reaches the web" || fail "the tablet cannot reach 1.1.1.1:443"
[ -z "$(C dig +time=3 +tries=1 +short @9.9.9.9 example.com A 2>/dev/null | grep -v '^;')" ] && pass "the tablet cannot ask another resolver (udp 53)" || fail "the tablet resolved through 9.9.9.9"
C nc -z -w 4 9.9.9.9 853 >/dev/null 2>&1 && fail "the tablet reaches a DNS over TLS resolver" || pass "the tablet cannot reach a DNS over TLS resolver (tcp 853)"
C nc -z -w 4 9.9.9.9 53 >/dev/null 2>&1 && fail "the tablet reaches another resolver over TCP" || pass "nor another resolver over TCP"
[ -n "$(G dig +time=5 +tries=2 +short @9.9.9.9 example.com A 2>/dev/null | grep -v '^;')" ] && pass "the parent's laptop can use another resolver" || fail "the laptop cannot ask 9.9.9.9"
G nc -z -w 8 9.9.9.9 853 >/dev/null 2>&1 && pass "and DNS over TLS" || fail "the laptop cannot reach 9.9.9.9:853"
