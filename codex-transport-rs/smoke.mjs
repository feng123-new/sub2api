// Reproducible IPC smoke check with no credentials or outbound requests by default.
// Usage: node smoke.mjs [--upstream-probe]
import assert from 'node:assert/strict';
import http from 'node:http';

const socketPath = process.env.CODEX_RUST_SOCKET || '/run/codex-rust/transport.sock';

async function call(path, method = 'GET', headers = {}) {
  return await new Promise((resolve, reject) => {
    const request = http.request({ socketPath, path, method, headers }, (response) => {
      const chunks = [];
      response.on('data', (chunk) => chunks.push(chunk));
      response.on('end', () => resolve({
        status: response.statusCode,
        headers: response.headers,
        body: Buffer.concat(chunks).toString('utf8'),
      }));
      response.on('error', reject);
    });
    request.setTimeout(25000, () => request.destroy(new Error('IPC request timed out')));
    request.on('error', reject);
    request.end();
  });
}

const health = await call('/health');
assert.equal(health.status, 200);
assert.equal(health.body, 'ok');

const badTarget = await call('/forward', 'POST', {
  'x-codex-rust-target': 'https://example.org/backend-api/codex/responses',
  'x-codex-rust-method': 'GET',
  'x-codex-rust-account': '42',
});
assert.equal(badTarget.status, 502);
assert.equal(badTarget.headers['x-codex-rust-error'], 'invalid_target');

const badAccount = await call('/forward', 'POST', {
  'x-codex-rust-target': 'https://chatgpt.com/backend-api/codex/models',
  'x-codex-rust-method': 'GET',
  'x-codex-rust-account': '0',
});
assert.equal(badAccount.status, 502);
assert.equal(badAccount.headers['x-codex-rust-error'], 'invalid_account');
console.log('IPC health and validation: PASS');

if (process.argv.includes('--upstream-probe')) {
  // Deliberately invalid bearer; a normal upstream 401/403/other HTTP status
  // proves the TLS request reached the upstream, without using real accounts.
  const result = await call('/forward', 'POST', {
    'x-codex-rust-target': 'https://chatgpt.com/backend-api/codex/models',
    'x-codex-rust-method': 'GET',
    'x-codex-rust-account': '42',
    authorization: 'Bearer deliberately-invalid-smoke-token',
  });
  assert.equal(result.headers['x-codex-rust-error'], undefined,
    `outbound transport error: ${result.headers['x-codex-rust-error']}`);
  assert.ok(result.status >= 100 && result.status <= 599);
  console.log(`Upstream HTTP status: ${result.status}; protocol: ${result.headers['x-codex-rust-upstream-proto'] || 'unknown'}`);
}
