#!/usr/bin/env bash
set -euo pipefail
umask 077
[[ ${COTTENROUTER_SYSTEMD_TEST:-} == 1 && ${WSL_DISTRO_NAME:-} == CottenRouter-Test && $EUID == 0 ]] || exit 1
[[ ! -e /etc/cottenrouter && ! -e /opt/cottenrouter/backends ]] || { echo 'Requires a fresh disposable router install.' >&2; exit 1; }
source_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
logs=/root/cottenrouter-validation/logs
mkdir -p "$logs" /root/cottenrouter-validation/old-cottendns
bash "$source_dir/scripts/install.sh" --local-source="$source_dir" --no-swap > "$logs/update-bootstrap.log" 2>&1
old=v2026.09.26.091349-cdf084f
archive=CottenDns_Server_Linux_AMD64.zip
cd /root/cottenrouter-validation/old-cottendns
for file in "$archive" SHA256SUMS.txt; do
  curl -fsSL --retry 3 "https://github.com/TaJirax/cottenDNS/releases/download/$old/$file" -o "$file"
done
awk -v name="$archive" '{ n=split($2, parts, "/"); if (parts[n] == name) print $1 "  " name }' SHA256SUMS.txt > selected.sha256
[[ -s selected.sha256 ]]
sha256sum -c selected.sha256
backend=/opt/cottenrouter/backends/cottendns
install -d -m 0750 "$backend"
unzip -q -o "$archive" -d "$backend"
binary="$backend/CottenDns_Server_Linux_AMD64_$old"
chmod 0755 "$binary"
"$binary" --version
python3 - "$backend/server_config.toml" <<'PY'
import pathlib,re,sys
p=pathlib.Path(sys.argv[1]); s=p.read_text()
for key,value in {'DOMAIN':'["updates.systemd.test"]','UDP_HOST':'"127.0.0.1"','UDP_PORT':'5301','TCP_LISTENER_ENABLED':'true'}.items():
    pattern=r'(?m)^\s*'+key+r'\s*=.*$'
    if re.search(pattern,s): s=re.sub(pattern,key+' = '+value,s)
    else: s+='\n'+key+' = '+value+'\n'
p.write_text(s)
PY
cat > /etc/systemd/system/cottendns.service <<EOF
[Unit]
Description=Old CottenDNS upgrade fixture
[Service]
WorkingDirectory=$backend
ExecStart=$binary --config $backend/server_config.toml
Restart=on-failure
[Install]
WantedBy=multi-user.target
EOF
chmod 0644 /etc/systemd/system/cottendns.service
systemctl daemon-reload
systemctl enable --now cottendns
for attempt in {1..50}; do [[ -s $backend/encrypt_key.txt ]] && break; sleep .1; done
cottenrouter configure --project cottendns --domain updates.systemd.test --tcp > "$logs/update-fixture-configure.log" 2>&1
sha256sum "$backend/encrypt_key.txt" "$backend/server_config.toml" /etc/cottenrouter/config.json /usr/local/bin/cottenrouter > "$logs/update-preserve.sha256"
# Reproduce the old installer's mtime-based selection bug deliberately.
touch -d 'next day' "$binary"
cottenrouter update --project cottendns > "$logs/cottendns-update.log" 2>&1
sha256sum --check --status "$logs/update-preserve.sha256"
python3 - "$backend" <<'PY'
import json,pathlib,re,subprocess,sys
receipt=json.loads(pathlib.Path('/var/lib/cottenrouter/projects/cottendns.json').read_text())
tag=receipt['release_tag']
assert tag != 'v2026.09.26.091349-cdf084f', tag
unit=subprocess.check_output(['systemctl','show','cottendns','--property=ExecStart','--value'],text=True)
binary=re.search(r'path=([^ ;}]+)',unit)[1]
assert pathlib.Path(binary).parent == pathlib.Path(sys.argv[1])
version=subprocess.check_output([binary,'--version'],text=True).strip()
assert tag in version, version
print('Verified real upgrade to '+tag+' with old executable still present and newer by mtime.')
PY
cottenrouter healthz --config /etc/cottenrouter/config.json
systemctl is-active cottendns cottenrouter
echo 'Backend update preserved the key, backend/router config and CottenRouter executable bytes.'
