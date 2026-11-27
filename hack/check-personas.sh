#!/bin/sh
# Validates every persona of test/vm/personas without a VM: router.yaml by routald, talos.yaml
# rendered and patched onto a generated config, then `talosctl validate`, for each Talos version
# given. What the personas do is checked on a real Talos by make vm-test.
# Usage: hack/check-personas.sh <out dir> <talos version>...      (make check-personas)
set -eu
out=$1/personas
shift
mkdir -p "$out/secrets"
# placeholders for what the VM test generates
head -c 32 /dev/zero | base64 >"$out/secrets/wg0.key"
peer=$(head -c 32 /dev/zero | base64)
for dir in test/vm/personas/*/; do
	name=$(basename "$dir")
	sed -e 's|\$SUBNET|203.0.113.0/24|' -e 's|\$WANHOST|203.0.113.1|' "$dir/router.yaml" >"$out/$name.router.yaml"
	sed -e 's|\$MAC|52:54:00:d4:00:32|' -e 's|\$LANMAC|52:54:00:d4:00:33|' -e 's|\$SUBNET|203.0.113.0/24|' \
		-e "s|\\\$WGPEER|$peer|" "$dir/talos.yaml" >"$out/$name.talos.yaml"
done
docker run --rm -v "$PWD":/src -v routal-gocache:/root/.cache/go-build -v routal-gomod:/go/pkg/mod -w /src golang:1.26 sh -c "
	go build -buildvcs=false -o /tmp/routald ./cmd/routald && cd $out &&
	for f in *.router.yaml; do
		n=\${f%.router.yaml}
		/tmp/routald validate \$f >/dev/null && /tmp/routald talos-render \$n.talos.yaml > \$n.talos.src.yaml &&
			/tmp/routald talos-patch \$f > \$n.patch.yaml || exit 1
	done"
fail=0
for version in "$@"; do
	tctl() { docker run --rm -v "$PWD/$out":/out "ghcr.io/siderolabs/talosctl:$version" "$@"; }
	tctl gen config routal https://192.0.2.1:6443 --skip-k8s-etcd --output-types controlplane -o "/out/base-$version.yaml" --force >/dev/null 2>&1
	for dir in test/vm/personas/*/; do
		name=$(basename "$dir")
		if tctl machineconfig patch "/out/base-$version.yaml" --patch "@/out/$name.talos.src.yaml" --patch "@/out/$name.patch.yaml" \
			-o "/out/$name-$version.yaml" >/dev/null 2>"$out/err" && tctl validate --mode metal --config "/out/$name-$version.yaml" >/dev/null 2>>"$out/err"; then
			echo "$name on Talos $version: OK"
		else
			echo "$name on Talos $version: FAILED: $(tail -2 "$out/err" | tr '\n' ' ' | cut -c1-240)"
			fail=1
		fi
	done
done
exit $fail
