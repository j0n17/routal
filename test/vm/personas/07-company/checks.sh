# shellcheck shell=sh
# Sourced by hack/vm-test.sh. Helpers: C runs a command on the first device of the LAN, G on the
# second machine (a LAN device on trunk0, or a visitor on the VLAN guest0), R on the remote laptop;
# dhcp <netns> <link> prints the address of a new lease; ask <address> <port> and askfrom <netns>
# <address> <port> print what a TCP server answers, nothing when blocked; $ip is the router's WAN
# address; pass and fail record a result.
employee=$(dhcp "$client" lan0)
case $employee in
192.168.50.1[0-9][0-9]) pass "an employee's computer gets an address: $employee" ;;
*) fail "no staff lease: '$employee'" ;;
esac
server=$(dhcp "$guest" guest0)
case $server in
192.168.60.1[0-9][0-9]) pass "a server gets an address on the servers' network: $server" ;;
*) fail "no server lease: '$server'" ;;
esac
signed=
for _ in 1 2 3 4 5 6; do
	if D +dnssec example.com A | grep -q 'flags:[^;]* ad[ ;]'; then signed=1; break; fi
	sleep 4
done
[ -n "$signed" ] && pass "DNS answers are validated (DNSSEC), asked over TLS" || fail "DNSSEC over TLS: $(D +dnssec example.com A | grep -E 'status|flags' | tr '\n' ' ')"
C nc -z -w 5 "$server" 8080 >/dev/null 2>&1 && pass "staff reach the servers' web port (port group web)" || fail "staff cannot reach $server:8080"
C nc -z -w 3 "$server" 2222 >/dev/null 2>&1 && fail "staff reach another port of the servers" || pass "staff cannot reach another port of the servers (2222, which listens)"
G nc -z -w 3 "$employee" 8080 >/dev/null 2>&1 && fail "a server reaches a staff computer" || pass "a server cannot reach a staff computer"
G nc -z -w 8 1.1.1.1 443 >/dev/null 2>&1 && pass "a server fetches its updates (tcp 443)" || fail "the server cannot reach 1.1.1.1:443"
G nc -z -w 4 9.9.9.9 853 >/dev/null 2>&1 && fail "a server reaches another port outside" || pass "a server reaches nothing else outside (tcp 853)"
logged=$(T dmesg 2>/dev/null | grep -c 'routal staff-to-servers-refused: ' || true)
[ "$logged" -gt 0 ] && pass "what was refused is in the router's log ($logged packets of staff-to-servers-refused)" || fail "the refused connection was not logged"
# an administrator's workstation has a fixed address, in the group admins
C ip -4 addr flush dev lan0 scope global
C ip addr add 192.168.50.20/24 dev lan0
C ip route replace default via 192.168.50.1
C nc -z -w 5 "$server" 2222 >/dev/null 2>&1 && pass "an administrator reaches every port of the servers (address group admins)" || fail "the administrator cannot reach $server:2222"
[ -z "$(ask "$ip" 53)" ] && pass "from outside: the router's DNS is closed" || fail "from outside: the router's DNS answers"
