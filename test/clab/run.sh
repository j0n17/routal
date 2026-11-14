#!/bin/sh
# The integration checks (test/integration/checks.sh) on a containerlab topology (make clab).
# Runs on the Docker host; containerlab itself runs in its container, so this works on macOS
# (Docker Desktop) as on Linux. KEEP=1 leaves the lab running for debugging.
set -u
cd "$(dirname "$0")/../.."
ARCH=${ARCH:-arm64}
CLAB_IMAGE=${CLAB_IMAGE:-ghcr.io/srl-labs/clab:0.71.1}

clab() {
	docker run --rm --privileged --network host --pid host \
		-v /var/run/docker.sock:/var/run/docker.sock -v "$PWD":"$PWD" -w "$PWD" \
		-e ARCH="$ARCH" "$CLAB_IMAGE" containerlab "$@"
}

nsx() { n=$1; shift; docker exec -i "clab-routal-$n" "$@"; }
bg() { docker exec -d "clab-routal-$1" sh -c "$2"; }
put() { docker exec -i "clab-routal-$1" sh -c "cat > $2"; }

R_WAN=eth1 R_LAN=eth2 ISP_R=eth1 ISP_V=eth2 REMOTE_IF=eth1 CL_IF=eth1

echo "== topology (containerlab)"
clab deploy --reconfigure -t test/clab/routal.clab.yml >/dev/null || exit 1
[ -n "${KEEP:-}" ] || trap 'clab destroy --cleanup -t test/clab/routal.clab.yml >/dev/null 2>&1' EXIT

# nodes reach each other only through the topology links, not the management network
for n in router isp remote laptop phone nas; do
	nsx $n ip route del default 2>/dev/null
	nsx $n ip -6 route del default 2>/dev/null
done
nsx sw sh -c 'ip link add br0 type bridge && for i in eth1 eth2 eth3 eth4; do ip link set $i master br0 up; done && ip link set br0 up'

. test/integration/checks.sh

exit $((fails > 0))
