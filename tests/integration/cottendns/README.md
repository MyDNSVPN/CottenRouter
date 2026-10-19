# Exact local CottenDNS compatibility

Run on Linux with Go 1.25+ and the latest CottenDNS development checkout:

```bash
COTTENDNS_DIR=/path/to/cottenDNS bash tests/integration/cottendns/verify-local.sh
```

The script copies the current source, including uncommitted changes, to a
temporary directory. It builds the actual client/server and this router, then
checks 64 KiB encrypted application echoes through real routing: legacy XOR and
AES-128-GCM with three query-type presets behind clean/realistic UDP resolvers,
plus CottenMax over UDP and TCP. CottenMax is also checked directly over UDP,
TCP and certificate-pinned DoT on IPv4 and IPv6. It does not install system
services or modify either checkout. Its temporary fixtures fix upstream's
UDP-only port allocation and avoid putting a UDP simulator in the TCP path.

CottenMax is a module inside CottenDNS, with one installer, service and router
entry. Enable `TUNNEL_MODE="cottenmax"` in a supporting CottenDNS server to
accept both normal and CottenMax clients; clients choose their mode. The gate
also verifies normal XOR/AES clients against that CottenMax-enabled server.

The 2026-10-18 release validation used local CottenDNS HEAD
`48d6c81d45edd85aaf3b4a88f8ebc06c22b88574` plus its current uncommitted CottenMax
changes. These local files are distinct from the published CottenDNS release
selected by the normal native installer. Publishing CottenRouter does not
publish or change the separate CottenDNS repository.

## Cold DNS response delivery

The published CottenDNS client could fill its local DNS cache without replying
to the first request. This requires a client fix; a router/server upgrade alone
does not repair installed clients. See the [investigation and focused patch](../../../docs/cottendns-first-response.md).

After applying that fix to the local CottenDNS source, run:

```bash
COTTENDNS_DIR=/path/to/cottenDNS bash tests/integration/cottendns/verify-dns-delivery.sh
```

This gate runs the delivery regression repeatedly under the race detector,
then tests real binaries directly and through CottenRouter using UDP and TCP.
Eight cold queries and eight cache hits must answer with correct transaction
IDs without retransmission, using only one controlled upstream lookup.
