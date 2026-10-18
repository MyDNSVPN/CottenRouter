This isolated module verifies real encrypted upstream Soar client/server streams
through CottenRouter. It pins Soar v0.2.3, commit
`a8af533d82284f5e6ab13b706552596c108bacf2`, without adding Soar to the router's
runtime dependencies.

Run from this directory:

```sh
go test -v -count=1 ./...
go test -race -count=1 ./...
```

The suite runs two Soar zones and an ordinary DNS route concurrently on one UDP
frontend. Each Soar zone transfers two 32 KiB encrypted echo streams and verifies
every byte. It also checks transaction-ID preservation and the explicit SERVFAIL
response for Soar's disabled direct TCP DNS route. The upstream server listens
only on UDP; Soar's TCP support connects clients to recursive DNS resolvers, whose
authoritative-server hop can still use UDP.

The default tests use only loopback listeners and destinations. Module download
needs network access; the default tests use no public resolver, domain, or service.

Two optional tests exercise real systemd isolation and an installed service.
They require root in the disposable `CottenRouter-Test` WSL2 distribution:

```sh
COTTENROUTER_TEST_SYSTEMD=1 go test -v -count=1 -run TestSoarSystemdDestinationGuard
# Install soar.systemd.test first. This probe makes one public HTTP HEAD request.
COTTENROUTER_TEST_SYSTEMD_NETWORK=1 go test -v -count=1 -run TestInstalledSoarPublicHTTP
# With both soar.systemd.test and soar22.systemd.test installed, probe both concurrently:
COTTENROUTER_TEST_SYSTEMD_NETWORK=1 go test -v -count=1 -run 'TestInstalled.*PublicHTTP'
```
