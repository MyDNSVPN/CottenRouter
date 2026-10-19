# CottenMax integration and measured performance — 2026-10-19

Max is a mode inside CottenDNS. Desktop and Android select normal CottenDNS or
Max using the existing profile, domain, resolver list and encryption key. The
upgraded server accepts both modes on its existing listeners. A separate Max
configuration, signing-key file or public-key transfer is no longer required.
Existing explicitly configured experimental identities remain supported for
migration; remove their server key-file/client pin settings to adopt the shared
credential. All holders of that credential share the identity trust boundary.
Fresh ephemeral session keys remain in use.

The adapted transport now includes [Lantern Soar v0.2.3](https://github.com/getlantern/soar/releases/tag/v0.2.3),
commit `a8af533d82284f5e6ab13b706552596c108bacf2`, including unused-session
retirement. Soar's held polling, conservative authenticated startup/path-size
discovery, TXT/AAAA fallback, flow/congestion control, retransmission and
resolver datasets remain present. Authenticated resolver discovery and range
sampling are explicit opt-ins. The independent upstream module is retained for
interoperability and comparison; production uses the attributed source copy.

CottenDNS additionally retains its hold-aware loss detection, ACK ownership,
reordered-response recovery, path-size retry/backoff, authenticated RTT updates,
stalled UDP-to-TCP fallback and half-open TCP/DoT recovery. Its adaptive query
window can grow to 256 with a global limit of 256; it starts conservatively.
Transparent forwarding and local DNS now pipeline data with OPEN. SOCKS/HTTP
continue waiting for confirmed destination success before reporting success.

Local DNS uses an authenticated framed stream and the existing server resolver
and cache. The first-response repair is included in the standalone CottenDNS
core, both app CottenDNS copies, desktop MasterDNS/StormDNS and Android
PhantomDNS. It preserves every caller's ID and handles concurrent waiters,
fragmentation, reused buffers and bounded timeouts.

Max currently requires one client domain, supports UDP/TCP/auto and verified
DoT, and rejects DoH, standby domains and hard query-rate budgets. Legacy
compression/FEC/carrier settings do not govern Max. Normal remains the default;
Max clients need the upgraded server. The currently published upstream server
release does not acquire this local development merely by updating Router.

## Measurement method

Ubuntu 24.04 on WSL2, Go 1.25.0, `GOMAXPROCS=4`. Before and final stages each ran
three complete comparisons serially without competing builds/tests. The
harness and upstream version were unchanged between stages. The production
change between stages was pipelining in the wrapper's client/DNS dial and the
transparent client adapter. Engine scheduling fixes were already in both.

Each run starts a client through a cold 2 KiB random echo, then measures the
median of three fresh-connection 100 KiB random echoes. Query counts are the
mean per warm echo, including dropped requests. The resolver rewrites IDs and
uses independent one-way delay timers, introducing reordering. Both directions
must return byte-exact data. Builds and server startup are outside timing.
Compression and extra duplication are disabled; all use one resolver and the
same reserved namespace. The Soar control uses its unmodified v0.2.3 module and
default congestion settings. Max includes its admission descriptor and wrapper.
These compare configured products, not an isolated algorithm benchmark.

Additional 30 ms RTT scenarios drop every 29th request/31st answer, limit names
to 101 characters, or apply a 1,000-query/s token bucket with a burst of 64.
These are controlled local emulators, not measurements on Iranian networks.
No machine-dependent speed threshold is asserted.

## Final comparison

Cells are medians across three runs. Warm latency is the per-run p50 described
above. A failed run has no valid timing and is never counted as a fast result.

| Scenario | Mode | Cold 2 KiB | Warm 100 KiB echo | Queries/echo | Completed |
|---|---|---:|---:|---:|---:|
| 0 ms RTT | normal | 615 ms | 221 ms | 986 | 3/3 |
| 0 ms RTT | Max | 7 ms | 12 ms | 852 | 3/3 |
| 0 ms RTT | Soar | 2 ms | 13 ms | 893 | 3/3 |
| 30 ms RTT | normal | 725 ms | 493 ms | 946 | 3/3 |
| 30 ms RTT | Max | 162 ms | 190 ms | 855 | 3/3 |
| 30 ms RTT | Soar | 188 ms | 7,246 ms | 5,102 | 3/3 |
| 150 ms RTT | normal | 1,982 ms | 1,111 ms | 948 | 3/3 |
| 150 ms RTT | Max | 763 ms | 1,060 ms | 851 | 3/3 |
| Loss, 30 ms | normal | 1,424 ms | 3,541 ms | 1,074 | 3/3 |
| Loss, 30 ms | Max | 364 ms | 3,768 ms | 3,386 | 3/3 |
| Loss, 30 ms | Soar | — | — | — | 1/3 |
| Short names, 30 ms | normal | — | — | — | 0/3 |
| Short names, 30 ms | Max | 426 ms | 509 ms | 3,625 | 3/3 |
| Short names, 30 ms | Soar | 253 ms | 7,292 ms | 5,207 | 3/3 |
| Rate limited, 30 ms | normal | — | — | — | 0/3 |
| Rate limited, 30 ms | Max | 163 ms | 3,422 ms | 3,637 | 3/3 |
| Rate limited, 30 ms | Soar | 188 ms | 7,653 ms | 5,148 | 3/3 |

Soar's one completed loss run took 17,168 ms warm; its other two runs hit a
20-second read deadline. Normal could not start with the short-name limit and
failed the rate-limited cases during startup/transfer. The complete comparison
therefore returns nonzero: **Max passed all 18 final cases**, while control
failures remain visible. Soar at 150 ms is explicitly omitted by the harness;
there is no result for that case.

## Effect of the final pipelining change

| Max scenario | Cold before → final | Warm before → final |
|---|---:|---:|
| 0 ms RTT | 8 → 7 ms | 10 → 12 ms |
| 30 ms RTT | 225 → 162 ms | 253 → 190 ms |
| 150 ms RTT | 1,067 → 763 ms | 1,363 → 1,060 ms |
| Loss, 30 ms | 394 → 364 ms | 3,623 → 3,768 ms |
| Short names, 30 ms | 257 → 426 ms | 569 → 509 ms |
| Rate limited, 30 ms | 225 → 163 ms | 3,501 → 3,422 ms |

At 30 ms, cold latency fell 28% and warm latency 25%. At 150 ms, the median
warm improvement was 22%. Short-name cold startup regressed, and loss-case
warm latency rose about 4%; zero-delay differences are small. Normal used far
fewer queries under loss and was slightly faster there. Its healthy timing
also varied substantially across stages. These results do not establish that
Max is universally faster, or that the observed Soar slowdown applies to other
resolver workloads.

All individual samples, completion/failure counts, transcript hashes and source
file hashes are in the [machine-readable results](../tests/integration/cottendns/results/2026-10-19-max-performance.json).

## Reproduction and delivery

From CottenRouter in Linux/WSL, with the updated local CottenDNS source:

```bash
COTTENDNS_DIR=/path/to/cottenDNS bash tests/integration/cottendns/verify-max-performance.sh
COTTENDNS_DIR=/path/to/cottenDNS bash tests/integration/cottendns/verify-dns-delivery.sh
# Include all normal and unmodified Soar controls; preserves their failures:
COTTENDNS_DIR=/path/to/cottenDNS COTTENMAX_COMPARE=1 bash tests/integration/cottendns/verify-max-performance.sh
```

The WSL race suite passed for Max, the copied engine, client and server. Actual
Linux binaries passed shared-credential UDP/TCP and all eight cold-DNS paths
(normal/Max × direct/Router × UDP/TCP), with eight cold requests and eight cache
hits per path, correct IDs, no retry and one upstream lookup per path.

Client builds are local development snapshots with explicit source hashes,
not new published upstream releases. Android packaging verifies the four ABI
binary hashes, source build stamps and 16 KiB ELF alignment. Desktop packaging
embeds the rebuilt helpers. No physical Android device or external censored
network was used in this validation.
