#!/usr/bin/env bash
# Run against the user's current CottenDNS source, including dirty files.
# All binaries, config, keys and fixtures live in temporary copies/ports.
set -euo pipefail
test_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
router_dir=$(cd "$test_dir/../../.." && pwd)
cotten_dir=${COTTENDNS_DIR:-"$router_dir/../cottenDNS"}
cotten_dir=$(cd "$cotten_dir" && pwd)
for path in go.mod go.sum cmd/server internal/cottenmax test/e2e/deployment_test.go test/e2e/cottenmax_test.go; do
  [[ -e $cotten_dir/$path ]] || { echo "Missing local CottenDNS input: $path" >&2; exit 1; }
done
work=$(mktemp -d -t cottenrouter-cottendns.XXXXXXXX)
trap 'rm -rf -- "$work"' EXIT
cp "$cotten_dir/go.mod" "$cotten_dir/go.sum" "$cotten_dir/server_config.toml.simple" "$cotten_dir/client_config.toml.simple" "$work/"
cp -R "$cotten_dir/cmd" "$cotten_dir/internal" "$cotten_dir/test" "$work/"
cp "$test_dir/router_test.go.fixture" "$work/test/e2e/cottenrouter_local_test.go"
# The upstream fixture chooses its router port using UDP alone. The router
# binds TCP too; select a port free for both protocols in this temporary copy.
sed -i 's/port := freeUDPPortForHost(t, "127.0.0.1")/port := freeDNSPortForHost(t, "127.0.0.1")/' "$work/test/e2e/deployment_test.go"
export COTTEN_ROUTER_DIR="$router_dir" COTTEN_RESOLVER_MATRIX=1
export GOPROXY="${GOPROXY:-https://proxy.golang.org|direct}"
cd "$work"
go test -v -count=1 -tags=e2e -parallel=4 -timeout=15m \
  -run '^TestDeployment$/^router$/(clean|realistic)/(default-txt|survival|all)/client=(1|3)$|^TestCottenRouterLocalCottenMax$|^TestCottenMaxBinaryModes$' ./test/e2e
