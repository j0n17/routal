#!/bin/sh
# Validates every example's talos.yaml: rendered by `routald talos-render` (key file references),
# patched onto a generated Kubernetes-free config, then `talosctl validate --mode metal`.
# Usage: hack/check-talos.sh <talos version> <out dir>
set -eu
version=$1 out=$2
mkdir -p "$out"
docker run --rm -v "$PWD":/src -v routal-gocache:/root/.cache/go-build -v routal-gomod:/go/pkg/mod -w /src golang:1.26 sh -c "
	go build -o /tmp/routald ./cmd/routald &&
	for f in examples/*/talos.yaml; do /tmp/routald talos-render \$f > $out/\$(basename \$(dirname \$f)).src.yaml || exit 1; done"
talosctl() { docker run --rm -v "$PWD/$out":/out "ghcr.io/siderolabs/talosctl:$version" "$@"; }
talosctl gen config routal https://192.0.2.1:6443 --skip-k8s-etcd --output-types controlplane -o /out/base.yaml --force >/dev/null
fail=0
for f in examples/*/talos.yaml; do
	ex=$(basename "$(dirname "$f")")
	if talosctl machineconfig patch /out/base.yaml --patch "@/out/$ex.src.yaml" -o "/out/$ex.yaml" && talosctl validate --mode metal --config "/out/$ex.yaml" >/dev/null; then
		echo "$ex talos.yaml: OK"
	else
		echo "$ex talos.yaml: FAILED"; fail=1
	fi
done
exit $fail
