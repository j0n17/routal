# shellcheck shell=sh
# Sourced by hack/vm-test.sh. Helpers: C runs a command on the first device of the LAN, G on the
# second machine (a LAN device on trunk0, or a visitor on the VLAN guest0), R on the remote laptop;
# dhcp <netns> <link> prints the address of a new lease; ask <address> <port> and askfrom <netns>
# <address> <port> print what a TCP server answers, nothing when blocked; $ip is the router's WAN
# address; pass and fail record a result.
staff=$(dhcp "$client" lan0)
[ -n "$staff" ] && pass "a staff computer gets an address: $staff" || fail "the staff computer got no lease"
room=$(dhcp "$guest" guest0)
case $room in
192.168.60.1[0-9][0-9]) pass "a guest gets an address on the guest network: $room" ;;
*) fail "no guest lease: '$room'" ;;
esac
answer=$(G dig +time=5 +tries=2 +short @192.168.60.1 example.com A | head -1)
[ -n "$answer" ] && pass "the guest resolves through the hotel's DNS: example.com -> $answer" || fail "guest DNS failed"
G nc -z -w 8 1.1.1.1 443 >/dev/null 2>&1 && pass "the guest reaches https (tcp 443)" || fail "the guest cannot reach 1.1.1.1:443"
G nc -z -w 8 1.1.1.1 80 >/dev/null 2>&1 && pass "and http (tcp 80)" || fail "the guest cannot reach 1.1.1.1:80"
[ -z "$(G dig +time=3 +tries=1 +short @9.9.9.9 example.com A 2>/dev/null | grep -v '^;')" ] && pass "the guest cannot ask another resolver (udp 53)" || fail "the guest resolved through 9.9.9.9"
G nc -z -w 4 9.9.9.9 853 >/dev/null 2>&1 && fail "the guest reaches tcp 853" || pass "another port is closed (tcp 853, open on the Internet)"
G nc -z -w 4 1.1.1.1 53 >/dev/null 2>&1 && fail "the guest reaches tcp 53 outside" || pass "and another one (tcp 53)"
G nc -z -w 3 "$staff" 8080 >/dev/null 2>&1 && fail "the guest reaches a staff computer" || pass "the guest cannot reach the staff network"
G nc -z -w 3 192.168.60.1 50000 >/dev/null 2>&1 && fail "the guest reaches the Talos API" || pass "the guest cannot reach the Talos API"
C nc -z -w 8 9.9.9.9 853 >/dev/null 2>&1 && pass "the staff network is not restricted (tcp 853)" || fail "the staff computer cannot reach 9.9.9.9:853"
