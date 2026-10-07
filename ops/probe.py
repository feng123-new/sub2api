#!/usr/bin/env python3
"""Check the deployed release; optional admin credentials exist only in memory."""
import argparse
import base64
import hashlib
import hmac
import json
import os
from pathlib import Path
import subprocess
import time
import urllib.error
import urllib.request

ROOT = Path('/opt/sub2api-deploy')


def sql(query):
    return subprocess.check_output([
        'docker', 'exec', os.environ.get('SUB2API_PROBE_DB', 'sub2api-postgres'),
        'psql', '-U', 'sub2api', '-d', 'sub2api', '-XAt', '-v', 'ON_ERROR_STOP=1',
        '-c', query], text=True).strip()


def request(path, token=None, body=None, timeout=30):
    headers = {'User-Agent': 'sub2api-ops-probe'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    if body is not None:
        headers['Content-Type'] = 'application/json'
    url = os.environ.get('SUB2API_PROBE_BASE', 'http://127.0.0.1:8080') + path
    req = urllib.request.Request(url, headers=headers,
                                 data=json.dumps(body).encode() if body is not None else None)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.read()


def admin_token():
    user = json.loads(sql("SELECT row_to_json(t) FROM (SELECT id,email,password_hash "
                          "FROM users WHERE role='admin' AND status='active' "
                          "AND deleted_at IS NULL ORDER BY id LIMIT 1) t"))
    secret = sql("SELECT value FROM security_secrets WHERE key='jwt_secret'")
    if not secret or not user:
        raise RuntimeError('Existing administrator and persisted JWT secret are required')
    fingerprint = hashlib.sha256((user['email'].strip().lower() + '\n' + user['password_hash']).encode()).digest()
    now = int(time.time())
    claims = {'user_id': user['id'], 'email': user['email'], 'role': 'admin',
              'token_version': int.from_bytes(fingerprint[:8], 'big') & 0x7fffffffffffffff,
              'iat': now, 'nbf': now, 'exp': now + 300}
    encode = lambda data: base64.urlsafe_b64encode(data).rstrip(b'=')
    unsigned = encode(b'{"alg":"HS256","typ":"JWT"}') + b'.' + encode(json.dumps(claims).encode())
    return (unsigned + b'.' + encode(hmac.new(secret.encode(), unsigned, hashlib.sha256).digest())).decode()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base-url', default='http://127.0.0.1:8080')
    parser.add_argument('--expected-version')
    parser.add_argument('--admin', action='store_true', help='also check ticket pool and one archived image')
    parser.add_argument('--out', type=Path, help='write sanitized JSON evidence')
    args = parser.parse_args()
    os.umask(0o077)
    os.environ['SUB2API_PROBE_BASE'] = args.base_url.rstrip('/')
    manifest = json.loads((ROOT / 'runtime/manifest.json').read_text())
    with (ROOT / 'runtime/sub2api').open('rb') as binary:
        digest = hashlib.file_digest(binary, 'sha256').hexdigest()
    container_digest = subprocess.check_output(
        ['docker', 'exec', 'sub2api', 'sha256sum', '/app/sub2api'], text=True).split()[0]
    assert digest == manifest['sha256'] == container_digest, 'Running binary does not match manifest'
    result = {'version': manifest['version'], 'commit': manifest['source_commit'],
              'sha256': digest, 'checks': {}}
    for path, expected in [('/health', 200), ('/api/v1/settings/public', 200),
                           ('/api/v1/admin/generated-images', 401), ('/v1/models', 401)]:
        status, raw = request(path)
        assert status == expected, (path, status)
        result['checks'][path] = status
        if path.endswith('/public'):
            version = json.loads(raw)['data']['version']
            assert version == (args.expected_version or manifest['version']), version
    if args.admin:
        token = admin_token()
        for path in ['/api/v1/auth/me', '/api/v1/admin/proxies/codex-ticket-pool']:
            status, _ = request(path, token)
            assert status == 200, (path, status)
            result['checks']['authenticated ' + path] = status
        status, raw = request('/api/v1/admin/generated-images?page=1&page_size=1', token)
        assert status == 200, ('image_list', status)
        items = json.loads(raw)['data']['items']
        result['checks']['image_list'] = status
        if items:
            status, image = request('/api/v1/admin/generated-images/' + items[0]['id'] + '/content', token)
            assert status == 200 and image, ('image_content', status)
            result['archive'] = {'bytes': len(image), 'sha256': hashlib.sha256(image).hexdigest()}
        result['migrations'] = int(sql('SELECT count(*) FROM schema_migrations'))
    rendered = json.dumps(result, indent=2) + '\n'
    if args.out:
        args.out.write_text(rendered)
    print(rendered, end='')


if __name__ == '__main__':
    main()
