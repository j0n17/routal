#!/bin/sh
# Runs every fuzz target for a while (run in the daemons image by make fuzz). A failing input is
# written to the package's testdata/fuzz/<target>/; the full log of a target is in <bin>/<target>.log.
set -eu
fuzztime=$1 bin=$2
mkdir -p /run/routal /var/lib/routal
fail=0
for t in pkg/blocklist:FuzzParse pkg/pd:FuzzParse pkg/leases:FuzzKea test/variants:FuzzConfig test/variants:FuzzField; do
  pkg=${t%%:*} name=${t##*:}
  echo "== $pkg $name ($fuzztime)"
  cd "/src/$pkg"
  # the progress lines only count executions when a worker reports: keep the first, the last and failures
  "$bin/$(basename "$pkg").test" -test.run '^$' -test.fuzz "^$name\$" -test.fuzztime "$fuzztime" \
    -test.fuzzcachedir "$bin/cache" > "$bin/$name.log" 2>&1 || fail=1
  grep -v '^fuzz: elapsed' "$bin/$name.log" || true
  grep '^fuzz: elapsed' "$bin/$name.log" | tail -1
done
exit $fail
