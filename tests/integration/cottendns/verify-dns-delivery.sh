#!/usr/bin/env bash
# Exercise the fixed local CottenDNS client without installing any service.
set -euo pipefail
test_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
router_dir=$(cd "$test_dir/../../.." && pwd)
cotten_dir=${COTTENDNS_DIR:-"$router_dir/../cottenDNS"}
cotten_dir=$(cd "$cotten_dir" && pwd)
[[ -f $cotten_dir/internal/client/dns_pending.go ]] || {
  echo 'This gate requires the local CottenDNS pending DNS callback fix.' >&2
  exit 1
}
work=$(mktemp -d -t cottenrouter-dns-delivery.XXXXXXXX)
trap 'rm -rf -- "$work"' EXIT
cp "$cotten_dir/go.mod" "$cotten_dir/go.sum" "$cotten_dir/server_config.toml.simple" "$cotten_dir/client_config.toml.simple" "$work/"
cp -R "$cotten_dir/cmd" "$cotten_dir/internal" "$cotten_dir/test" "$work/"
cp "$test_dir/dns_delivery_test.go.fixture" "$work/internal/client/dns_delivery_test.go"
cp "$test_dir/dns_delivery_e2e_test.go.fixture" "$work/test/e2e/cottenrouter_dns_delivery_test.go"
sed -i 's/port := freeUDPPortForHost(t, "127.0.0.1")/port := freeDNSPortForHost(t, "127.0.0.1")/' "$work/test/e2e/deployment_test.go"
export COTTEN_ROUTER_DIR="$router_dir"
cd "$work"
go test -race -count=20 -run '^TestDNS(CacheMiss|Delivery)' ./internal/client
go test -v -count=1 -tags=e2e -timeout=6m -run '^TestCottenRouterColdDNSDelivery$' ./test/e2e
