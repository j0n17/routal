#!/bin/sh
# Shows why a CI step failed as annotations on the run: the failing lines, then the end of the log.
# Usage: hack/ci-summary.sh <log file>
log=$1
[ -f "$log" ] || exit 0
esc() { sed -e 's/%/%25/g' | awk '{ printf "%s%%0A", $0 }'; }
echo "::error title=Failing lines::$(grep -aE 'FAIL|Error|error:|panic' "$log" | tail -n 40 | cut -c1-300 | esc)"
echo "::error title=End of the log::$(tail -n 50 "$log" | cut -c1-300 | esc)"
