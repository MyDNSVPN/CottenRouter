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

The 2026-10-18 release validation used local CottenDNS HEAD
`48d6c81d45edd85aaf3b4a88f8ebc06c22b88574` plus its current uncommitted CottenMax
changes. These local files are distinct from the published CottenDNS release
selected by the normal native installer. Publishing CottenRouter does not
publish or change the separate CottenDNS repository.
