# shellcheck shell=sh
# Sourced by hack/vm-test.sh (helpers: C, D, dhcp, ask, pass, fail; $ip is the router's WAN address).
lease=$(dhcp "$client" lan0)
[ "$lease" = 192.168.50.10 ] && pass "the NAS gets its fixed address" || fail "the NAS got '$lease', not 192.168.50.10"
answer=$(D +short nas.home.arpa A | head -1)
[ "$answer" = 192.168.50.10 ] && pass "nas.home.arpa resolves to it" || fail "nas.home.arpa -> '$answer'"
got=$(ask "$ip" 8080)
[ "$got" = device ] && pass "from outside: router:8080 reaches the NAS" || fail "from outside: router:8080 answered '$got'"
[ -z "$(ask "$ip" 8081)" ] && pass "from outside: a port that is not published stays closed" || fail "router:8081 is open"
[ -z "$(ask "$ip" 53)" ] && pass "from outside: the router's DNS is closed" || fail "from outside: the router's DNS answers"
