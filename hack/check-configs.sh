#!/bin/sh
# Checks every example's and variant's generated configs with the real tools (run in the daemons image, with NET_ADMIN).
set -eu
fail=0
mkdir -p /run/routal /var/lib/routal
for f in /src/examples/*/router.yaml /src/test/variants/*.yaml; do
  case $f in
    */router.yaml) ex=$(basename "$(dirname "$f")") ;;
    *) ex=variant-$(basename "$f" .yaml) ;;
  esac
  out=/tmp/check/$ex
  mkdir -p "$out"
  routald render "$f" > "$out/ruleset.nft"
  if nft -c -f "$out/ruleset.nft"; then echo "$ex nftables: OK"; else echo "$ex nftables: FAILED"; fail=1; fi
  routald render-services "$f" "$out" >/dev/null
  # Kea wants the interfaces to exist: dummy links named like the configured ones
  for l in $(sed -n 's/.*"interface": "\(.*\)".*/\1/p' "$out"/kea-dhcp*.json 2>/dev/null | sort -u); do
    ip link show "$l" >/dev/null 2>&1 || ip link add "$l" type dummy
  done
  for c in "$out"/*; do
    case "$(basename "$c")" in
      kea-dhcp4.json) cmd="kea-dhcp4 -t $c" ;;
      kea-dhcp6.json) cmd="kea-dhcp6 -t $c" ;;
      unbound.conf)   cmd="unbound-checkconf $c" ;;
      radvd.conf)     cmd="radvd -c -C $c" ;;
      chrony.conf)    cmd="chronyd -p -f $c" ;;
      inadyn.conf)    cmd="inadyn --check-config -f $c" ;;
      *) continue ;;
    esac
    if log=$($cmd 2>&1); then echo "$ex $(basename "$c"): OK"; else echo "$ex $(basename "$c"): FAILED"; echo "$log" | tail -5; fail=1; fi
  done
done
exit $fail
