import {execFile} from 'node:child_process';
import {promisify} from 'node:util';
import {readFile} from 'node:fs/promises';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const run = promisify(execFile);
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

const {stdout} = await run('git', ['ls-files', '-z'], {cwd: root, maxBuffer: 8 * 1024 * 1024});
const trackedFiles = stdout.split('\0').filter(Boolean);
const forbiddenPath = /(^|\/)(license-signer-local|\.license-signer)(\/|$)|\.license\.(private|signing)\./i;
const privateKeyMaterial = /-----BEGIN(?: [A-Z0-9]+)? PRIVATE KEY-----/;
const forbiddenSecretField = /(?:license|signer)[_-]?(?:private|secret)[_-]?(?:key|seed)|privateKeyPem/i;

for (const file of trackedFiles) {
  assert(!forbiddenPath.test(file), `tracked signer or private License path: ${file}`);
  const bytes = await readFile(path.join(root, file));
  const content = bytes.toString('utf8');
  assert(!privateKeyMaterial.test(content), `private-key PEM material is tracked: ${file}`);
  assert(!forbiddenSecretField.test(content), `private License/signer field is tracked: ${file}`);
}

const offlineE2E = await readText('tests/e2e/offline-license.mjs');
assert(!offlineE2E.includes('generateKeyPair'), 'offline E2E must not generate a License signing key');
assert(!offlineE2E.includes('createPrivateKey'), 'offline E2E must not create a License private key');
assert(!offlineE2E.includes('privateKey'), 'offline E2E must not handle a License private key');
assert(offlineE2E.includes('CI === \'true\''), 'offline E2E must enforce its local-only boundary');

const compose = await readText('tests/e2e/offline-license.compose.yaml');
assert(compose.includes('internal: true'), 'offline E2E network must be internal');
assert(compose.includes('read_only: true'), 'offline License fixture must be mounted read-only');
assert(compose.includes('AEP_LICENSE_TRUSTED_KEYS_FILE'), 'offline E2E must mount trusted public keys');
assert(compose.includes('AEP_LICENSE_FILE'), 'offline E2E must mount the signed License envelope');
assert(!compose.includes('AEP_LICENSE_PRIVATE'), 'offline Compose must not define a License private-key variable');
assert(!compose.includes('LICENSE_SIGNER'), 'offline Compose must not define a signer variable');

const fixture = JSON.parse(await readText('tests/e2e/fixtures/offline-license.json'));
const trustedKeys = JSON.parse(await readText('tests/e2e/fixtures/offline-license-trusted-keys.json'));
assert(fixture.format === 'zhiyuan-license-v1', 'offline fixture must be a v1 License envelope');
assert(typeof fixture.signature === 'string' && fixture.signature.length > 0, 'offline fixture must contain a public signed envelope');
assert(Object.keys(trustedKeys).length === 1 && trustedKeys[fixture.keyId], 'offline fixture must contain its public verification key');
assert(!JSON.stringify(fixture).match(/private|secret|signer/i), 'offline License fixture contains a forbidden sensitive field');
assert(!JSON.stringify(trustedKeys).match(/private|secret|signer/i), 'offline trusted-key fixture contains a forbidden sensitive field');

const workflowFiles = trackedFiles.filter(file => file.startsWith('.github/workflows/'));
for (const file of workflowFiles) {
  const content = await readText(file);
  assert(!content.includes('AEP_OFFLINE_SIGNING_KEY_BASE64'), `${file} passes an offline signing key to CI`);
  assert(!content.includes('AEP_LICENSE_PRIVATE'), `${file} references a License private key`);
  assert(!content.toLowerCase().includes('license-signer'), `${file} references the offline signer`);
}

console.log(`License boundary audit passed: ${trackedFiles.length} tracked files checked; no signer or private-key material found.`);

async function readText(relativePath) {
  return readFile(path.join(root, relativePath), 'utf8');
}

function assert(condition, message) {
  if (!condition) throw new Error(`License boundary audit failed: ${message}`);
}
