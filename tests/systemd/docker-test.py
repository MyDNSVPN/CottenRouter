#!/usr/bin/env python3
"""Run the built image and verify UDP/TCP routing with a local DNS origin."""
import json
import os
import pathlib
import socket
import struct
import subprocess
import sys
import threading
import time

if (os.geteuid() != 0 or os.environ.get('WSL_DISTRO_NAME') != 'CottenRouter-Test'
        or os.environ.get('COTTENROUTER_SYSTEMD_TEST') != '1'):
    raise SystemExit('Requires root and COTTENROUTER_SYSTEMD_TEST=1 in CottenRouter-Test')

name = 'cottenrouter-systemd-routing'
root = pathlib.Path('/root/cottenrouter-validation')
root.mkdir(exist_ok=True)
config = root / 'docker-smoke.json'
config.write_text(json.dumps({'listen_udp': '127.0.0.1:15353', 'listen_tcp': '127.0.0.1:15353',
                             'admin_listen': '127.0.0.1:19088',
                             'routes': [{'name': 'fixture', 'domains': ['docker.systemd.test'],
                                         'backend': '127.0.0.1:53099'}]}))
config.chmod(0o644)
udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
udp.bind(('127.0.0.1', 53099))
tcp = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
tcp.bind(('127.0.0.1', 53099))
tcp.listen()


def response(query):
    # A complete TXT answer, with a pointer to the original question.
    return (query[:2] + struct.pack('!5H', 0x8400, 1, 1, 0, 0) + query[12:]
            + b'\xc0\x0c' + struct.pack('!2HIH', 16, 1, 0, 7) + b'\x06docker')


def read_exact(sock, length):
    data = b''
    while len(data) < length:
        part = sock.recv(length - len(data))
        assert part, 'truncated TCP message'
        data += part
    return data


def udp_origin():
    while True:
        query, address = udp.recvfrom(65535)
        udp.sendto(response(query), address)


def tcp_origin():
    while True:
        conn, _ = tcp.accept()
        with conn:
            query = read_exact(conn, struct.unpack('!H', read_exact(conn, 2))[0])
            reply = response(query)
            conn.sendall(struct.pack('!H', len(reply)) + reply)


threading.Thread(target=udp_origin, daemon=True).start()
threading.Thread(target=tcp_origin, daemon=True).start()
try:
    subprocess.run(['docker', 'run', '-d', '--name', name, '--network', 'host', '--read-only',
                    '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
                    '--mount', f'type=bind,src={config},dst=/etc/cottenrouter/config.json,readonly',
                    'cottenrouter:systemd-test'], check=True)
    user = subprocess.check_output(['docker', 'inspect', '--format={{.Config.User}}', name], text=True).strip()
    assert user == '65532:65532', user
    for attempt in range(40):
        if subprocess.run(['docker', 'exec', name, '/usr/local/bin/cottenrouter', 'healthz',
                           '-config', '/etc/cottenrouter/config.json']).returncode == 0:
            break
        time.sleep(0.2)
    else:
        raise AssertionError('container did not become healthy')
    query = (struct.pack('!6H', 0x7314, 0x100, 1, 0, 0, 0)
             + b'\x06docker\x07systemd\x04test\x00' + struct.pack('!2H', 16, 1))
    for protocol in (socket.SOCK_DGRAM, socket.SOCK_STREAM):
        with socket.socket(socket.AF_INET, protocol) as client:
            client.settimeout(5)
            client.connect(('127.0.0.1', 15353))
            if protocol == socket.SOCK_DGRAM:
                client.sendall(query)
                reply = client.recv(65535)
            else:
                client.sendall(struct.pack('!H', len(query)) + query)
                reply = read_exact(client, struct.unpack('!H', read_exact(client, 2))[0])
            assert reply == response(query), 'container changed DNS reply'
    print('Docker: non-root, read-only, no capabilities, health, UDP and TCP routing passed')
finally:
    if sys.exc_info()[0] is not None:
        subprocess.run(['docker', 'logs', name], check=False)
    subprocess.run(['docker', 'rm', '-f', name], check=False)
