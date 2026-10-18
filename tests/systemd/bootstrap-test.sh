#!/usr/bin/env bash
# Run only in the explicitly authorized, disposable WSL test distribution.
set -euo pipefail
[[ ${COTTENROUTER_SYSTEMD_TEST:-} == 1 && ${WSL_DISTRO_NAME:-} == CottenRouter-Test && ${EUID} == 0 ]] || {
  echo 'Requires root, COTTENROUTER_SYSTEMD_TEST=1, and the CottenRouter-Test WSL distribution.' >&2
  exit 1
}
[[ $(ps -p 1 -o comm=) == systemd ]] || { echo 'systemd must be PID 1' >&2; exit 1; }
source_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
mkdir -p /root/cottenrouter-validation
fixture=$(mktemp -d /root/cottenrouter-validation/bootstrap.XXXXXX)
export COTTENROUTER_BOOTSTRAP_FIXTURE="$fixture"
mkdir -p "$fixture/bin" "$fixture/release" "$fixture/archive/source"
[[ ! -e /etc/cottenrouter/config.json && ! -e /etc/systemd/system/cottenrouter.service ]] || {
  echo 'Fresh-install test requires no pre-existing router installation.' >&2
  exit 1
}
export PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export GOPROXY='https://proxy.golang.org|direct'
export CGO_ENABLED=0
(cd "$source_dir" && go build -trimpath -ldflags='-X main.version=systemd-test' -o "$fixture/good-router" ./cmd/cottenrouter)
tar -C "$source_dir" --exclude=./.git --exclude=./.cache --exclude=./.codex --exclude=./.agents --exclude=./.aws -cf - . |
  tar -C "$fixture/archive/source" -xf -
tar -C "$fixture/archive" -czf "$fixture/source.tar.gz" source
cp "$fixture/good-router" "$fixture/release/cottenrouter-linux-amd64"
(cd "$fixture/release" && sha256sum cottenrouter-linux-amd64 > SHA256SUMS)
# Only release transport is replaced: run the unchanged current installer,
# its archive extraction, SHA checks, account setup, unit, probes and rollback.
cat > "$fixture/bin/curl" <<'PY'
#!/usr/bin/python3
import os, pathlib, subprocess, sys
root = pathlib.Path(os.environ['COTTENROUTER_BOOTSTRAP_FIXTURE'])
url = next((a for a in sys.argv[1:] if a.startswith('https://')), '')
files = {
 'https://github.com/TaJirax/CottenRouter/archive/refs/tags/v0.0.0.tar.gz': root/'source.tar.gz',
 'https://github.com/TaJirax/CottenRouter/releases/download/v0.0.0/SHA256SUMS': root/'release/SHA256SUMS',
 'https://github.com/TaJirax/CottenRouter/releases/download/v0.0.0/cottenrouter-linux-amd64': root/'release/cottenrouter-linux-amd64',
}
if url in files:
    data = files[url].read_bytes()
    if '-o' in sys.argv:
        pathlib.Path(sys.argv[sys.argv.index('-o')+1]).write_bytes(data)
    else:
        sys.stdout.buffer.write(data)
    sys.exit(0)
sys.exit(subprocess.call(['/usr/bin/curl', *sys.argv[1:]]))
PY
chmod 0755 "$fixture/bin/curl"
run_installer() {
  PATH="$fixture/bin:$PATH" bash "$source_dir/scripts/install.sh" --version=v0.0.0 --no-swap
}
run_installer > "$fixture/fresh.log" 2>&1 || { tail -n 90 "$fixture/fresh.log"; exit 1; }
systemctl is-active --quiet cottenrouter
/usr/local/bin/cottenrouter healthz --config /etc/cottenrouter/config.json
[[ $(systemctl show cottenrouter -p User --value) == cottenrouter ]]
ss -H -lunp 'sport = :53' | grep -q cottenrouter
ss -H -ltnp 'sport = :53' | grep -q cottenrouter
printf 'Fresh bootstrap and unprivileged live service: PASS\n'
# Reproduce root-only existing config: upgrade must leave it readable by the
# actual service account, while keeping contents unchanged.
before_config=$(sha256sum /etc/cottenrouter/config.json | awk '{print $1}')
chown root:root /etc/cottenrouter/config.json
chmod 0600 /etc/cottenrouter/config.json
run_installer > "$fixture/upgrade.log" 2>&1 || { tail -n 90 "$fixture/upgrade.log"; exit 1; }
[[ $(sha256sum /etc/cottenrouter/config.json | awk '{print $1}') == "$before_config" ]]
runuser -u cottenrouter -- test -r /etc/cottenrouter/config.json
/usr/local/bin/cottenrouter healthz --config /etc/cottenrouter/config.json
printf 'Upgrade and existing-config permissions: PASS\n'
# A checksum-valid release can still fail at runtime. Simulate exactly that
# and prove the old executable and live service are restored.
before_binary=$(sha256sum /usr/local/bin/cottenrouter | awk '{print $1}')
cat > "$fixture/release/cottenrouter-linux-amd64" <<'SH'
#!/bin/sh
if [ "${1:-}" = serve ]; then exit 17; fi
exec "${COTTENROUTER_BOOTSTRAP_FIXTURE}/good-router" "$@"
SH
chmod 0755 "$fixture/release/cottenrouter-linux-amd64"
(cd "$fixture/release" && sha256sum cottenrouter-linux-amd64 > SHA256SUMS)
if run_installer > "$fixture/rollback.log" 2>&1; then
  echo 'Broken release unexpectedly passed startup' >&2
  exit 1
fi
[[ $(sha256sum /usr/local/bin/cottenrouter | awk '{print $1}') == "$before_binary" ]]
for attempt in {1..40}; do
  if /usr/local/bin/cottenrouter healthz --config /etc/cottenrouter/config.json >/dev/null 2>&1; then break; fi
  sleep 0.25
done
/usr/local/bin/cottenrouter healthz --config /etc/cottenrouter/config.json
printf 'Failed upgrade restores executable and healthy service: PASS\n'
printf 'Bootstrap systemd lifecycle checks passed; router left installed for backend tests.\n'
printf 'Persistent fixture and logs: %s\n' "$fixture"
