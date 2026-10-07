#!/usr/bin/env python3
"""Exercise install.sh against an isolated release server; never install globally."""
import gzip
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import os
from pathlib import Path
import platform
import subprocess
import tempfile
import threading

root = Path(__file__).resolve().parent.parent
version = 'v9.8.7'
os_name = {'Darwin': 'darwin', 'Linux': 'linux'}[platform.system()]
arch = {'arm64': 'arm64', 'aarch64': 'arm64', 'x86_64': 'amd64'}[platform.machine()]
asset = f'live-{version}-{os_name}-{arch}'
binary = b'''#!/bin/sh
case "$1" in
version) echo 'live v9.8.7';;
login) echo called > "$LIVE_TEST_LOGIN_MARKER";;
*) exit 1;;
esac
'''
requests = []
bad_checksum = False

class Releases(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass
    def do_GET(self):
        requests.append(self.path)
        if self.path.endswith('/latest.txt'):
            body = version.encode()
        elif self.path.endswith('/' + asset + '.sha256'):
            body = (('0' * 64) if bad_checksum else hashlib.sha256(binary).hexdigest()).encode()
        elif self.path.endswith('/' + asset + '.gz'):
            body = gzip.compress(binary)
        else:
            self.send_error(404)
            return
        self.send_response(200)
        self.end_headers()
        self.wfile.write(body)

server = ThreadingHTTPServer(('127.0.0.1', 0), Releases)
threading.Thread(target=server.serve_forever, daemon=True).start()
origin = f'http://127.0.0.1:{server.server_port}'
try:
    with tempfile.TemporaryDirectory(prefix='live-install-test-') as tmp:
        dest = Path(tmp) / 'bin'
        env = dict(os.environ, LIVE_BIN_DIR=str(dest), LIVE_NO_MODIFY_PATH='1',
                   LIVE_GH_BASE=origin + '/releases/download',
                   LIVE_LATEST_URL=origin + '/releases/latest/download/latest.txt',
                   LIVE_TEST_LOGIN_MARKER=tmp + '/login')
        for key in ['LIVE_INSTALL_BASE', 'LIVE_VERSION', 'LIVE_FROM_SOURCE']:
            env.pop(key, None)
        def install(*args):
            return subprocess.run(['bash', str(root / 'install.sh'), *args],
                                  env=env, capture_output=True, text=True, timeout=20)
        result = install()
        assert result.returncode == 0, result.stderr
        assert (dest / 'live').read_bytes() == binary
        assert '/releases/latest/download/latest.txt' in requests
        assert f'/releases/download/{version}/{asset}.gz' in requests
        assert not Path(env['LIVE_TEST_LOGIN_MARKER']).exists(), 'Install started login'
        requests.clear()
        result = install('--version', version)
        assert result.returncode == 0, result.stderr
        assert not any(p.endswith('latest.txt') for p in requests)
        requests.clear()
        env['LIVE_INSTALL_BASE'] = origin + '/mirror'
        result = install()
        assert result.returncode == 0, result.stderr
        assert all(p.startswith('/mirror/') for p in requests)
        bad_checksum = True
        result = install()
        assert result.returncode != 0 and 'checksum mismatch' in result.stderr
        assert (dest / 'live').read_bytes() == binary, 'Failed update replaced installed binary'
        print('PASS: GitHub-style latest/pinned assets, explicit mirror, checksum rejection, no automatic login')
finally:
    server.shutdown()
    server.server_close()
