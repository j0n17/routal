#!/bin/sh
# Prints the Talos versions to test as a JSON list: for every major, the newest release of each of
# its last three minors (a stable release, else its newest pre-release), as published on
# ghcr.io/siderolabs/talosctl. Minors below MIN, the oldest the extension supports, are left out.
# Usage: hack/talos-versions.sh [min minor, default 1.14]
set -eu
exec python3 - "${1:-1.14}" <<'PY'
import json, re, sys, urllib.request

floor = tuple(int(x) for x in sys.argv[1].split("."))
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
newest = {}
for tag in tags:
    m = re.fullmatch(r"v(\d+)\.(\d+)\.(\d+)(?:-(alpha|beta|rc)\.(\d+))?", tag)
    if not m or (int(m[1]), int(m[2])) < floor:
        continue
    # a stable release beats any pre-release of the same minor
    key = (m[4] is None, int(m[3]), stage[m[4]], int(m[5] or 0))
    minor = (int(m[1]), int(m[2]))
    if minor not in newest or key > newest[minor][0]:
        newest[minor] = (key, tag)

# the last three minors of every major
out = []
for major in sorted({m for m, _ in newest}):
    out += [newest[k][1] for k in sorted(k for k in newest if k[0] == major)[-3:]]
print(json.dumps(out))
PY
