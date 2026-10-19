#!/usr/bin/env bash
# Run in Linux/WSL against the local CottenDNS development source. No install.
set -euo pipefail
test_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
router_dir=$(cd "$test_dir/../../.." && pwd)
cotten_dir=${COTTENDNS_DIR:-"$router_dir/../cottenDNS"}
cd "$cotten_dir"
[[ -f internal/cottenmax/pipelining_test.go && -f test/e2e/cottenmax_comparison_test.go ]] || {
  echo 'Use the updated local CottenDNS source, including Max integration tests.' >&2
  exit 1
}
grep -q 'github.com/getlantern/soar v0.2.3' go.mod || {
  echo 'The independent comparison requires the pinned Soar v0.2.3 module.' >&2
  exit 1
}
go test -race -count=1 -timeout=10m ./internal/cottenmax/... ./internal/client ./internal/udpserver
go test -v -tags=e2e -count=1 -timeout=6m -run '^TestCottenMaxSharedCredentialModes$' ./test/e2e
# All Max impairment cases must complete with byte-exact responses. Timing is
# reported rather than asserted because machine load affects measurements.
go test -v -tags=e2e -count=3 -timeout=15m \
  -run '^TestCottenMax(LocalComparison|ImpairedComparison)$/.*/cottenmax$' ./test/e2e
if [[ ${COTTENMAX_COMPARE:-0} == 1 ]]; then
  # Full controls are strict too: an unsupported/failing legacy or upstream
  # path returns nonzero. Preserve those failures when reporting comparisons.
  go test -v -tags=e2e -count=3 -timeout=20m \
    -run '^TestCottenMax(LocalComparison|ImpairedComparison)$' ./test/e2e
fi
