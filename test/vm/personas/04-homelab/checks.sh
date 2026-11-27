# shellcheck shell=sh
# Sourced by hack/vm-test.sh. Helpers: C runs a command on the first device of the LAN, G on the
# second machine (a LAN device on trunk0, or a visitor on the VLAN guest0), R on the remote laptop;
# dhcp <netns> <link> prints the address of a new lease; ask <address> <port> and askfrom <netns>
# <address> <port> print what a TCP server answers, nothing when blocked; $ip is the router's WAN
# address; pass and fail record a result.
nas=$(dhcp "$client" lan0)
[ "$nas" = 192.168.50.10 ] && pass "the NAS gets its fixed address" || fail "the NAS got '$nas', not 192.168.50.10"
web=$(dhcp "$guest" trunk0)
[ "$web" = 192.168.50.11 ] && pass "the web server gets its fixed address" || fail "the web server got '$web', not 192.168.50.11"
answer=$(D +short nas.home.arpa A | head -1)
[ "$answer" = 192.168.50.10 ] && pass "nas.home.arpa resolves to the NAS" || fail "nas.home.arpa -> '$answer'"
answer=$(D +short web.home.arpa A | head -1)
[ "$answer" = 192.168.50.11 ] && pass "web.home.arpa resolves to the web server" || fail "web.home.arpa -> '$answer'"
got=$(ask "$ip" 8080)
[ "$got" = first ] && pass "from outside: router:8080 reaches the NAS" || fail "from outside: router:8080 answered '$got'"
got=$(ask "$ip" 8081)
[ "$got" = second ] && pass "from outside: router:8081 reaches the web server" || fail "from outside: router:8081 answered '$got'"
[ -z "$(ask "$ip" 8082)" ] && pass "from outside: a port that is not published stays closed" || fail "router:8082 is open"
[ -z "$(ask "$ip" 53)" ] && pass "from outside: the router's DNS is closed" || fail "from outside: the router's DNS answers"
# After the router restarted, the laptop still holds the keys of the old session: WireGuard only
# starts a new handshake some 15 s after traffic went unanswered.
for _ in 1 2 3 4 5 6 7 8; do
	got=$(askfrom "$remote" 192.168.50.10 8080)
	[ "$got" = first ] && break
	sleep 4
done
[ "$got" = first ] && pass "over the VPN: the laptop on the road reaches the NAS on its LAN address" || fail "over the VPN: the NAS answered '$got' ($(R wg show 2>&1 | grep -E 'handshake|transfer' | tr '\n' ' '))"
got=$(askfrom "$remote" 192.168.50.11 8080)
[ "$got" = second ] && pass "over the VPN: and the web server" || fail "over the VPN: the web server answered '$got'"
answer=$(R dig +time=5 +tries=2 +short @10.8.0.1 nas.home.arpa A | head -1)
[ "$answer" = 192.168.50.10 ] && pass "over the VPN: local names resolve" || fail "over the VPN: nas.home.arpa -> '$answer'"
open_tcp="50000 8080 8081" # what a port scan from outside must find, and nothing else
