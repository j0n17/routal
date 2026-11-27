#!/bin/sh
# Prints the Talos versions to test as a JSON list: the last three releases of each supported
# release line (stable ones; pre-releases only while the line has no stable release yet), as
# published on ghcr.io/siderolabs/talosctl.
# Usage: hack/talos-versions.sh [line...]      default: the supported lines below
set -eu
[ $# -gt 0 ] || set -- 1.14 1.15 # the supported Talos release lines: add a line here to test it
exec python3 - "$@" <<'PY'
import json, re, sys, urllib.request

lines = [tuple(int(x) for x in a.split(".")) for a in sys.argv[1:]]
registry = "https://ghcr.io"


def get(url, token=None):
    req = urllib.request.Request(url, headers={"Authorization": "Bearer " + token} if token else {})
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.load(r), r.headers.get("Link", "")


token = get(registry + "/token?scope=repository:siderolabs/talosctl:pull")[0]["token"]
tags, url = [], registry + "/v2/siderolabs/talosctl/tags/list?n=1000"
while url:
    page, link = get(url, token)
    tags += page.get("tags") or []
    m = re.search(r'<([^>]+)>;\s*rel="next"', link)
    url = registry + m[1] if m else None

stage = {"alpha": 0, "beta": 1, "rc": 2, None: 3}
releases = {line: [] for line in lines}
for tag in tags:
    m = re.fullmatch(r"v(\d+)\.(\d+)\.(\d+)(?:-(alpha|beta|rc)\.(\d+))?", tag)
    if m and (int(m[1]), int(m[2])) in releases:
        # sorts by patch, then alpha < beta < rc < stable
        releases[(int(m[1]), int(m[2]))].append(((int(m[3]), stage[m[4]], int(m[5] or 0)), m[4] is None, tag))

out = []
for line in sorted(releases):
    found = sorted(releases[line])
    stable = [r for r in found if r[1]]
    out += [tag for _, _, tag in (stable or found)[-3:]]
print(json.dumps(out))
PY
