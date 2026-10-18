#!/usr/bin/env bash
# Destructive only inside the explicitly named disposable test distribution.
set -euo pipefail
[[ ${COTTENROUTER_SYSTEMD_TEST:-} == 1 && ${WSL_DISTRO_NAME:-} == CottenRouter-Test && ${EUID} == 0 ]] || {
  echo 'Requires root, COTTENROUTER_SYSTEMD_TEST=1, and CottenRouter-Test.' >&2
  exit 1
}
source_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
logs=/root/cottenrouter-validation/logs
mkdir -p "$logs"
systemctl restart cottendns masterdnsvpn stormdns thefeed-server slipgate-dnstt soar-server soar22-server cottenrouter
for attempt in {1..30}; do
  if cottenrouter healthz --config /etc/cottenrouter/config.json; then break; fi
  sleep 0.2
done
python3 "$source_dir/tests/systemd/native-routing.py"
configs=(
  /etc/cottenrouter/config.json
  /opt/cottenrouter/backends/cottendns/server_config.toml
  /opt/cottenrouter/backends/masterdnsvpn/server_config.toml
  /opt/cottenrouter/backends/stormdns/server_config.toml
  /opt/thefeed/data/thefeed.env
  /etc/slipgate/config.json
  /etc/slipgate/tunnels/dnstt/server.key
  /opt/cottenrouter/backends/soar/soar.env
  /opt/cottenrouter/backends/soar/server.key
  /opt/cottenrouter/backends/soar22/soar.env
  /opt/cottenrouter/backends/soar22/server.key
)
sha256sum "${configs[@]}" > "$logs/preserved-config-checksums"
bash "$source_dir/scripts/uninstall.sh" > "$logs/uninstall-preserve.log" 2>&1
[[ ! -e /usr/local/bin/cottenrouter && ! -e /etc/systemd/system/cottenrouter.service ]]
sha256sum --check --status "$logs/preserved-config-checksums"
for service in cottendns masterdnsvpn stormdns thefeed-server slipgate-dnstt soar-server soar22-server; do
  systemctl is-active --quiet "$service"
done
echo 'Default uninstall preserved every backend and config/identity byte.'
bash "$source_dir/scripts/uninstall.sh" --purge --purge-backends --confirm CottenRouter > "$logs/uninstall-purge.log" 2>&1
for path in /etc/cottenrouter /opt/cottenrouter/backends /opt/thefeed /etc/slipgate \
  /usr/local/bin/cottenrouter /usr/local/bin/slipgate /usr/local/bin/dnstt-server \
  /etc/systemd/system/cottenrouter.service /etc/systemd/system/cottenrouter-backends.slice \
  /var/lib/cottenrouter/projects /var/lib/cottenrouter/upstreams; do
  [[ ! -e $path && ! -L $path ]] || { echo "Purge left $path" >&2; exit 1; }
done
for service in cottendns masterdnsvpn stormdns thefeed-server slipgate-dnstt soar-server soar22-server stormdns-egress-filter; do
  ! systemctl is-active --quiet "$service"
  [[ ! -e /etc/systemd/system/${service}.service ]]
done
! id cottenrouter >/dev/null 2>&1
! id slipgate >/dev/null 2>&1
echo 'Confirmed full purge removed managed services, binaries, data, identities and accounts.'
