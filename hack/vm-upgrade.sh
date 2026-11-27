#!/bin/sh
# Upgrade test (Linux, QEMU): boots the build of the commit named in test/vm/upgrade-from, gives it
# the homelab's config and state (leases), upgrades it to this build with `talosctl upgrade`, and
# checks that the new build runs and took the state over. test/vm/upgrade-from is the oldest build
# whose state an upgrade must keep: move it to the last release when one is cut.
# Usage: hack/vm-upgrade.sh <talos version> <out dir> <arch>      (make vm-upgrade-test)
set -eu
version=$1 out=$2 arch=$3
from=$(cat test/vm/upgrade-from)
src=$out/upgrade-src
registry=routal-registry

cleanup() {
	docker rm -f "$registry" >/dev/null 2>&1 || true
	sudo rm -rf "$src"
	git worktree prune
}
trap cleanup EXIT
cleanup

echo "== the build to upgrade from: $from"
mkdir -p "$out"
git worktree add --detach "$src" "$from" >/dev/null
make -C "$src" image ARCH="$arch" TALOS_VERSION="$version" VERSION=0.0.1 OUT=_out >"$out/upgrade-old.log" 2>&1 ||
	{ tail -20 "$out/upgrade-old.log"; exit 1; }

echo "== this build's installer, in a registry the VM can reach"
make installer ARCH="$arch" TALOS_VERSION="$version" VERSION=0.0.2 OUT="$out" >"$out/upgrade-new.log" 2>&1 ||
	{ tail -20 "$out/upgrade-new.log"; exit 1; }
docker run -d --name "$registry" -p 5000:5000 registry:2 >/dev/null
tag=build-$(date +%s) # Talos reuses an installer it already pulled under the same tag
make push-installer ARCH="$arch" OUT="$out" IMAGE="localhost:5000/routal-installer:$tag" >>"$out/upgrade-new.log" 2>&1 ||
	{ tail -20 "$out/upgrade-new.log"; exit 1; }

# the host is 203.0.113.1 on the VM's WAN link (see hack/vm-test.sh)
UPGRADE_IMAGE="$src/_out/metal-$arch.raw" UPGRADE_FROM="$from" UPGRADE_REGISTRY=203.0.113.1:5000 \
	UPGRADE_TO="203.0.113.1:5000/routal-installer:$tag" UPGRADE_VERSION=0.0.2 \
	hack/vm-test.sh "$version" "$out" "$arch"
