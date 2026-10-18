#!/usr/bin/env bash
# Exercise persisted installer ownership across first install, upgrades, and
# failed upgrades without changing the host firewall or service manager.
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
fixture_dir=$(mktemp -d)
trap 'rm -rf -- "${fixture_dir}"' EXIT
FIREWALL_MARKER=${fixture_dir}/firewall.marker
backup_dir=${fixture_dir}/backup
mkdir "${backup_dir}"

extract_function() {
  awk -v name="$1" '$0 ~ "^" name "\\(\\) \\{" { found=1 } found { print } found && /^}/ { exit }' "${script_dir}/install.sh"
}
for function_name in fail fetch download_go_archive path_present backup_file restore_file rollback_firewall load_firewall_ownership save_firewall_ownership; do
  eval "$(extract_function "${function_name}")"
  [[ $(type -t "${function_name}") == function ]] || { echo "FAIL: missing ${function_name}" >&2; exit 1; }
done

# The supported Ubuntu 20.04 curl lacks --retry-all-errors; do not pass an
# unsupported flag, while retaining it on newer hosts.
curl() {
  if [[ $1 == --help ]]; then
    [[ ${curl_has_retry_all} == false ]] || printf '%s\n' '--retry-all-errors'
    return 0
  fi
  printf '%s\n' "$*" > "${fixture_dir}/curl.args"
}
curl_has_retry_all=false
fetch https://example.invalid/source
if grep -q -- '--retry-all-errors' "${fixture_dir}/curl.args"; then
  echo 'FAIL: old curl received --retry-all-errors' >&2
  exit 1
fi
curl_has_retry_all=true
fetch https://example.invalid/source
grep -q -- '--retry-all-errors' "${fixture_dir}/curl.args" || { echo 'FAIL: new curl lost retries on all errors' >&2; exit 1; }

# A blocked Google archive endpoint falls back to a mirror, while a working
# canonical download never needs it. The build verifies the same official
# SHA-256 after this helper returns, whichever source supplied the archive.
fetch() {
  printf '%s\n' "$1" >> "${fixture_dir}/fetch.calls"
  [[ $1 != https://go.dev/dl/* || ${official_download_fails} == false ]]
}
official_download_fails=false
download_go_archive go1.25.0.linux-amd64.tar.gz "${fixture_dir}/go.tar.gz"
[[ $(<"${fixture_dir}/fetch.calls") == 'https://go.dev/dl/go1.25.0.linux-amd64.tar.gz' ]] || { echo 'FAIL: healthy official Go download unexpectedly used a mirror' >&2; exit 1; }
rm -- "${fixture_dir}/fetch.calls"
official_download_fails=true
download_go_archive go1.25.0.linux-amd64.tar.gz "${fixture_dir}/go.tar.gz" 2>/dev/null
[[ $(<"${fixture_dir}/fetch.calls") == $'https://go.dev/dl/go1.25.0.linux-amd64.tar.gz\nhttps://mirrors.aliyun.com/golang/go1.25.0.linux-amd64.tar.gz' ]] || { echo 'FAIL: blocked official Go download did not use the expected mirror' >&2; exit 1; }

assert_marker() {
  local expected=$1
  [[ $(<"${FIREWALL_MARKER}") == "${expected}" ]] || {
    printf 'FAIL: firewall ownership marker\ngot:\n%s\nwant:\n%s\n' "$(<"${FIREWALL_MARKER}")" "${expected}" >&2
    exit 1
  }
}

load_firewall_ownership
firewall_kind=ufw
firewall_added_ports=(53/udp 53/tcp)
save_firewall_ownership
assert_marker $'kind ufw\nport 53/udp\nport 53/tcp'

# Reinstallation sees existing rules and adds none; their ownership survives.
load_firewall_ownership
firewall_added_ports=()
save_firewall_ownership
assert_marker $'kind ufw\nport 53/udp\nport 53/tcp'

# A later TLS listener is appended without dropping or duplicating old rules.
load_firewall_ownership
firewall_added_ports=(53/udp 853/tcp)
save_firewall_ownership
assert_marker $'kind ufw\nport 53/udp\nport 53/tcp\nport 853/tcp'

# A failed upgrade undoes only its new rules and restores the original record.
backup_file firewall_marker "${FIREWALL_MARKER}"
ufw() { printf '%s\n' "$*" >> "${fixture_dir}/ufw.calls"; }
firewall_added_ports=(443/tcp)
save_firewall_ownership
rollback_firewall
restore_file firewall_marker "${FIREWALL_MARKER}"
assert_marker $'kind ufw\nport 53/udp\nport 53/tcp\nport 853/tcp'
[[ $(<"${fixture_dir}/ufw.calls") == '--force delete allow 443/tcp' ]] || { echo 'FAIL: rollback removed an existing firewall rule' >&2; exit 1; }

# Invalid ownership records are rejected before they can drive tool arguments.
for invalid in $'kind ufw\nport 0/tcp' $'kind ufw\nport 65536/tcp' $'kind ufw\nkind firewalld' $'kind ufw\nport 53/tcp extra'; do
  printf '%s\n' "${invalid}" > "${FIREWALL_MARKER}"
  if (load_firewall_ownership) >/dev/null 2>&1; then
    echo 'FAIL: invalid firewall marker accepted' >&2
    exit 1
  fi
done

echo 'install.sh state transitions OK'
