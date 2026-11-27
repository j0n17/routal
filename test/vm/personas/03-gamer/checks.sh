# shellcheck shell=sh
# Sourced by hack/vm-test.sh. Helpers: C runs a command on the first device of the LAN, G on the
# second machine (a LAN device on trunk0, or a visitor on the VLAN guest0), R on the remote laptop;
# dhcp <netns> <link> prints the address of a new lease; ask <address> <port> and askfrom <netns>
# <address> <port> print what a TCP server answers, nothing when blocked; $ip is the router's WAN
# address; pass and fail record a result.
lease=$(dhcp "$client" lan0)
[ -n "$lease" ] && pass "the console gets an address: $lease" || fail "the console got no lease"
[ -z "$(ask "$ip" 9000)" ] && pass "before the game starts: router:9000 is closed from outside" || fail "router:9000 is open without a mapping"
out=$(C timeout 20 upnpc -m lan0 -a "$lease" 8080 9000 TCP 2>&1) && pass "the game maps port 9000 with UPnP" || fail "upnpc: $(echo "$out" | tr '\n' ' ' | cut -c1-500) -- miniupnpd: $(grep miniupnpd "$work/routald.log" | tail -4 | cut -c1-160 | tr '\n' ' ')"
got=$(ask "$ip" 9000)
[ "$got" = first ] && pass "from outside: router:9000 reaches the console" || fail "from outside: router:9000 answered '$got'"
C timeout 20 upnpc -m lan0 -a 192.168.50.77 8080 9001 TCP >/dev/null 2>&1 && fail "the console mapped a port to another device" || pass "the console cannot map a port to another device"
[ -z "$(ask "$ip" 9001)" ] && pass "from outside: router:9001 stays closed" || fail "router:9001 is open"
